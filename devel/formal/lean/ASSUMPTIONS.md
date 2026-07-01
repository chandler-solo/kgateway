# Spec assumptions and how each is discharged

The Lean spec in this directory proves safety of the kgateway per-client
xDS publication state machine. Like every model, it stands on
assumptions about the parts of the system it does not define: the
go-control-plane snapshot cache, Envoy's warming behavior, and a few
implementation details it abstracts. This ledger names each assumption,
points at where the spec relies on it, and lists the test that
discharges it against the real component.

The machine-readable mapping lives in
`devel/testing/formal-assumptions.yaml` and is gated by
`devel/testing/formal_assumptions_test.go`, which fails if a referenced
test or spec anchor disappears. A regression therefore either breaks a
Lean proof, breaks a discharging test, or breaks the gate that ties the
two together.

## GCP-A1: named EDS watch respondability

go-control-plane answers a state-of-the-world ADS EDS watch if and only
if the watch's version differs from the snapshot version and every EDS
resource in the snapshot is named in Envoy's request.

- Spec reliance: `canRespond` and the `edsWatchResponds` guard in
  `XdsSpec/Spec.lean`; the `AlignedEDSRequestRespondable` invariant is
  only meaningful under this response rule (issue 14184 was exactly
  this rule interacting with stale CLAs).
- Discharged by: `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`
  and `TestSnapshotPerClientServiceNameEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`
  in `pkg/kgateway/proxy_syncer/perclient_test.go`, which drive the real
  `SnapshotCache.CreateWatch`.

## GCP-A2: last-good retention on delete

