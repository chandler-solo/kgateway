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

The mechanism is deliberately small. Completeness is never a publication
*predicate* — it is a *signal* that bounds a deferral episode by a clock and
marks what ships as degraded, so nothing can wait forever and nothing degrades
silently:

1. **The transforms always build.** Each per-client collection always emits a
   row once derived — even an empty one (a gateway with zero backend clusters
   is a legitimate result) — so a nil `FetchOne` downstream unambiguously
   means "not derived yet" and `snapshotPerClient` defers
   (`clusters_not_ready` / `endpoints_not_ready`) rather than building from a
   placeholder. This matters for already-published clients: substituting an
   empty cluster set would publish a state-of-the-world CDS with every backend
   cluster removed — a whole-gateway connection drain for a transient.
   Otherwise the transform produces the best snapshot available from current
   inputs, reconciling EDS at build time: published CLAs are exactly those
   required by EDS clusters in the same snapshot's CDS (S2, below), with
   required-but-missing CLAs **synthesized empty** — keeping the snapshot
   consistent and the cluster warming instead of stalling on
   `initial_fetch_timeout` — and the synthesized names reported on the wrapper
   so publication can log, count, and mark them.

2. **Publish-time policy** (`syncXds`, before `SetSnapshot`):
   - **Hard failures always withhold**: nil/mistyped/misnamed resources,
     generated proto validation, duplicate listener filter chain matches,
     go-control-plane snapshot consistency, and SDS references whose secret is
     absent (a plugin bug, never a transient). Envoy keeps the last good
     snapshot; the heal is a new build (retrying the same data cannot help).
   - **Incomplete inputs defer, bounded per episode.** A snapshot is
     incomplete when dataplane cluster references (RouteAction / TcpProxy
     targets, precomputed once per gateway; blackhole and errored clusters
     exempt) are absent from CDS, or when CLAs were synthesized. The first
     incomplete attempt starts an episode clock; within the budget the update
     is withheld — for a cold client this is the warm-up the reconnect race
     #13868 needed, and for a published client it keeps a transiently
     incomplete rebuild from regressing Envoy's config (removing live
     clusters, wiping a backend's endpoints). Past the budget the snapshot
     publishes as-is, marked **degraded**, logged, and counted; the episode
     stays open so further incomplete updates flow immediately (endpoint
     updates must not freeze), and only a clean publish ends it.
   - **Withheld snapshots are retained and retried directly.** A withhold
     produces no KRT event, and an unchanged recompute is hash-suppressed, so
     budget expiry cannot depend on the event stream: the heartbeat loop
     re-attempts retained pending snapshots each tick. A sequence guard makes
     a retry that raced a newer event-driven publish a no-op. The effective
     deferral bound is the budget rounded up to the next heartbeat tick.
   - **Publication commits atomically.** `SetSnapshot` and the bookkeeping
     that depends on it (published/deferred/degraded state, the orphan-reclaim
     clock) happen under one lock, totally ordering publish against reclaim —
     a reconnect racing a reclaim pass can no longer have its just-written
     snapshot cleared.

