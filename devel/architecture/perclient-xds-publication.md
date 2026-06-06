# Per-client xDS publication: design

Status: implemented. This document is the reference for how per-client xDS
snapshots are built and published, and for the invariants that publication
maintains. It supersedes the deferral behavior introduced by #13868.

## 1. Problem

PR #13868 made per-client snapshot publication conditional on *completeness*:
`snapshotPerClient` returned nil (a KRT Delete the syncer intentionally
ignores) unless every cluster referenced by LDS/RDS was present, and every
referenced EDS cluster had a CLA. Three production failure modes followed, all
rooted in one inverted invariant:

1. **Stranding** (#14184, report 1): one Envoy of N stuck on stale endpoints
   forever -> upstream connect failures to deleted pods.
2. **Single-backend staleness** (#14184, report 2): a redeployed backend's new
   endpoints never programmed while everything else looks healthy.
3. **Whole-gateway starvation**: all clients of all gateways defer
   continuously under modest route churn; new routes 404; recovery only after
   churn stops plus a controller roll.

The inverted invariant: *"publish only when complete"* is evaluated inside an
eventually-consistent dataflow that has **no completeness oracle**. Blocking on
a predicate nothing guarantees will become true makes permanent deferral
inherent, not incidental.

Field diagnostics for failure mode 3 supplied the mechanism: the per-client
clusters collection (`PerClientEnvoyClusters`, keyed on backends, fetching
connected clients) had **permanent holes** — a cohort of clients missing rows
for the same set of stable backends, plus a backend with zero rows for any
client — created when fan-out recomputes (one per backend on every client
connect) were dropped or ran against a stale client fetch, and never re-ran
because stable backends never change. The gate then converts any hole that
intersects the referenced set into a frozen gateway.

A fourth, independent defect rides along: **stale CLAs block ADS EDS
responses**. go-control-plane refuses to answer a state-of-the-world named EDS
watch when the snapshot contains CLAs outside the requested name set, so
retained CLAs for removed clusters turn "retain last good" into an EDS
progress failure (`ADS mode: not responding to request ...
ClusterLoadAssignment [...]`).

## 2. Design invariants

Safety is obtained **by construction of the snapshot**, never by withholding
publication. Liveness is obtained by level-triggered reconciliation, never
assumed from edge delivery.

Safety (every published per-client snapshot):

- **S1 — Reference closure.** Every cluster referenced as a dataplane routing
  target by the snapshot's LDS/RDS is present in the snapshot's CDS (or is the
  blackhole sentinel). Preserved from #13868, but enforced by construction
  (sections 3-4), not by deferral.
- **S2 — EDS subset.** The snapshot's EDS resources are exactly the CLAs
  required by EDS clusters present in the same snapshot's CDS. This is what
  makes go-control-plane answer named state-of-the-world EDS watches; it fixes
  the ADS not-responding failure. Any mechanism that injects clusters
  (carry-forward) must move the cluster and its CLA as a unit. The filtered
  EDS version is derived from both the filtered content and the upstream
  version: content alone misses the policy-attachment re-warm bump, upstream
  alone misses remove/re-add transitions — each omission leaves the EDS watch
  "up to date" and stalls Envoy on `initial_fetch_timeout`.
- **S3 — Endpoint truth.** If a cluster is present in the current per-client
  input, its CLA in the snapshot is its *current* translation — including an
  empty CLA for a backend scaled to zero. Old endpoints are never retained for
  a cluster the pipeline currently knows. (This kills failure modes 1-2
  permanently; it is the existing intent of `newFinalBackendEndpoints`.)

Liveness:

- **L1 — Publish liveness.** Every relevant input change produces a published
  snapshot for every connected client. `snapshotPerClient` returns nil only
  when there is no gateway translation for the client's role at all.
- **L2 — Bounded staleness.** Any internal hole (a per-client row missing or
  stale relative to current inputs) heals within one heartbeat interval
  (default 30s). This is the level-triggered foundation: correctness does not
  depend on the dataflow library delivering every recompute edge.

Route-granular make-before-break:

- **S4 — Route transition readiness.** A route *change* (new route, or a
  retarget to a different cluster) activates only when its new target is
  usable; until then the route holds its previous form (or is withheld if
  new). Endpoint truth (S3) is unaffected: unchanged routes always see current
  endpoints.

"Usable" means: the cluster is non-EDS, or its CLA contains at least one
endpoint not marked `UNHEALTHY`. This mirrors Envoy's own warming semantics —
a route flipped onto a cluster with no usable endpoint can only serve 503s.

## 3. Per-cluster publication rules

For each cluster `C` referenced by the about-to-publish LDS/RDS (blackhole and
explicitly errored clusters exempt, as today):

| # | Condition | Action | Rationale |
|---|-----------|--------|-----------|
| R1 | `C` present in per-client clusters | Publish `C` with its **current** CLA, even if empty | S3. A scale-to-zero empty CLA is the truth; deferring or carrying old endpoints recreates failure modes 1-2. |
| R2 | `C` absent, but present in the last published snapshot | **Carry forward** previous `C` + its CLA, as a unit | The absence is an internal hole (field evidence: holes are pipeline artifacts, not cluster reality). Continuity for `C`, progress for everything else. The heartbeat replaces the carried copy with fresh translation within L2. |
| R3 | `C` absent and never published (brand-new cluster not yet translated) | **Withhold the referencing routes only** (hold each at its previous version if it had one; omit if new). Publish everything else. | Nothing exists to carry; publishing the route would violate S1 or 503. Withholding a route that never worked harms no traffic. Heals on the backend's translation event or the next heartbeat. |

Missing-CLA edge under R1: for a present EDS cluster whose CLA row is absent
(an endpoints-side hole — the endpoints pipeline always emits a CLA for known
backends, so absence is a hole, not "no endpoints"), carry the previous CLA if
one was published, else synthesize an empty CLA (counted by metric). Either
way S2 holds and Envoy is not left waiting on `initial_fetch_timeout`.

Notes:

- A dangling route (Service deleted, route not yet retranslated) may briefly
  hit R2 and carry a dead cluster; harmless — that route's backend is gone
  regardless, no other route is affected, and route retranslation converges it
  to the blackhole sentinel.
- Carried clusters are by definition absent from current input, so R2 does not
  conflict with S3.

## 4. Per-route make-before-break (S4)

R3 already requires holding individual routes, which is the same machinery S4
needs. S4 extends the hold condition from "target absent" to "target not
usable" for route *changes* only:

- A route entry or TCP filter chain intervenes only if it is **new** or its
  **target set changed** relative to the previously published entry (matched
  by RouteConfiguration name, virtual host name, and route name — or listener
  name and filter chain name — and compared by cluster references, so an edit
  that keeps the same target never holds).
- An intervened entry keeps its previously published form when one exists and
  is itself satisfiable, else it is omitted. A held entry may target a
  now-unusable cluster: that is what Envoy is already serving, so keeping it
  is a no-op for the dataplane.
- Unchanged routes are never held: if their target's endpoints went away, S3
  publishes that truth (Envoy fast-fails with no-healthy-upstream, which is
  correct).
- Transition checking is skipped on controller cold start (no previously
  published baseline to define "changed"; Envoy's own cluster warming covers
  that window — and a fresh controller must not 404 routes to
  steady-state-empty backends that Envoy is already serving) and on
  endpoints-only updates (gated on the route/listener resource versions
  differing from the previously published ones).

The alternative — gating the *whole snapshot* on endpoint usability — closes
the same warming window but makes any referenced scale-to-zero backend freeze
the entire gateway and pins stale endpoints for it (an S3 violation, and
strictly worse than the 503s it prevents). The usable-endpoint predicate is
sound only at route-transition granularity.

## 5. Liveness machinery

- **Demand-driven heartbeat.** A `RecomputeTrigger` dependency in the
  per-client cluster/endpoint transforms; fired when any connected client is
  stuck (deferred since last publish, or connected-but-never-published — the
  latter catches clients whose very first build deferred and therefore emitted
  no KRT event), plus a rare unconditional fallback tick. A dropped heartbeat
  recompute leaves previous rows in place, so the heartbeat can heal holes but
  never create them. `KGW_PERCLIENT_HEARTBEAT_INTERVAL` tunes cadence;
  `<= 0` disables the heartbeat only.
- **Reclaim loop** (independent of the heartbeat's off-switch): clears
  retained cache entries for clients absent from the connected set past a
  grace period, with reconnect-resets. Fixes the unbounded SnapshotCache leak
  that "retain last good on delete" otherwise implies.
- With R1-R3 in place, deferral events are rare; the heartbeat remains as the
  guarantee that internal holes are bounded (L2) regardless of event-delivery
  behavior, and the stuck-client signal naturally includes clients held up by
  the publish-time safety net.

## 6. Alternatives considered

| Alternative | Disposition |
|---|---|
| Keep the #13868 gate (defer until complete) | Rejected: permanent deferral is inherent (section 1). Preserved behind `KGW_LEGACY_SNAPSHOT_GATE=true` for one release as an operational escape hatch; the heartbeat and reclaim loops still run in that mode. |
| Demote the EDS-CLA gate to a warning (publish despite missing CLAs) | Rejected alone: re-opens the warming 503 window for route retargets. Superseded by the R1 missing-CLA edge + S4. |
| Whole-snapshot carry-forward at publish time | Reworked into R2: carry-forward must be per-cluster, with the CLA traveling alongside the cluster, or it violates S2 and can mask brand-new clusters (R3 cases) it cannot satisfy. |
| Gate the whole snapshot on endpoint usability | Rejected: fixes warming but freezes gateways on scale-to-zero and pins stale endpoints (S3 violation). Its predicate is adopted at route granularity as S4. |
| Re-key the per-client collections onto the client axis | Not pursued: with L1/L2 in place it is no longer needed for correctness, and it inverts the fan-out unfavorably — every backend create/delete would recompute all clients x all backends, which is the hot path in churn-heavy clusters. The underlying dropped-recompute behavior is pursued upstream in istio/krt instead. |
| Delete-branch cache clearing | Retained as a no-op for defer windows (last-good continuity); true departures are reclaimed by the reconciler after a grace period. |

## 7. Observability

- `xds_snapshot_perclient_defers_total{reason}` — should approach zero;
  sustained nonzero indicates a publish-time hold or a regression.
- `xds_snapshot_perclient_recoveries_total`, `..._reclaimed_total`.
- `xds_snapshot_perclient_carried_clusters_total`,
  `..._held_routes_total{action=held|omitted}`,
  `..._synthesized_load_assignments_total` — sustained carried/held counts
  identify a long-lived internal hole (pipeline bug) vs transient churn.

## 8. Rollout

1. **Patch line (`v2.3.x`):** S2 filter, R1-R3, heartbeat + reclaim, metrics.
   Escape hatch: `KGW_LEGACY_SNAPSHOT_GATE=true` restores the #13868 deferral
   behavior for one release.
2. **Main:** S4 (route diff/hold machinery) and the `xds_warming` e2e suite.
3. **Independent:** file the dropped-fan-out delivery issue against istio/krt
   (the stress harness lives at
   `pkg/kgateway/proxy_syncer/perclient_clusters_stress_test.go`); a native
   incremental join primitive upstream would remove the fan-out shape
   entirely.

## 9. Test plan

- Unit: per-rule tests for R1 (empty-CLA publishes; scale-to-zero replaces
  endpoints), R2 (carry unit + S2 filter), R3 (route withheld, rest publish),
  the missing-CLA edge (carry vs synthesize), S4 (hold-retarget-until-usable,
  omit-new, cold-start skip, version fast-path skip, TCP chains, and the S3
  guard: an unchanged route publishes its empty CLA), the usable predicate,
  the publish-time safety net, and the legacy gate; heartbeat and reclaim
  suites; contract and concurrency coverage for the per-client collections.
- ADS respondability: named state-of-the-world EDS watches against a real
  `SnapshotCache.CreateWatch` for cluster removal, `EdsClusterConfig.
  service_name`, and remove-then-re-add (the S2 version-derivation cases).
- e2e: `xds_warming` (S4 against a real Envoy: route retarget, weighted split,
  and new-route scenarios) and `perclientxds` (endpoint-follow, S3).
