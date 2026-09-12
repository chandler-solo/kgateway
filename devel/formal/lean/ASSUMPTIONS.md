# Spec assumptions and evidence

The Lean proofs establish properties of their transition relation. Tests
characterize selected implementation behavior; they cannot universally
discharge dependency assumptions. The machine-readable links in
`devel/testing/formal-assumptions.yaml` are checked for declarations and
anchors. Execution is a separate obligation. See
[assurance status](../assurance-status.md) for the current profile and limits.

## GCP-A1 Named EDS watch respondability

**Open; original iff statement is contradicted.** The convergence model's
`canRespond`/`edsWatchResponds` guard uses a differing version plus snapshot
names contained in the subscription. v0.14.0 `CreateWatch` also responds at
unchanged version to newly subscribed resources not yet returned. Both its
immediate and parked-watch paths can discard a declined named request.

The filtered-EDS and service-name cache tests establish specific successful
requests, not the full rule. RF-007 requires an explicit lifecycle model,
returned-resource history, and probes of both lost-watch paths. The existing
convergence theorem is conditional on its simplified guard.

## GCP-A2 Last-good retention on delete

**Implementation-characterized.** The kgateway collection Delete callback is
a no-op, retaining its snapshot cache entry. This is not a guarantee about
`ClearSnapshot`, process restart, or cache failure. `deleteRetainsLastGood`
models that callback and `TestSnapshotPerClientClientRemovalRetainsServedCache`
exercises it.

## GCP-A3 ADS wire-delivery ordering windows

**Implementation-characterized.** The three `TestADS*` ordering probes in
`xds_delivery_order_probe_test.go` observe quiet-stream CDS-before-RDS,
ACK-skew RDS-before-CDS in both modes, and combined removal CDS-before-RDS
in both modes. `OrderedADS.lean` represents those schedules.

Setup now passes `EnableOrderedAds` to `NewControlPlane`; the earlier claim
that the option is unavailable is stale. Fixed type ordering does not close
ACK-skew or removal windows. `gracefulRemovalSystem` observes that the old
route is inactive before removal. A fixed elapsed grace does not implement
that guard without an application/latency assumption. Action RF-010: model
and probe a real barrier or explicitly bounded timeout policy.

## GCP-A4 Per-stream callback serialization

**Implementation-characterized.** The v0.14.0 SotW processing goroutine
serializes request callbacks and defers its close callback, including after
a request callback error. `TestADSCallbacksAreSerializedPerStream` and
`TestADSStreamClosedFiresAfterRequestError` exercise those paths.
`ClientIdentity.lean` relies on atomic per-stream actions. This does not
establish ordering between distinct streams or correctness of every shared
identity-map interleaving.

## ENV-A1 Envoy activation

**Open (RF-004).** `activateNew` and `activeSnapshotClosed` require abstract
dependency closure. Existing warming e2e tests run through kgateway's gate
and cannot prove an independent Envoy usable-endpoint guard. Empty EDS can
initialize a cluster without usable traffic. C0 allows cold publication with
complete CDS and empty CLAs, including a warm proxy reconnecting to a fresh
cache. Direct, pinned Envoy probes must separate receipt, ACK, initialization,
listener activation, and traffic.

## IMPL-A1 EDS version digest

**Open (RF-008); contract stated, runtime detection added.** `versionEq`
uses name-set equality. `VersionDigest.lean` proves that abstraction is
blind to a same-name payload change (`nameSetVersion_blind`), so the
convergence proofs' version statements are about name sets only. Go hashes
each deterministically marshalled CLA with 64-bit FNV-1a and XORs the
results; `xorDigest_cancels` shows the combiner alone can collide when two
resources share a per-resource digest. No finite digest is injective over
unbounded content, so `DigestContract` assumes determinism on content-equal
sets and collision freedom only on the domain a run compares.

Evidence: `TestFilterEndpointResourcesForClusters_VersionDigestProperties`
checks determinism, order invariance, and no collisions in its finite corpus.
Schema-2 traces carry each CLA's digest, and `TraceCheck.lean` fails a run in
which one client's EDS version is unchanged while its content changed
(`version-reuse`) and counts unchanged content with a moved version
(`version-churn`). These detect violations in observed runs; they do not
prove the assumption. A monotonic revision allocator would discharge the
collision assumption but would make equivalent content produce distinct
versions, defeating the churn suppression that PR #14516 relies on.

## KRT-A1 Eventual coherent inputs

**Open; fairness stated.** A dropped fan-out can leave a client permanently
partial. The abstract `heartbeatRederive` supplies a coherent candidate, and
`stuck_client_has_recovery_path` proves existence of a recovery path. Neither
proves that a production timer runs, reads authoritative truth, obtains
coherent inputs, or receives fair downstream execution.

`../tla/KrtRecovery.tla` states the temporal claim under explicit weak
fairness. Source inventory found no watchdog, ticker, or periodic
re-derivation in `pkg/kgateway/proxy_syncer` or `pkg/kgateway/setup`, so
the heartbeat action has no implementation counterpart. The deployed
profile is `KrtRecoveryCoherentInput`: convergence of a deferred-partial
client rests entirely on KRT eventually delivering the missing dependency.
Without that assumption the property fails, and a watchdog that replays the
stuck cached derivation fails too; only a watchdog that rereads
authoritative inputs restores it. Whether KRT delivery is fair in
production remains implementation evidence to collect (RF-005).

## IMPL-A2 Per-client isolation

**Implementation-characterized.** `TestSnapshotPerClientPartialUpdateForOneClientDoesNotPoisonAnotherClient`
checks two different client keys. Lean `isolation` and `multi_safety` use
disjoint abstract state components by construction. Shared keys, concurrent
streams, cache-wide locks, and stale callbacks remain composition obligations.

## GCP-A5 Named-watch lifecycle characterization

**Implementation-characterized; the publish-entry half repaired in the pin.**
`gcpprobe` exercises both declined-watch paths and equal-version new
subscriptions using the root module. Since the 2026-09-12 merge the root pin
is go-control-plane `1cd122661`: a declined parked watch is retained and the
next aligned snapshot is delivered to it (`TestParkedNamedWatchIsRetainedOnDeclinedResponse`),
which is the retention policy `GcpWatch.lean` proposed; the request-entry
decline still registers no watch (`TestDeclinedNewRequestRegistersNoWatch`).
`GcpWatch.lean` keeps the v0.14.0 loss counterexample and the retention
policy over 534 finite states, so both pins remain explained. This seam supplements the
incomplete GCP-A1 guard; it does not yet refine the composed convergence proof.
RF-007 tracks cache installation, stream state, and that refinement.
