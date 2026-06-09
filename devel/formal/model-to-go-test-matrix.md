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
- `test/e2e`: real Envoy startup, warming, reconnect, and active dataplane behavior that cannot be proven from emitted snapshots alone.

## Snapshot closure matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsAdsSotw.SentSnapshotsAreDependencyClosed` | Publish LDS/RDS/CDS/EDS with missing dependencies. | `TestCheckSnapshotMissingRDSReferencedByListener`, `TestCheckSnapshotMissingCDSClusterReferencedByRoute`, `TestCheckSnapshotMissingEDSAssignmentReferencedByEDSCluster`; translator fixtures `TestTranslated*SnapshotPassesXDSCheck`. | Covered | Keep adding translator fixtures for new policy/filter features that emit cluster, endpoint, or secret references. |
| `XdsPerClientPublication.CacheSnapshotCoherent` | Per-client cache contains missing route clusters, missing EDS resources, or orphan EDS resources. | `assertNoXDSCheckErrors` in the issue-focused `proxy_syncer` tests; xdscheck missing/orphan tests. | Covered | Convert more per-client tests through `assertNoXDSCheckErrors` when they produce full snapshots. |
| `XdsPerClientConvergence.CacheSnapshotClosed` | Cached refs, CDS, and EDS are not closed after an update. | `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`, xdscheck closure tests. | Covered | Add multi-client variants to ensure closure is per-client, not accidentally global. |
| `XdsEdsSubset.NoOrphanEndpointResources` | EDS publishes a `ClusterLoadAssignment` for a cluster no emitted EDS cluster can request. | `TestCheckSnapshotOrphanClusterLoadAssignment`, `TestCheckSnapshotStaticClusterLoadAssignmentIsOrphan`, `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`. | Covered | Keep the orphan CLA finding at error severity for ADS named EDS safety. |
| `XdsEdsSubset.EDSRequestRespondable` and `ResponseMatchesADSGuard` | Snapshot contains resources outside Envoy's named EDS request, so ADS cannot answer. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` uses go-control-plane `SnapshotCache.CreateWatch`. | Covered | Add a service-name EDS watch test for `EdsClusterConfig.ServiceName`, not only cluster-name EDS. |
| `XdsNamedEdsWatch.ResponseOnlyForRequestedNames` | go-control-plane response contains EDS resources Envoy did not request. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` checks returned resources exclude `cluster-b`. | Covered | Add a direct xDS server stream test only if kgateway adds custom watch filtering. |
| Resource name uniqueness | Duplicate LDS/RDS/CDS/EDS/SDS names create ambiguous snapshots. | `TestCheckSnapshotDuplicateResourceNames`. | Covered | Add translator fixture coverage if a new naming scheme is introduced. |
| Dynamic route cluster references | `cluster_header` cannot be statically validated as a CDS dependency. | `TestCheckSnapshotClusterHeaderRouteIsWarningOnly`. | Covered | Keep this as warning severity unless a policy forbids `cluster_header`. |
| Unknown typed configs | Validator panics or silently claims safety when it cannot unpack a config. | `TestCheckSnapshotUnknownHCMTypedConfigDoesNotPanic` and other unknown typed-config tests. | Covered | Add warning-only tests for each new extension family the checker partially recognizes. |

## Issue 13868 and per-client publication matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsReconnectRace13868.ServedSnapshotReferencesResolved` | Reconnect publishes a partial per-client snapshot whose routes/listeners reference a missing CDS cluster. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`. | Covered | Add a two-client variant where only one client's cluster set is partial. |
| `XdsReconnectRace13868.PublishedSnapshotIsCurrentWhenPublished` | After all referenced clusters are ready or errored, the published snapshot does not match the current computed state. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`, `TestSnapshotPerClientStillPublishesWhenReferencedClusterErrored`. | Partial | Add explicit assertions that published cluster names and `erroredClusters` exactly equal the current per-client inputs. |
| `XdsPerClientPublication.CacheReferencesResolved` | Cache route refs include a cluster missing from CDS and not explicitly errored. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`, `TestSnapshotPerClientStillPublishesWhenReferencedClusterErrored`; xdscheck missing-cluster tests. | Covered | Keep blackhole cluster behavior separate from missing referenced clusters. |
| `XdsPerClientPublication.CacheReferencedEndpointsPresent` | Referenced EDS cluster is present in CDS but its CLA is missing. | `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady`. | Covered | Add service-name based endpoint readiness coverage. |
| `XdsPerClientPublication.CacheHasNoOrphanEndpointResources` | Stale CLA remains after CDS shrinks. | `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`. | Covered | Add a static-cluster stale CLA per-client test if static clusters can appear in per-client snapshots. |
| `XdsPerClientPublication.CoherentInputsCanPublish` | Inputs become coherent but the KRT collection never republishes. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady`, `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints`, `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` use bounded `Eventually` after the missing input appears. | Partial | Add a targeted "coherent after delete/defer" test that first observes no partial output, then observes exactly one coherent output. |
| `XdsPerClientConvergence.DeleteRetainsLastGood` | A KRT delete/defer event clears the served last-good xDS cache. | `TestSnapshotPerClientKeepsPublishingWhenMisconfiguredBackendRefArrivesAtRuntime` covers one runtime misconfiguration that must not withdraw output. | Partial | Add a direct per-client cache retention test: start with a coherent snapshot, introduce an incoherent partial update, and assert the served `SnapshotCache` still returns the old version. |
| `XdsPerClientConvergence.PartialDoesNotOverwriteCache` | Partial computed state overwrites a coherent per-client cache. | `TestSnapshotPerClientDefersUntilAllReferencedClustersAreReady` and `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` assert no partial KRT output. | Partial | Assert the actual served cache remains old while the derived KRT collection withholds new output. |
| `XdsPerClientConvergence.CoherentInputCanPublish` | Publication remains disabled even after CDS and EDS become closed. | Same bounded `Eventually` tests as above. | Covered | Include the old and new resource versions in the assertions when practical. |
| `XdsPerClientConvergence.CoherentNewEventuallyActive` | A coherent desired snapshot never reaches active use. | Unit tests can only observe publication; real active dataplane requires Envoy. | Partial | Add an e2e make-before-break test that proves traffic stays on old until new EDS is ready, then succeeds on new without transient `NC`/`500`. |