3. **S2 — EDS subset with a two-signal version.** Published EDS resources are
   exactly the CLAs required by EDS clusters in the same snapshot's CDS,
   fixing the ADS not-responding failure. The reconciled EDS version combines
   the reconciled content with the upstream version: content alone misses the
   policy-attachment re-warm bump (envoyproxy/envoy#13009), upstream alone
   misses remove/re-add and synthesis transitions — each omission leaves the
   EDS watch "up to date" and stalls Envoy.

4. **Level-triggered liveness.** Correctness never depends on the dataflow
   library delivering every recompute edge:
   - **Every transform on the per-client path marks the heartbeat** — the
     leaf collections and the per-UCC stages alike. The event-delivery gap
     that motivates the heartbeat can drop a recompute on ANY edge between
     collections, and a heartbeat that re-runs only the leaves cannot heal a
     stale row downstream of them (an unchanged leaf recompute is
     hash-suppressed and propagates nothing).
   - A **demand-driven heartbeat** ticks cheaply and acts only when needed:
     each tick first re-attempts pending withheld snapshots, then fires the
     recompute trigger if any connected client is stuck — withheld, degraded,
     or connected-but-never-published (the latter catches clients whose very
     first build deferred and therefore emitted no KRT event; clients whose
     role has no per-gateway snapshot to publish are excluded, since no
     recompute can help them) — plus a rare unconditional fallback tick. A
     dropped heartbeat recompute leaves previous rows in place, so the
     heartbeat can heal holes but never create them.
     `KGW_PERCLIENT_HEARTBEAT_INTERVAL` tunes cadence; `<= 0` disables the
     heartbeat only.
   - A **reclaim loop** (independent of the heartbeat's off-switch) drops ALL
     tracked state for clients absent from the connected set past a grace
     period — clearing retained cache entries for published ones — fixing the
     unbounded SnapshotCache leak that "retain last good on delete" otherwise
     implies, and guaranteeing no stuck signal outlives its client. Snapshot
     Delete events are classified by liveness: a still-connected client is
     marked stuck (transform defer); a departed one only starts the reclaim
     clock, so routine pod churn never drives the heartbeat.

## 3. Accepted transients

Every error window this design accepts is transient, bounded, and no worse
than production behavior before #13868:

| Window | Behavior | Bound |
|---|---|---|
| Incomplete-inputs episode (cold-client warm-up, or a transiently incomplete rebuild for a published client) | No new errors: update withheld; Envoy keeps its last coherent config (a cold client has none yet) | episode budget + one heartbeat tick |
| Persistently incomplete inputs past the budget | Degraded publish: no-cluster errors on affected routes, no-healthy-upstream on synthesized CLAs — logged and counted, client marked stuck | heals on the tick/event that completes the inputs |
| Internal pipeline hole | Withheld (incomplete) or degraded publish, per the episode state; heartbeat re-runs the whole per-client path | one heartbeat interval |
| Backend scaled to zero | Empty CLA publishes immediately (truth); fast no-healthy-upstream | none — correct behavior |

What can no longer happen: a client stranded on stale endpoints, a gateway
frozen by one incomplete input, endpoints failing to propagate, ADS EDS
responses suppressed by stale CLAs, a malformed resource overwriting a good
snapshot, a transient rebuild draining a gateway's live clusters or silently
wiping a backend's endpoints, or any deferral lasting beyond its budget plus
one heartbeat tick.

## 4. Alternatives considered

| Alternative | Disposition |
|---|---|
| Keep the #13868 gate (defer until complete) | Rejected: with no completeness oracle, permanent deferral is inherent (section 1). |
| Hard-fail validation on missing cluster references | Rejected: cross-collection skew makes missing references an expected transient; hard-failing recreates the #13868 gate at the publish layer. Detection is kept; the deferral is clock-bounded per episode and ends in a marked degraded publish, never a permanent withhold. |
| Carry missing clusters forward from the last published snapshot, and hold/omit individual routes until their targets are ready | Designed and prototyped; rejected for the patch line as disproportionate: publish-time proto surgery on customer RDS/listener shapes carries more risk than the transients it removes. Recorded here as the designed follow-up if field metrics show the residual transients matter. |
| Gate the snapshot on endpoint usability (CLA with a healthy endpoint) | Rejected: any referenced scale-to-zero backend would freeze the whole client and pin stale endpoints — strictly worse than the warming 503s it prevents. |
| Re-key the per-client collections onto the client axis | Not pursued: not needed for correctness once liveness is level-triggered, and it inverts the fan-out unfavorably for backend-churn-heavy clusters. The underlying dropped-recompute behavior is pursued upstream in istio/krt (the stress harness lives at `pkg/kgateway/proxy_syncer/perclient_clusters_stress_test.go`). |

## 5. Observability

Every counter draws a hard line between "withheld" and "served degraded":

- `xds_snapshot_perclient_defers_total{reason}`: the update was WITHHELD.
  Reasons: `incomplete_inputs` (clock-bounded episode deferral),
  `invalid_snapshot` (hard validation failure — a bug),
  `clusters_not_ready` / `endpoints_not_ready` (a per-client input not yet
  derived).
- `xds_snapshot_perclient_degraded_publishes_total{reason}`: the snapshot WAS
  published, with known-incomplete data, past the episode budget. Reasons:
  `missing_clusters` (no-cluster errors on the referencing routes),
  `synthesized_load_assignments` (the backend serves no endpoints until the
  real CLA arrives). Traffic is being served; the heartbeat is healing.
- `xds_snapshot_perclient_recoveries_total`: a CLEAN publish after a withhold
  or a degraded publish. `..._reclaimed_total`: departed-client cache entries
  cleared.

## 6. Test plan

- Unit: publish-time policy (coherent publish, hard-invalid withholds,
  episode-bounded incomplete-inputs deferral for both missing clusters and
  synthesized CLAs, budget expiry driven through the pending-retry path with
  no KRT event, episode-stays-open-after-degraded, fresh-episode deferral for
  a published client, errored-cluster exemption); build-time EDS
  reconciliation (static and stale CLAs dropped, missing required CLAs
  synthesized and reported, deterministic version, version change on
  synthesis-to-real transitions); reconciler lifecycle (recovery accounting
  incl. degraded-is-not-recovery, stuck detection incl.
  unpublishable-role exclusion, defer-vs-departure Delete classification,
  reclaim after grace, never-published departed state swept, publish resets
  the orphan clock, pending-retry sequence guard); validator
  resource/reference/filter-chain checks; heartbeat suites (re-run reaches the
  per-client transforms; stability assertions synchronized on the recompute
  actually having run).
- ADS respondability: named state-of-the-world EDS watches against a real
  `SnapshotCache.CreateWatch` for cluster removal,
  `EdsClusterConfig.service_name`, and remove-then-re-add (the S2
  version-derivation cases).
- e2e: `perclientxds` (endpoint-follow through rollout and scale, including
  scale-to-zero propagation).
