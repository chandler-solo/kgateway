# Per-client xDS publication: design

Status: implemented. This document is the reference for how per-client xDS
snapshots are published and for the invariants publication maintains. It
supersedes the deferral behavior introduced by #13868.

## 1. Problem

PR #13868 made per-client snapshot publication conditional on *completeness*:
`snapshotPerClient` returned nil (a KRT Delete the syncer intentionally
ignores) unless every cluster referenced by LDS/RDS was present, and every
referenced EDS cluster had a CLA. Three production failure modes followed
(#14184): a single Envoy stranded on stale endpoints forever (upstream connect
failures to deleted pods), a redeployed backend whose new endpoints were never
programmed, and whole-gateway starvation under modest route churn (new routes
404; recovery only after churn stopped plus a controller roll).

The root defect is the predicate, not its implementation: *"publish only when
complete"* is evaluated inside an eventually-consistent dataflow that has
**no completeness oracle**. Field diagnostics showed the per-client
collections can develop **permanent holes** — rows missing for specific
(client, backend) pairs after fan-out recomputes are dropped — so a gate that
waits for completeness can wait forever, and one hole freezes every other
update for that client.

Two adjacent defects ride along. Stale CLAs retained in a snapshot make
go-control-plane suppress named state-of-the-world ADS EDS responses
(`ADS mode: not responding to request ...`). And nothing validated final xDS
resources before they reached the cache: a malformed or nil resource from any
plugin would overwrite the last good snapshot.

## 2. Design

The mechanism is deliberately small. The problem #13868 actually needed to
solve — a reconnecting client briefly receiving a partial CDS and returning
NC/500 on routes that were healthy — is a **warm-up** problem, so the remedy
is scoped to warm-up and bounded by a clock, never by a completeness predicate:

1. **The transform always builds.** `snapshotPerClient` produces the best
   snapshot available from current inputs. It returns nil only when an input
   has not been derived at all yet (the per-client collections, driven by the
   same events, may briefly lag this handler).