## Issue 14184 and named EDS matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsNamedEdsWatch.ChangedSnapshotRequestRespondable` | EDS snapshot is version-new but contains stale extra resources outside Envoy's named request. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`; xdscheck orphan CLA tests. | Covered | Add a direct test for `service_name` EDS names and a mixed cluster-name/service-name snapshot. |
| `XdsNamedEdsWatch.ResourceSetChangeRequiresVersionChange` | Filtered EDS resource set changes but EDS version is reused. | `TestFilterEndpointResourcesForClusters_FiltersStaticClusterCLAs`, `TestFilterEndpointResourcesForClusters_FiltersStaleClusterLoadAssignments`, `TestSnapshotPerClientFiltersStaleEndpointResourcesWhenClusterRemoved`, `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved`. | Covered | Assert version determinism for equivalent filtered resource sets if hash churn becomes a concern. |
| `XdsNamedEdsWatch.ChangedRespondableSnapshotCanSend` | A version-new, request-compatible EDS snapshot opens a watch instead of responding. | `TestSnapshotPerClientFilteredEdsSnapshotRespondsToNamedADSRequestAfterClusterRemoved` waits for an immediate go-control-plane response. | Covered | Add a regression test around newly subscribed resources with unchanged version if kgateway depends on that path. |
| `XdsNamedEdsWatch.NoSuppressedChangedResponse` | ADS suppresses a changed EDS response because the snapshot is not name-compatible. | Same named ADS watch test plus orphan CLA validator. | Covered | Keep the production fix at the cache input boundary by filtering EDS, not by changing go-control-plane. |

