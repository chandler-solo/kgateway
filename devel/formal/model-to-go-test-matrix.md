# Model-to-Go test matrix

## Purpose

The TLA+ models are executable specifications for small xDS state machines. They do not inspect Go code. This matrix turns each modeled obligation into a Go testing obligation so a reviewer can answer: "Which Go test would fail if kgateway violated this model?"

The expected workflow is:

1. Use TLC to check the abstract model and read any counterexample trace.
2. Translate the trace into a concrete Go unit, integration, or e2e test.
3. Assert concrete resource names, resource versions, cache contents, xDS watch behavior, and `xdscheck` findings.
4. Keep the TLA+ model as the smallest explanation for why the Go test exists.

## Coverage statuses

- Covered: an existing Go test should fail if kgateway violates the obligation in the modeled way.
- Partial: existing tests cover a nearby shape, but not the full modeled transition or not the real runtime boundary.
- Gap: no Go test currently pins the obligation.
- External: the behavior is primarily owned by go-control-plane or Envoy; kgateway only needs a Go test if it wraps, configures, or depends on that behavior in a fragile way.

## Where Go tests should live

- `pkg/kgateway/translator/xdscheck`: pure Envoy proto snapshot invariants and precise finding messages.
- `pkg/kgateway/translator/gateway/gateway_translator_test.go`: representative IR -> xDS translator fixtures run through `xdscheck`.
- `pkg/kgateway/proxy_syncer/perclient_test.go`: per-client KRT and `SnapshotCache` publication traces, including issue 13868 and issue 14184 shapes.
- `test/e2e/features/xds_warming`: real Envoy startup, warming, reconnect, and active dataplane behavior that cannot be proven from emitted snapshots alone.