The xDS snapshot cache retains the previously published snapshot when a
per-client collection entry is deleted (the Delete branch in
proxy_syncer's xDS subscriber is a no-op).

- Spec reliance: `deferPartialInput` leaves `cache` and `lastGood`
  untouched; the `DeleteRetainsLastGood` invariant asserts the defer
  window serves last-good config.
- Discharged by: `TestSnapshotPerClientDeleteDuringPartialUpdateRetainsServedCache`
  in `pkg/kgateway/proxy_syncer/perclient_test.go`.

## GCP-A3: ADS wire-delivery ordering windows

Snapshot coherence is not wire coherence: go-control-plane delivers each
resource type as its own DiscoveryResponse, and the order decides
whether Envoy transiently applies a route whose cluster it does not have
(`503 NC`). Probing the real `server.StreamAggregatedResources`
established three facts:

1. On a quiet stream, additions arrive CDS before RDS in both server
   modes, because the cache itself writes responses in type order
   (`pkg/cache/v3/order.go`). The default server's `reflect.Select`
   drain randomizes only when several per-type channels are ready at
   once (busy streams); `server.WithOrderedADS()` closes that residual
   addition window (PR
   https://github.com/kgateway-dev/kgateway/pull/14341).
2. ACK skew defeats both modes deterministically: after a CDS response
   is sent, that watch is closed until Envoy ACKs it. A snapshot landing
   in that window (new cluster + route retarget) can only answer the
   open RDS watch, so the route reaches the wire before the CDS carrying
   its cluster. SotW answers only open watches; no server option closes
   this. In kgateway this is reachable whenever a route is retargeted to
   a new backend while an earlier CDS-only update is still un-ACKed
   (CDS churns on any backend change for the client).
3. Removals are delivered in the WRONG order in both modes: a combined
   de-reference + cluster-removal snapshot ships the CDS change first
   (cache type order; cemented by WithOrderedADS), so the still-applied
   route briefly references a removed cluster. Only a control-plane
   grace window — de-reference in one snapshot, remove the cluster in a
   later one — is safe; kgateway computes combined snapshots in a single
   KRT recompute and has no such window today.

- Spec reliance: `XdsSpec/OrderedADS.lean`. `orderedAdditionSystem`
  keeps `ActiveRouteHasCluster`; `unorderedAdditionBugSystem` (busy
  streams), `ackSkewAdditionBugSystem`, and
  `orderedRemovalStillBrokenBugSystem` each violate it;
  `gracefulRemovalSystem` keeps it via the grace window.
- Discharged by (characterization, against the real server):
  `TestADSAdditionOnQuietStreamIsClusterFirst`,
  `TestADSAckSkewDeliversRouteBeforeClusterEvenWithOrderedADS`, and
  `TestADSOrderedServerStillDeliversClusterRemovalBeforeRouteUpdate` in
  `pkg/kgateway/proxy_syncer/xds_delivery_order_probe_test.go`.
- Remediation state: kgateway does not yet pass `WithOrderedADS()`
  (`pkg/kgateway/setup/controlplane.go`; PR #14341 adds it — necessary
  for busy streams but not sufficient: the ACK-skew and removal windows
  need control-plane pacing/grace, tracked in
  `devel/testing/formal-model-map.yaml`).

## ENV-A1: Envoy warming and make-before-break

Envoy activates a route/listener against a cluster only after the
cluster is ACKed and a ClusterLoadAssignment with a usable endpoint has
been received; until then it keeps serving its previous configuration.

- Spec reliance: the `activateNew` guard and the `ActiveSnapshotClosed`
  invariant; the `edsResponded -> activeNew` ordering.
- Discharged by: the `test/e2e/features/xds_warming` suite
  (route retarget, weighted split, and cold-start scenarios against a
  real Envoy).

## IMPL-A1: EDS version is an injective content digest

The spec models xDS versions as digests of EDS content
(`Version Name := Option (List Name)` with content equality), so the
`EDSResourceSetChangeChangesVersion` proof assumes the implementation's
version string is deterministic for content-equal resource sets and
different for content-different sets. The implementation uses an XOR of
per-resource proto hashes (`filterEndpointResourcesForClusters` in
`pkg/kgateway/proxy_syncer/perclient.go`), which is order-invariant and
deterministic by construction but only probabilistically injective.

- Spec reliance: `versionEq` in `XdsSpec/Spec.lean`; the
  `cacheVerDigest`/`clientVerDigest` conjuncts of `IndInv` in
  `XdsSpec/Proofs.lean`.
- Discharged by: `TestFilterEndpointResourcesForClusters_VersionDigestProperties`
  in `pkg/kgateway/proxy_syncer/perclient_version_property_test.go`
  (determinism, order invariance, and injectivity over a corpus).

## KRT-A1: per-client inputs eventually become coherent (OPEN)

The KRT fan-out that drives the per-client collections eventually
delivers the events that make `snapshotPerClient`'s inputs reflect the
current `clients x backends` truth — no event is dropped permanently.

This is the assumption the production stale-endpoints incident
violated: a dropped fan-out left one replica's inputs permanently
partial, so the client sat in the defer window forever. Nothing the
safety proofs establish was broken — convergence was simply never
reached, which is why this entry exists: the ledger must name liveness
assumptions, not only safety ones.

- Spec reliance: `inputBecomesCoherent` in `XdsSpec/Spec.lean` models
  the fan-out event arriving. `heartbeatRederive` models the watchdog
  that discharges this assumption mechanically by re-deriving the
  inputs from current truth on a timer. The progress theorem
  `stuck_client_converges` (`XdsSpec/Liveness.lean`) proves the
  heartbeat is sufficient: any reachable deferred state converges
  within one heartbeat re-derivation (at most five steps). The model
  checker reproduces both sides at the finite instance: the
  `DroppedFanoutBug` system (no coherence event) violates
  `DeferredPartial ~> Converged`, and `DroppedFanoutWithHeartbeat`
  restores it.
- Status: **open** on this branch. The discharging mechanism is the
  defer watchdog on the `fix/defer-watchdog` branch (a periodic
  reconciler that re-publishes for any live proxy without a current
  snapshot past a threshold). When that lands here, list its tests as
  the discharge and flip this entry to discharged.

## IMPL-A2: per-client isolation

`snapshotPerClient`'s KRT transform for one UniquelyConnectedClient
writes only that client's snapshot-cache entry.

- Spec reliance: `applyClientAction` in `XdsSpec/MultiClient.lean`
  updates a single client's component by construction; the `isolation`
  theorem makes the frame property explicit, and `multi_safety` uses it
  to lift safety to any number of clients.
- Discharged by: `TestSnapshotPerClientPartialUpdateForOneClientDoesNotPoisonAnotherClient`
  in `pkg/kgateway/proxy_syncer/perclient_test.go`.