## Envoy startup and warming matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsEnvoyWarming.ActiveClustersHaveCDSAndEDS` | Code or tests treat CDS ACK as enough for a cluster to be usable before EDS arrives. | `TestSnapshotPerClientDefersUntilReferencedEDSClustersHaveEndpoints` and `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` prevent publication of routes to EDS clusters without endpoints. | Partial | Add Envoy e2e coverage that observes traffic behavior while EDS is withheld. |
| `XdsEnvoyWarming.ActiveRouteReferencesActiveCluster` | RDS route moves to a cluster before the cluster is active. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` models the control-plane side of this ordering. | Partial | Add e2e make-before-break traffic assertions; unit tests cannot observe Envoy active state. |
| `XdsEnvoyWarming.ActiveListenerHasRouteConfig` | LDS listener becomes active before referenced RDS exists. | `TestCheckSnapshotMissingRDSReferencedByListener` catches emitted snapshot closure, not Envoy warming. | Partial | Add startup e2e that withholds RDS or uses a delayed route input and checks no active listener points at missing RDS. |
| `XdsEnvoyWarming.ActiveListenerAndRouteAgree` | Active listener and active route identity diverge. | No direct Go test. | Gap | Add Envoy e2e or a dedicated Envoy-state simulator test if this becomes a kgateway-owned sequencing decision. |
| `XdsEnvoyWarming.StartupActiveOnlyAfterClosure` | Startup declares success before LDS/RDS/CDS/EDS closure exists. | Static closure is covered by xdscheck; active startup is not. | Partial | Add e2e startup readiness coverage tied to actual traffic success, not only ACK logs. |
| `XdsEnvoyWarming.NoBreakBeforeMake` | Old active cluster is removed before traffic has moved to the new warmed cluster. | `TestSnapshotPerClientDefersMakeBeforeBreakRouteUntilNewEndpointReady` covers the control-plane ordering and removal after new closure. | Partial | Add e2e traffic test that continuously sends requests during old -> new transition. |

## ADS protocol matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| `XdsAdsSotw.AckAdvancesOnlyMatchingNonce` | Server records acceptance for a resource type without ACK of that type's latest nonce. | No kgateway-specific test; go-control-plane owns SotW stream mechanics. | External | Add kgateway xDS server stream tests only if kgateway adds custom ACK handling or wraps go-control-plane acceptance state. |
| `XdsAdsSotw.NackDoesNotAdvanceAcceptedVersion` | NACK advances accepted version. | No kgateway-specific test. | External | Same as above. |
| `XdsAdsSotw.StaleNonceDoesNotAdvanceAcceptedVersion` | Stale nonce request mutates accepted state or republishes an old response. | No kgateway-specific test. | External | Same as above; this becomes high value if kgateway starts interpreting nonce/error detail itself. |
| `XdsAdsSotw.VersionsArePerResourceType` | LDS/RDS/CDS/EDS versions bleed across resource types. | EDS version changes are tested around endpoint filtering, but full per-type ADS version behavior is not. | Partial | Add a SnapshotCache resource-version test that mutates only EDS and asserts LDS/RDS/CDS versions remain unchanged. |
| `XdsAdsSotw.NoncesArePerStream` | Nonces survive reconnect and poison the new stream. | No kgateway-specific test. | External | Add a fake ADS stream reconnect test only if kgateway-owned code starts persisting stream nonce state. |
| Stable valid desired snapshot eventually publishable | Coherent input exists but no xDS update reaches the client. | Bounded `Eventually` tests in `proxy_syncer/perclient_test.go`; translator fixtures produce closed snapshots. | Partial | Add progress tests for multi-client and service-name EDS cases. |

## Translator integration matrix

| Model obligation | Go bug shape | Existing Go coverage | Status | Next action |
| --- | --- | --- | --- | --- |
| Snapshot closure after IR -> xDS | Translator emits invalid concrete xDS even though per-client publication rules are safe. | `TestTranslatedRedirectSnapshotPassesXDSCheck`, `TestTranslatedBackendSnapshotPassesXDSCheck`, `TestTranslatedOAuth2SnapshotPassesXDSCheck`, `TestTranslatedExtAuthSnapshotPassesXDSCheck`, `TestTranslatedRateLimitSnapshotPassesXDSCheck`, `TestTranslatedExtProcSnapshotPassesXDSCheck`, `TestTranslatedGRPCAccessLogSnapshotPassesXDSCheck`, `TestTranslatedOpenTelemetryAccessLogAndTracingSnapshotPassesXDSCheck`. | Partial | Add xdscheck assertions to more existing translator fixtures, especially new policies that emit filters with cluster or secret references. |
| Dynamic/unverifiable constructs remain visible | Validator silently treats dynamic xDS as fully checked. | `cluster_header` warning tests and unknown typed-config warning tests. | Covered | Keep warning assertions in translator tests if a fixture intentionally uses a dynamic construct. |

## Highest-value gaps

1. Add a direct served-cache retention test for `DeleteRetainsLastGood` and `PartialDoesNotOverwriteCache`: start with a coherent per-client snapshot, introduce a partial/deferred update, and assert the served `SnapshotCache` still returns the old coherent resources and old versions.
2. Add service-name EDS variants for endpoint filtering, version change, and named ADS response behavior.
3. Add a real Envoy e2e make-before-break test for warming: traffic should not see transient `NC` or `500` while the new EDS resource is absent, and should move after EDS appears.
4. Add a small SnapshotCache per-type version test: mutate only filtered EDS and assert LDS/RDS/CDS versions do not change while EDS does.
5. Add multi-client per-client publication tests so one client's partial state cannot clear or poison another client's coherent cache.

## Commands

Run the current covered Go checks:

```bash
go test ./pkg/kgateway/translator/xdscheck
go test ./pkg/kgateway/translator/gateway -run '^(TestTranslatedRedirectSnapshotPassesXDSCheck|TestTranslatedBackendSnapshotPassesXDSCheck|TestTranslatedOAuth2SnapshotPassesXDSCheck|TestTranslatedExtAuthSnapshotPassesXDSCheck|TestTranslatedRateLimitSnapshotPassesXDSCheck|TestTranslatedExtProcSnapshotPassesXDSCheck|TestTranslatedGRPCAccessLogSnapshotPassesXDSCheck|TestTranslatedOpenTelemetryAccessLogAndTracingSnapshotPassesXDSCheck)$'
go test ./pkg/kgateway/proxy_syncer
```

Run the model side:

```bash
devel/formal/tla/check-docker.sh
```