## Snapshot closure matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsAdsSotw.SentSnapshotsAreDependencyClosed` | Publish LDS/RDS/CDS/EDS with missing dependencies. | `TestCheckSnapshotMissingRDSReferencedByListener`, `TestCheckSnapshotMissingCDSClusterReferencedByRoute`, `TestCheckSnapshotMissingEDSAssignmentReferencedByEDSCluster`; translator fixtures `TestTranslated*SnapshotPassesXDSCheck`. | Covered | Keep adding translator fixtures for new policy/filter features that emit cluster, endpoint, or secret references. |
| `XdsPerClientPublication.CacheSnapshotCoherent` | Per-client cache contains missing route clusters, missing EDS resources, or orphan EDS resources. | `assertNoXDSCheckErrors` in the issue-focused `proxy_syncer` tests; xdscheck missing/orphan tests. | Covered | Convert more per-client tests through `assertNoXDSCheckErrors` when they produce full snapshots. |
| `XdsPerClientConvergence.CacheSnapshotClosed` | Cached refs, CDS, and EDS are not closed after an update. | `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`, `TestSnapshotPerClientPartialUpdateForOneClientDoesNotPoisonAnotherClient`, xdscheck closure tests. | Covered | Extend when more per-client resource types are added. |
| `XdsEdsSubset.NoOrphanEndpointResources` | EDS publishes a `ClusterLoadAssignment` for a cluster no emitted EDS cluster can request. | `TestCheckSnapshotOrphanClusterLoadAssignment`, `TestCheckSnapshotStaticClusterLoadAssignmentIsOrphan`, `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`. | Covered | Keep the orphan CLA finding at error severity for ADS named EDS safety. |
| `XdsEdsSubset.EDSRequestRespondable` and `ResponseMatchesADSGuard` | Snapshot contains resources outside Envoy's named EDS request, so ADS cannot answer. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` and `TestSnapshotPerClientServiceNameEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` use go-control-plane `SnapshotCache.CreateWatch`. | Covered | Add mixed cluster-name and service-name variants if that combination regresses. |
| `XdsNamedEdsWatch.ResponseOnlyForRequestedNames` | go-control-plane response contains EDS resources Envoy did not request. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` checks returned resources exclude `cluster-b`. | Covered | Add a direct xDS server stream test only if kgateway adds custom watch filtering. |
| Resource name uniqueness | Duplicate LDS/RDS/CDS/EDS/SDS names create ambiguous snapshots. | `TestCheckSnapshotDuplicateResourceNames`. | Covered | Add translator fixture coverage if a new naming scheme is introduced. |
| Dynamic route cluster references | `cluster_header` cannot be statically validated as a CDS dependency. | `TestCheckSnapshotClusterHeaderRouteIsWarningOnly`. | Covered | Keep this as warning severity unless a policy forbids `cluster_header`. |
| Unknown typed configs | Validator panics or silently claims safety when it cannot unpack a config. | `TestCheckSnapshotUnknownHCMTypedConfigDoesNotPanic` and other unknown typed-config tests. | Covered | Add warning-only tests for each new extension family the checker partially recognizes. |

## Issue 13868 and per-client publication matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsReconnectRace13868.ServedSnapshotReferencesResolved` | Reconnect publishes a partial per-client snapshot whose routes/listeners reference a missing CDS cluster. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`. | Covered | Add a two-client variant where only one client's cluster set is partial. |
| `XdsReconnectRace13868.PublishedSnapshotIsCurrentWhenPublished` | After all referenced clusters are ready or errored, the published snapshot does not match the current computed state. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady` asserts exact published CDS names; `TestSnapshotPerClientStillPublishesWhenReferencedClusterErrored` asserts errored clusters. | Covered | Keep exact-name assertions when adding new readiness exceptions. |
| `XdsPerClientPublication.CacheReferencesResolved` | Cache route refs include a cluster missing from CDS and not explicitly errored. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`, `TestSnapshotPerClientStillPublishesWhenReferencedClusterErrored`; xdscheck missing-cluster tests. | Covered | Keep blackhole cluster behavior separate from missing referenced clusters. |
| `XdsPerClientPublication.CacheReferencedEndpointsPresent` | Referenced EDS cluster is present in CDS but its CLA is missing. | `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints`, `TestSnapshotPerClientDefersUntilReferencedEDSServiceNameHasEndpoints`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`, and `TestSnapshotPerClientDefersWeightedRouteUntilAllEndpointsReady`. | Covered | Keep direct, weighted, service-name, and cluster-name cases together. |
| `XdsPerClientPublication.CacheHasNoOrphanEndpointResources` | Stale CLA remains after CDS shrinks. | `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`. | Covered | Add a static-cluster stale CLA per-client test if static clusters can appear in per-client snapshots. |
| `XdsPerClientPublication.CoherentInputsCanPublish` | Inputs become coherent but the KRT collection never republishes. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`, `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints`, `TestSnapshotPerClientDefersUntilReferencedEDSServiceNameHasEndpoints`, `TestSnapshotPerClientDeleteDuringPartialUpdateRetainsServedCache`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`, and `TestSnapshotPerClientDefersWeightedRouteUntilAllEndpointsReady` use bounded `Eventually` after the missing input appears. | Covered | Add new progress tests when adding new per-client dependency gates. |
| `XdsPerClientConvergence.DeleteRetainsLastGood` | A KRT delete/defer event clears the served last-good xDS cache. | `TestSnapshotPerClientDeleteDuringPartialUpdateRetainsServedCache` starts with a coherent served `SnapshotCache`, introduces a partial update, and asserts the served cache retains old resources and versions. | Covered | Keep delete/defer events as no-op for the served cache unless a distinct UCC-gone signal is added. |
| `XdsPerClientConvergence.PartialDoesNotOverwriteCache` | Partial computed state overwrites a coherent per-client cache. | `TestSnapshotPerClientDeleteDuringPartialUpdateRetainsServedCache` asserts the partial route does not reach the served cache; deferral tests assert no partial KRT output. | Covered | Add analogous tests if new dependency gates are introduced. |
| `XdsPerClientConvergence.CoherentInputCanPublish` | Publication remains disabled even after CDS and EDS become closed. | Same bounded `Eventually` tests as above. | Covered | Include the old and new resource versions in the assertions when practical. |
| `XdsPerClientConvergence.CoherentNewEventuallyActive` | A coherent desired snapshot never reaches active use. | `TestKgateway/XdsWarming/TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic` observes real Envoy traffic moving from old to new only after the new backend endpoints exist. `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointHasUsableEndpoint` covers the unit-level empty-CLA case found by the e2e. | Covered | Add more hosts/backends only if another production shape escapes this route-update scenario. |

