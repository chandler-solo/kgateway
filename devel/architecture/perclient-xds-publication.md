# Per-client xDS publication: bounded wait-for-consistency

Status: implemented. This document describes how per-client xDS snapshots are
published, why publication waits for consistency, and the machinery that
bounds that wait (#14184).

## 1. The consistency requirement

kgateway publishes a state-of-the-world (SotW) xDS snapshot per connected
Envoy client. SotW semantics make partial snapshots dangerous: a connecting
client may already hold a full, working config in memory (an Envoy
reconnecting after a stream drop or a control-plane restart), the control
plane cannot see what the client already has, and publishing a snapshot
missing some of those resources REMOVES them — an outage on a healthy proxy.

`snapshotPerClient` therefore withholds a client's snapshot until its
per-client inputs are consistent (the #13868 guards):

1. The per-client endpoints row has been derived.
2. Every cluster referenced as a dataplane routing target (RouteAction /
   TcpProxy; blackhole and errored clusters exempt) is present in the
   per-client CDS.
3. Every referenced EDS cluster has its ClusterLoadAssignment present.

A withheld snapshot surfaces as a KRT Delete that the xDS subscriber
deliberately ignores, so the cache retains the last published snapshot and
Envoy keeps serving its previous coherent config. A withhold is always safe;
only its DURATION can hurt.

## 2. The problem: the wait could be permanent

In production (#14184), the wait sometimes never ended. The per-client
collections are maintained by an eventually-consistent reactive dataflow with
no completeness oracle; field diagnostics showed durable holes — rows missing
for specific (client, backend) pairs while older clients kept theirs — that
no further event would ever repair, because the rows' own inputs never
changed again. One hole held a guard closed forever, which froze every other
update for that client: stranded on stale endpoints (upstream connect errors
to deleted pods), new endpoints never programmed, recovery only by restart.

Two adjacent defects rode along: stale CLAs retained in a snapshot make
go-control-plane suppress named state-of-the-world ADS EDS responses ("ADS
mode: not responding to request"), freezing endpoint delivery even after
inputs heal; and departed clients' cache entries were never reclaimed (an
unbounded leak).

## 3. The fix: make the wait reliably terminate

The guards stay exactly as they are. What changes is that the system can now
always make progress toward opening them, observe when it is not, and clean
up after departed clients:

1. **Demand-driven heartbeat.** A `RecomputeTrigger` dependency in EVERY
   transform on the per-client path — the leaf cluster/endpoint collections
   and the per-UCC stages alike, because a dropped recompute can land on any
   edge between collections, and re-running only the leaves cannot heal a
   stale row downstream of them (an unchanged leaf recompute is
   hash-suppressed and propagates nothing). Each tick fires the trigger if
   any connected client is stuck — deferred since its last publish, or
   connected-but-never-published (a brand-new client whose very first build
   deferred emits NO event at all, so only connected-set membership reveals
   it; that was exactly the stranded-cohort shape seen in production).
   Clients whose role has no per-gateway snapshot to publish are excluded,
   since no recompute can help them. A rare unconditional fallback tick
   covers signals the stuck tracking cannot see. A healthy fleet pays only a
   cheap per-tick check; unchanged recomputes are hash-suppressed by KRT, so
   even a firing tick causes no snapshot churn, and a dropped heartbeat
   recompute leaves previous rows in place — the heartbeat can heal holes but
   never create them. `KGW_PERCLIENT_HEARTBEAT_INTERVAL` tunes cadence;
   `<= 0` disables the heartbeat only.
2. **Reclaim loop** (independent of the heartbeat's off-switch): drops ALL
   tracked state for clients absent from the connected set past a grace
   period — clearing retained cache entries for published ones — with
   reconnect-resets. Fixes the unbounded SnapshotCache leak that "retain last
   good on delete" otherwise implies. Snapshot Delete events are classified
   by liveness: a still-connected client is marked stuck (a transform defer);
   a departed one only starts the reclaim clock, so routine pod churn never
   drives the heartbeat. Publication commits atomically with its bookkeeping
   (SetSnapshot under the reconciler lock, zeroing the orphan clock), so a
   reclaim pass racing a reconnect can never clear a snapshot that was just
   written.
3. **S2 — EDS subset with a two-signal version.** Published EDS resources are
   exactly the CLAs required by EDS clusters in the same snapshot's CDS,
   fixing the ADS not-responding failure. The filtered EDS version combines
   the filtered content with the upstream version: content alone misses the
   policy-attachment re-warm bump (envoyproxy/envoy#13009), upstream alone
   misses remove/re-add transitions — each omission leaves the EDS watch
   "up to date" and stalls Envoy on `initial_fetch_timeout`.

With these in place the defers are bounded in practice by one heartbeat
interval: a defer marks the client stuck, the next tick re-runs the whole
per-client path against current inputs, and the guards open as soon as the
inputs are coherent. A wait that persists across ticks indicates a genuinely
unresolved input (visible via the defer metrics below), never a silently
wedged pipeline.

## 4. Alternatives considered

| Alternative | Disposition |
|---|---|
| Publish the best available snapshot after a deferral budget expires | Rejected: a "new" client may be a warm Envoy holding full working config; a SotW publish of a partial snapshot removes everything not in it. Withholding is safe for warm clients and merely delays cold ones; the wait must terminate by HEALING, not by publishing subsets. |
| Resolve holes at publish time (carry forward missing clusters from the previously published snapshot; hold/omit referencing routes) | Designed and prototyped; rejected for the patch line as disproportionate: publish-time proto surgery on customer RDS/listener shapes carries more risk than the waits it shortens. |
| Hard-validate snapshots before publish (PGV, consistency, reference closure) | Out of scope for the patch line; the guards already prevent the incoherent-snapshot class this would catch. |
| Re-key the per-client collections onto the client axis | Not pursued for the patch line: it inverts the fan-out unfavorably for backend-churn-heavy clusters. The underlying event-delivery question is pursued upstream in istio/krt; the stress/reproducer harness lives at `pkg/kgateway/proxy_syncer/perclient_clusters_stress_test.go`. |

## 5. Observability

- `xds_snapshot_perclient_defers_total{reason}`: `endpoints_not_ready` (input
  not yet derived), `missing_clusters` (guard 2), `missing_endpoints`
  (guard 3). Transient blips are normal; a sustained rate for a gateway means
  a client's inputs are not becoming consistent despite the heartbeat —
  i.e., a genuinely unresolved input.
- `xds_snapshot_perclient_recoveries_total`: a client resumed publishing
  after a defer — with the heartbeat as backstop, recoveries of
  long-deferred clients are heartbeat-driven heals.
- `xds_snapshot_perclient_reclaimed_total`: departed-client cache entries
  cleared.

## 6. Test plan

- Unit: guard semantics (defer on missing referenced cluster, defer on
  missing CLA for a referenced EDS cluster, publish once inputs are
  consistent, errored-cluster and blackhole exemptions, zero-cluster
  gateways); S2 filter (static and stale CLAs dropped, deterministic version,
  version change when a required CLA arrives); reconciler lifecycle (recovery
  accounting, stuck-shape coverage including never-published clients and
  unpublishable-role exclusion, defer-vs-departure Delete classification,
  reclaim after grace with reconnect resets, departed-state sweep, publish
  resets the orphan clock); heartbeat suites (a tick re-runs per-client
  translation; stability assertions synchronized on the recompute actually
  having run).
- ADS respondability: named state-of-the-world EDS watches against a real
  `SnapshotCache.CreateWatch` for cluster removal,
  `EdsClusterConfig.service_name`, and remove-then-re-add (the S2
  version-derivation cases).
- Stress/reproducer: trigger-driven churn harnesses for the per-client
  collections, including sleep-injected windows (slow per-pair translation,
  post-read delays in the connected-client source) with quiescent,
  permanence-checked assertions — failures here reproduce the #14184
  stranding shape.
- e2e: `perclientxds` (endpoint-follow through rollout and scale).