2. **Publish-time validation** (`syncXds`, before `SetSnapshot`):
   - **Hard failures always withhold**: nil/mistyped/misnamed resources,
     generated proto validation, duplicate listener filter chain matches,
     go-control-plane snapshot consistency, and SDS references whose secret is
     absent (a plugin bug, never a transient). Envoy keeps the last good
     snapshot; the heartbeat retries.
   - **Required-but-missing CLAs are synthesized empty**, keeping the snapshot
     consistent and letting the cluster finish warming immediately instead of
     stalling on `initial_fetch_timeout`. The affected backend serves
     no-healthy-upstream until the real CLA arrives.
   - **Missing dataplane cluster references** (RouteAction / TcpProxy targets
     absent from CDS; blackhole and errored clusters exempt) defer publication
     ONLY while the client has **never been published** and its **warm-up
     deadline** has not expired. The deadline makes deferral bounded by
     construction: the predicate is advisory, the clock is the guarantee.
     After first publish — or deadline expiry — the snapshot publishes as-is;
     the referencing routes transiently return no-cluster errors (the
     pre-#13868 behavior) and heal on the next input event or heartbeat tick.

3. **S2 — EDS subset with a two-signal version.** Published EDS resources are
   exactly the CLAs required by EDS clusters in the same snapshot's CDS,
   fixing the ADS not-responding failure. The filtered EDS version combines
   the filtered content with the upstream version: content alone misses the
   policy-attachment re-warm bump (envoyproxy/envoy#13009), upstream alone
   misses remove/re-add transitions — each omission leaves the EDS watch
   "up to date" and stalls Envoy.

4. **Level-triggered liveness.** Correctness never depends on the dataflow
   library delivering every recompute edge:
   - A **demand-driven heartbeat** re-runs the per-client collections when any
     connected client is stuck (deferred since last publish, or
     connected-but-never-published — the latter catches clients whose very
     first build deferred and therefore emitted no KRT event), plus a rare
     unconditional fallback tick. A dropped heartbeat recompute leaves
     previous rows in place, so the heartbeat can heal holes but never create
     them. `KGW_PERCLIENT_HEARTBEAT_INTERVAL` tunes cadence; `<= 0` disables
     the heartbeat only.
   - A **reclaim loop** (independent of the heartbeat's off-switch) clears
     retained cache entries for clients absent from the connected set past a
     grace period, fixing the unbounded SnapshotCache leak that "retain last
     good on delete" otherwise implies.

## 3. Accepted transients

Every error window this design accepts is transient, bounded, and was
production behavior before #13868:

| Window | Behavior | Bound |
|---|---|---|
| Client warm-up | No errors: first publish deferred until coherent or deadline | warm-up budget + one heartbeat tick |
| New route whose backend translates a beat later | Brief no-cluster errors on that route only | next input event (typically sub-second) |
| Internal pipeline hole | No-cluster errors on affected routes only | one heartbeat interval |
| Backend scaled to zero | Empty CLA publishes immediately (truth); fast no-healthy-upstream | none — correct behavior |

What can no longer happen: a client stranded on stale endpoints, a gateway
frozen by one incomplete input, endpoints failing to propagate, ADS EDS
responses suppressed by stale CLAs, or a malformed resource overwriting a
good snapshot.

## 4. Alternatives considered

| Alternative | Disposition |
|---|---|
| Keep the #13868 gate (defer until complete) | Rejected: with no completeness oracle, permanent deferral is inherent (section 1). |
| Hard-fail validation on missing cluster references | Rejected: cross-collection skew makes missing references an expected transient; hard-failing recreates the #13868 gate at the publish layer. Detection is kept; the policy is warm-up-scoped. |
| Carry missing clusters forward from the last published snapshot, and hold/omit individual routes until their targets are ready | Designed and prototyped; rejected for the patch line as disproportionate: publish-time proto surgery on customer RDS/listener shapes carries more risk than the transients it removes. Recorded here as the designed follow-up if field metrics show the residual transients matter. |
| Gate the snapshot on endpoint usability (CLA with a healthy endpoint) | Rejected: any referenced scale-to-zero backend would freeze the whole client and pin stale endpoints — strictly worse than the warming 503s it prevents. |
| Re-key the per-client collections onto the client axis | Not pursued: not needed for correctness once liveness is level-triggered, and it inverts the fan-out unfavorably for backend-churn-heavy clusters. The underlying dropped-recompute behavior is pursued upstream in istio/krt (the stress harness lives at `pkg/kgateway/proxy_syncer/perclient_clusters_stress_test.go`). |

## 5. Observability

- `xds_snapshot_perclient_defers_total{reason}`: `warmup` (bounded first-publish
  deferral), `missing_clusters_published` (post-warm-up partial publish — the
  transient-NC signal), `invalid_snapshot` (hard validation failure — a bug),
  `endpoints_not_ready` (input not yet derived).
- `xds_snapshot_perclient_recoveries_total`, `..._reclaimed_total`.

## 6. Test plan

- Unit: publish-time policy (coherent publish, hard-invalid withholds,
  missing-CLA synthesis, warm-up deferral bounded by the clock, errored-cluster
  exemption, publish-after-first-publish); S2 filter (static and stale CLAs
  dropped, deterministic version); transform-level partial builds; validator
  resource/reference/filter-chain checks; heartbeat and reclaim suites;
  contract and concurrency coverage for the per-client collections.
- ADS respondability: named state-of-the-world EDS watches against a real
  `SnapshotCache.CreateWatch` for cluster removal,
  `EdsClusterConfig.service_name`, and remove-then-re-add (the S2
  version-derivation cases).
- e2e: `perclientxds` (endpoint-follow through rollout and scale, including
  scale-to-zero propagation).