## Issue 14184 and named EDS matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsNamedEdsWatch.ChangedSnapshotRequestRespondable` | EDS snapshot is version-new but contains stale extra resources outside Envoy's named request. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`, `TestSnapshotPerClientServiceNameEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`; xdscheck orphan CLA tests. | Covered | Add a mixed cluster-name/service-name snapshot if that shape appears in production. |
| `XdsNamedEdsWatch.ResourceSetChangeRequiresVersionChange` | Filtered EDS resource set changes but EDS version is reused. | `TestFilterEndpointResourcesForClusters_FiltersStaticClusterCLAs`, `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`, `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`. | Covered | Assert version determinism for equivalent filtered resource sets if hash churn becomes a concern. |
| `XdsNamedEdsWatch.ChangedRespondableSnapshotCanSend` | A version-new, request-compatible EDS snapshot opens a watch instead of responding. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` waits for an immediate go-control-plane response. | Covered | Add a regression test around newly subscribed resources with unchanged version if kgateway depends on that path. |
| `XdsNamedEdsWatch.NoSuppressedChangedResponse` | ADS suppresses a changed EDS response because the snapshot is not name-compatible. | Same named ADS watch test plus orphan CLA validator. | Covered | Keep the production fix at the cache input boundary by filtering EDS, not by changing go-control-plane. |

## Envoy startup and warming matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsEnvoyWarming.ActiveClustersHaveCDSAndEDS` | Code or tests treat CDS ACK as enough for a cluster to be usable before EDS arrives. | `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointHasUsableEndpoint`, `TestKgateway/XdsWarming/TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic`, and `TestKgateway/XdsWarming/TestInitialRouteWaitsForEDSBeforeBecomingActive`. | Covered | Add more shapes only if kgateway introduces new startup publication modes. |
| `XdsEnvoyWarming.ActiveClustersHaveReadyCLA` | Code treats an ACKed empty CLA as usable endpoint state. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointHasUsableEndpoint` covers the unit-level empty-CLA gate; `TestKgateway/XdsWarming/TestInitialRouteWaitsForEDSBeforeBecomingActive` covers startup route publication against real Envoy by requiring the delayed-endpoints host to remain unrouted until the backend is usable; `TestKgateway/XdsWarming/TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic` covers hot update delayed endpoints. | Covered | Add a multi-endpoint partial-ready case if endpoint health filtering becomes part of the publication gate. |
| `XdsEnvoyWarming.ActiveRouteReferencesActiveCluster` | RDS route moves to a cluster before the cluster is active. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` and `TestSnapshotPerClientDefersWeightedRouteUntilAllEndpointsReady` cover the control-plane side; `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointHasUsableEndpoint` covers empty EDS; `TestKgateway/XdsWarming/TestInitialRouteWaitsForEDSBeforeBecomingActive` covers startup delayed endpoints; `TestKgateway/XdsWarming/TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic` and `TestKgateway/XdsWarming/TestWeightedRouteWaitsForAllEDSBeforeSplittingTraffic` cover real Envoy traffic while the new EDS has no usable endpoints. | Covered | Add continuous-load variants if transient sub-second failures become a concern. |
| `XdsEnvoyWarming.ActiveListenerHasRouteConfig` | LDS listener becomes active before referenced RDS exists. | `TestCheckSnapshotMissingRDSReferencedByListener` catches emitted snapshot closure, not Envoy warming. | Partial | Add startup e2e that withholds RDS or uses a delayed route input and checks no active listener points at missing RDS. |
| `XdsEnvoyWarming.ActiveListenerAndRouteAgree` | Active listener and active route identity diverge. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` now uses an HCM/RDS listener and `xdscheck` to assert the emitted listener and route config stay closed; actual active Envoy state is not observable in unit tests. | Partial | Add Envoy e2e if this becomes a suspected startup/warming regression. |
| `XdsEnvoyWarming.StartupActiveOnlyAfterClosure` | Startup declares success before LDS/RDS/CDS/EDS closure exists. | Static closure is covered by xdscheck; `TestKgateway/XdsWarming/TestInitialRouteWaitsForEDSBeforeBecomingActive` covers a startup route/service with delayed endpoints and verifies traffic does not move to the host until the backend becomes usable. | Covered | Add a delayed-RDS startup case if listener/RDS warming is suspected. |
| `XdsEnvoyWarming.NoBreakBeforeMake` | Old active cluster is removed before traffic has moved to the new warmed cluster. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` and `TestSnapshotPerClientDefersWeightedRouteUntilAllEndpointsReady` cover the control-plane ordering; `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointHasUsableEndpoint` covers empty EDS; `TestKgateway/XdsWarming/TestRouteUpdateWaitsForNewEDSBeforeBreakingOldTraffic` asserts old traffic remains stable while the new backend has no endpoints, then moves to new; `TestKgateway/XdsWarming/TestWeightedRouteWaitsForAllEDSBeforeSplittingTraffic` asserts old traffic remains stable before weighted traffic splits. | Covered | Add higher-rate continuous-load coverage if this needs to catch very short transient failures. |

