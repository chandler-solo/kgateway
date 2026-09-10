# Envoy startup and warming model

## Question

Configuration publication, cluster initialization, and successful backend traffic are different states. ACK alone proves none of the latter two. An empty `ClusterLoadAssignment` can initialize a cluster with zero hosts. A valid Service with no endpoints must not prevent first publication for every route on a gateway.

The research policy therefore distinguishes first cache publication (C0) from a warm route transition (C3). C0 requires referenced CDS to be complete, with CLAs present for EDS clusters, but permits empty assignments. C3 retains the existing usable-endpoint guard when a previously published snapshot exists. A controller cache restart also enters C0 even when the reconnecting proxy retains old configuration.

Primary source: [xDS protocol](https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol). Direct characterization of the pinned Envoy remains an open assumption; this model does not prove its internals or guarantee successful backend traffic.

## Model

`devel/formal/tla/XdsEnvoyWarming.tla` models cold initialization and a warm transition from `old` to `new`. `activeClusters` means initialized clusters. `claState` distinguishes missing, empty, and ready assignments; only ready assignments represent usable endpoints. This abstraction omits timeouts, partial NACKs, and worker application.

Cold initialization can reach an active listener with an empty CLA. The warm transition can initialize the new cluster with an empty CLA but keeps routes on the old cluster until ready EDS arrives. This is a control-plane policy guard, not an Envoy-provided route warming mechanism.

## Checked properties

- `ActiveClustersHaveCDSAndEDS`: initialization requires both CDS and EDS in this abstraction.
- `ActiveClustersHaveCLA`: initialized clusters cannot have missing CLAs; empty is allowed.
- `WarmRouteFlipHasReadyCLA`: the warm route flip requires ready endpoints.
- `ActiveRouteReferencesActiveCluster`: active routes reference initialized clusters.
- `ActiveListenerHasRouteConfig` and `ActiveListenerAndRouteAgree`: listener and RDS closure.
- `StartupActiveOnlyAfterClosure`: cold activation requires CDS/EDS/RDS/LDS closure, not usable endpoints.
- `NoBreakBeforeMake`: remove the old cluster only after its route moves.

`XdsEnvoyWarming.cfg` checks both cold and warm safety. `XdsEnvoyWarmingColdEmpty.cfg` also checks eventual cold activation with permanently empty endpoints, under weak fairness of the five cold initialization actions. There is no ready-endpoint action in that system, so the progress result cannot depend on endpoint recovery. This is not a real-time startup bound.

The Lean `PerClusterReadiness.lean` cold systems independently model first cache installation, cache restart, and an unrelated route update with permanently empty endpoints. The safe system preserves closure and has a path to publication. The old usable-endpoint gate is stuck; bypassing missing CDS violates closure. Lean's finite checker establishes recoverability, not all-fair-execution liveness.

## Counterexamples

| Configuration | Expected violation |
|---|---|
| `XdsEnvoyWarmingAckImpliesActiveBug.cfg` | `ActiveClustersHaveCDSAndEDS`: CDS ACK alone initializes a cluster |
| `XdsEnvoyWarmingEmptyCLAFlipBug.cfg` | `WarmRouteFlipHasReadyCLA`: a warm route flips to a cluster initialized with empty EDS |
| `XdsEnvoyWarmingRouteBeforeClusterBug.cfg` | `ActiveRouteReferencesActiveCluster`: route flips before EDS initialization |
| `XdsEnvoyWarmingListenerBeforeRouteBug.cfg` | `ActiveListenerHasRouteConfig`: listener activates before RDS |

The former empty-CLA-implies-active counterexample was incorrectly classifying valid empty-cluster initialization as a bug. Its replacement tests the retained warm C3 policy.

## Implementation evidence and limitations

`TestSnapshotPerClientFirstPublishWithEmptyEndpoints` exercises synthesized and explicit empties, service names, missing-CDS deferral, cache restart, later route updates, and endpoint recovery. The existing setup fixtures remain unchanged. The `xds_starvation` suite pins gateway availability and controller-restart progress; its ExternalName fixture is not a universal reproducer of missing-CDS starvation.

The `xds_warming` initial-route test adds a host to an already running gateway, so it exercises C3 rather than C0. Existing warm tests remain applicable. The whole-type RDS/LDS/SDS hold for a permanently empty newly referenced backend remains a known limitation. First-publication deferral for permanently missing nonexempt CDS also remains possible.

## How to run

Both formal TLC runners include the safe warming and permanent-empty cold configurations. For an individual configuration:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsEnvoyWarmingColdEmpty.cfg XdsEnvoyWarming.tla
```

Use a counterexample configuration from the table to reproduce its expected invariant failure. `devel/formal/lean/check.sh` rebuilds the Lean models and checks emitted publication traces, including `publish-first`. That event permits empty CLAs but still checks CDS closure, CLA presence, and orphan assignments. It does not assert successful wire delivery or activation.