## ADS protocol matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsAdsSotw.AckAdvancesOnlyMatchingNonce` | Server records acceptance for a resource type without ACK of that type's latest nonce. | No kgateway-specific test; go-control-plane owns SotW stream mechanics. | External | Add kgateway xDS server stream tests only if kgateway adds custom ACK handling or wraps go-control-plane acceptance state. |
| `XdsAdsSotw.NackDoesNotAdvanceAcceptedVersion` | NACK advances accepted version. | No kgateway-specific test. | External | Same as above. |
| `XdsAdsSotw.StaleNonceDoesNotAdvanceAcceptedVersion` | Stale nonce request mutates accepted state or republishes an old response. | No kgateway-specific test. | External | Same as above; this becomes high value if kgateway starts interpreting nonce/error detail itself. |
| `XdsAdsSotw.VersionsArePerResourceType` | LDS/RDS/CDS/EDS versions bleed across resource types. | `TestSnapshotPerClientEndpointOnlyUpdateOnlyChangesEDSVersion` mutates only EDS and asserts LDS/RDS/CDS versions remain unchanged while EDS changes. | Covered | Add per-type version assertions for SDS/ECDS if those become modeled. |
| `XdsAdsSotw.NoncesArePerStream` | Nonces survive reconnect and poison the new stream. | No kgateway-specific test. | External | Add a fake ADS stream reconnect test only if kgateway-owned code starts persisting stream nonce state. |
| Stable valid desired snapshot eventually publishable | Coherent input exists but no xDS update reaches the client. | Bounded `Eventually` tests in `proxy_syncer/perclient_test.go`, including service-name EDS and multi-client cases; translator fixtures produce closed snapshots. | Covered | Envoy active-state convergence still needs e2e. |

## Translator integration matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| Snapshot closure after IR -> xDS | Translator emits invalid concrete xDS even though per-client publication rules are safe. | `TestTranslatedRedirectSnapshotPassesXDSCheck`, `TestTranslatedBackendSnapshotPassesXDSCheck`, `TestTranslatedOAuth2SnapshotPassesXDSCheck`, `TestTranslatedExtAuthSnapshotPassesXDSCheck`, `TestTranslatedRateLimitSnapshotPassesXDSCheck`, `TestTranslatedExtProcSnapshotPassesXDSCheck`, `TestTranslatedGRPCAccessLogSnapshotPassesXDSCheck`, `TestTranslatedOpenTelemetryAccessLogAndTracingSnapshotPassesXDSCheck`. | Partial | Add xdscheck assertions to more existing translator fixtures, especially new policies that emit filters with cluster or secret references. |
| Dynamic/unverifiable constructs remain visible | Validator silently treats dynamic xDS as fully checked. | `cluster_header` warning tests and unknown typed-config warning tests. | Covered | Keep warning assertions in translator tests if a fixture intentionally uses a dynamic construct. |

## Remaining highest-value gaps

1. Add mixed cluster-name and service-name EDS named-watch coverage if production can emit both forms in the same per-client snapshot.
2. Add SDS/ECDS per-type version checks when those resource types are added to the TLA+ publication models.
3. Add delayed-RDS startup coverage if listener warming becomes a suspected regression source.

## Commands

Run the current covered Go checks:

```bash
go test ./pkg/kgateway/translator/xdscheck
go test ./pkg/kgateway/translator/gateway -run '^(TestTranslatedRedirectSnapshotPassesXDSCheck|TestTranslatedBackendSnapshotPassesXDSCheck|TestTranslatedOAuth2SnapshotPassesXDSCheck|TestTranslatedExtAuthSnapshotPassesXDSCheck|TestTranslatedRateLimitSnapshotPassesXDSCheck|TestTranslatedExtProcSnapshotPassesXDSCheck|TestTranslatedGRPCAccessLogSnapshotPassesXDSCheck|TestTranslatedOpenTelemetryAccessLogAndTracingSnapshotPassesXDSCheck)$'
go test ./pkg/kgateway/proxy_syncer
go test -tags e2e ./test/e2e/features/xds_warming
go test -tags e2e ./test/e2e/tests -run '^$'
go test ./test/e2e/tests -run TestAllE2ETestsInShards
```

Run the model side:

```bash
devel/formal/tla/check-docker.sh
```
