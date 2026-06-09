# XdsAdsSotw TLA+ model

## Purpose

`XdsAdsSotw.tla` is an abstract finite model of ADS/SotW-style xDS publication. It is intentionally small: one listener, one route configuration, one cluster, and one endpoint resource. The goal is to make the protocol and dependency invariants executable under TLC, not to model all Envoy fields.

`XdsReconnectRace13868.tla` is a focused model for PR 13868's reconnect-time cluster readiness gate. It captures a retained last-good snapshot, partial per-client cluster translation after reconnect, and the decision to publish or defer. `XdsReconnectRace13868.cfg` checks the safe behavior. `XdsReconnectRace13868Bug.cfg` intentionally checks the old partial-publish behavior and should fail.

`XdsPerClientPublication.tla` combines the 13868 and 14184 failure shapes in one phase-based model. It captures retained last-good snapshots, reconnect partial cluster readiness, explicitly errored clusters, stale EDS resources after CDS shrinks, named EDS response compatibility, and a minimal Envoy warming/active distinction. `XdsPerClientPublication.cfg` checks the safe behavior. The two buggy configs intentionally fail with the missing-cluster and stale-EDS counterexamples.

`XdsPerClientConvergence.tla` connects issue 13868, issue 14184, and startup/warming into one convergence path. It starts from a last-good active old snapshot, models a partial/deferred input, requires cache retention during the defer window, publishes a later coherent input with a changed EDS version, answers Envoy's named EDS request, and activates the new state only after CDS/EDS closure exists. `XdsPerClientConvergence.cfg` checks the safe behavior plus a small liveness property. The buggy configs intentionally fail for cache clearing, partial overwrite, stale EDS, EDS version reuse, activate-before-EDS, and no-publish regressions.

`XdsEnvoyWarming.tla` is a focused startup and make-before-break model for Envoy active state. It separates ACKed CDS/EDS/RDS/LDS resources from active clusters, active routes, and active listeners. `XdsEnvoyWarming.cfg` checks the safe behavior. The buggy configs intentionally fail when CDS ACK is treated as cluster-active before EDS, RDS is activated before its referenced cluster is active, or LDS is activated before RDS exists.

`XdsNamedEdsWatch.tla` focuses on go-control-plane ADS/SotW named EDS watch respondability. It separates the cache EDS resource set, Envoy's named request, the cache EDS version, Envoy's last accepted EDS version, and the cache response state. `XdsNamedEdsWatch.cfg` checks the safe behavior. The buggy configs intentionally fail when stale extra EDS resources suppress a named ADS response or when the EDS resource set changes without an EDS version change.

`XdsEdsSubset.tla` is an issue-focused finite model for ADS named EDS responses. It captures a smaller property: the EDS snapshot must not contain endpoint resources that are no longer named by emitted EDS clusters. `XdsEdsSubset.cfg` checks the safe behavior. `XdsEdsSubsetBug.cfg` intentionally checks a buggy transition that keeps stale EDS resources after CDS shrinks, so TLC can show the counterexample without editing the model.

## Correspondence to xDS behavior

The model represents:

- Resource types LDS, RDS, CDS, and EDS as separate logical streams within ADS.
- Resource-type versions that persist across reconnects.
- Response nonces that are scoped to the current stream and tracked per resource type.
- ACK of the latest response nonce advancing the server-observed accepted version for that type.
- NACK leaving the accepted version unchanged.
- Stale nonce requests leaving accepted versions and sent snapshots unchanged.
- Reconnect resetting nonce context while preserving resource-level versions.
- SotW publication sequencing that keeps sent LDS/RDS/CDS/EDS state dependency-closed.
- Reconnect-time per-client snapshot publication deferral until referenced clusters are present or explicitly errored.
- Per-client cache retention, EDS filtering, named EDS response compatibility, and a minimal Envoy active/warming split for the combined 13868/14184 startup and reconnect traces.
- Per-client convergence from last-good cache retention through partial input deferral, coherent publication, named EDS response, EDS version change, and active-state closure.
- Envoy startup and make-before-break ordering: CDS ACK is distinct from cluster active, EDS enables cluster activation, RDS must follow active clusters for routes, and LDS must follow RDS for listener activation.
- go-control-plane named EDS watch behavior: version-new snapshots answer only when all EDS snapshot resources are named by Envoy's request, and EDS resource set changes require EDS version changes.

The dependency chain is:

```text
listener -> route -> cluster -> endpoint
```

## Out of scope

- Delta xDS.
- Multiple Envoy clients.
- Full subscription set semantics.
- Envoy warming and delayed full application of config.
- Envoy proto validation annotations.
- Extension typed configs.
- Kubernetes watch or controller behavior.

## How to run TLC

Install Java 11 or newer and obtain `tla2tools.jar` from the TLA+ tools releases. The script will look in:

- `devel/formal/tla/tla2tools.jar`
- `tools/tla2tools.jar`
- `$TLA2TOOLS_JAR`

Run both passing models:

```bash
devel/formal/tla/check.sh
```

Or:

```bash
TLA2TOOLS_JAR=/path/to/tla2tools.jar devel/formal/tla/check.sh
```

To run TLC without installing Java or `tla2tools.jar` on the host, use Docker:

```bash
devel/formal/tla/check-docker.sh
```

The Docker runner downloads `tla2tools.jar` to `/tmp/kgateway-tla2tools` by default and mounts it into an `eclipse-temurin` Java image. It does not vendor the jar into the repository. Set `TLA2TOOLS_CACHE_DIR=/path/to/cache` to choose another cache, `TLA2TOOLS_JAR=/path/to/tla2tools.jar` to reuse a local jar, or `TLC_WORKERS=<n>` to pass an explicit TLC worker count.

To reproduce the issue-focused counterexample directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsReconnectRace13868Bug.cfg XdsReconnectRace13868.tla
```

To reproduce the combined per-client publication counterexamples directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsPerClientPublicationMissingClusterBug.cfg XdsPerClientPublication.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientPublicationStaleEdsBug.cfg XdsPerClientPublication.tla
```

To reproduce the per-client convergence counterexamples directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceClearOnDeleteBug.cfg XdsPerClientConvergence.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergencePartialOverwriteBug.cfg XdsPerClientConvergence.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceStaleEdsBug.cfg XdsPerClientConvergence.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceVersionReuseBug.cfg XdsPerClientConvergence.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceActivateBeforeEdsBug.cfg XdsPerClientConvergence.tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceNoPublishBug.cfg XdsPerClientConvergence.tla
```

To reproduce the Envoy warming counterexamples directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsEnvoyWarmingAckImpliesActiveBug.cfg XdsEnvoyWarming.tla
java -jar /path/to/tla2tools.jar -config XdsEnvoyWarmingRouteBeforeClusterBug.cfg XdsEnvoyWarming.tla
java -jar /path/to/tla2tools.jar -config XdsEnvoyWarmingListenerBeforeRouteBug.cfg XdsEnvoyWarming.tla
```

To reproduce the named EDS watch counterexamples directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsNamedEdsWatchStaleExtraBug.cfg XdsNamedEdsWatch.tla
java -jar /path/to/tla2tools.jar -config XdsNamedEdsWatchVersionReuseBug.cfg XdsNamedEdsWatch.tla
```

To reproduce the issue-14184 EDS subset counterexample directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsEdsSubsetBug.cfg XdsEdsSubset.tla
```

## Interpreting counterexamples

If TLC reports an invariant violation, read the state trace from the initial state to the failing state. Each action corresponds to one abstract event such as `InputChange`, `SendResponse("EDS")`, `ClientAck("CDS")`, `ClientNack("LDS")`, `StaleClientRequest("RDS")`, or `Reconnect`.

Useful examples:

- A `SentSnapshotsAreDependencyClosed` failure means a type was published before its dependencies were present in the sent snapshot.
- An `AckAdvancesOnlyMatchingNonce` failure means acceptance advanced without the current stream's latest nonce for that type.
- A `NoncesArePerStream` failure means nonce state survived a reconnect incorrectly.
- An `XdsReconnectRace13868` `ServedSnapshotReferencesResolved` failure means the xDS cache was overwritten by a new per-client snapshot whose route/listener references include a cluster that is neither in CDS nor explicitly errored.
- An `XdsPerClientPublication` `CacheSnapshotCoherent` failure means the per-client xDS cache was overwritten with a snapshot that has a missing referenced cluster, a missing referenced EDS resource, or an orphan EDS resource.
- An `XdsPerClientPublication` `AlignedEDSRequestRespondable` failure means Envoy has learned the same CDS names as the cache, but the cache EDS set cannot satisfy the named EDS request.
- An `XdsPerClientConvergence` `DeleteRetainsLastGood` failure means a KRT delete/defer event cleared or changed the last-good per-client xDS cache.
- An `XdsPerClientConvergence` `PartialDoesNotOverwriteCache` failure means a partial computed snapshot overwrote the coherent cache.
- An `XdsPerClientConvergence` `CacheSnapshotClosed` failure means the cache has missing route/cluster/endpoint closure or stale extra EDS resources.
- An `XdsPerClientConvergence` `EDSResourceSetChangeChangesVersion` failure means the EDS resource set changed without an EDS version change.
- An `XdsPerClientConvergence` `ActiveSnapshotClosed` failure means Envoy active state moved to a route/cluster snapshot before EDS closure existed.
- An `XdsPerClientConvergence` temporal-property failure means coherent input can remain stuck without publication, named EDS response, or activation.
- An `XdsEnvoyWarming` `ActiveClustersHaveCDSAndEDS` failure means a cluster was considered active before both CDS and EDS were present.
- An `XdsEnvoyWarming` `ActiveRouteReferencesActiveCluster` failure means an active route points at a cluster that is not active.
- An `XdsEnvoyWarming` `ActiveListenerHasRouteConfig` failure means an active listener points at an RDS route config that is not present.
- An `XdsNamedEdsWatch` `ChangedSnapshotRequestRespondable` failure means a version-new EDS snapshot contains a resource outside Envoy's named EDS request, so go-control-plane ADS can suppress the response.
- An `XdsNamedEdsWatch` `ResourceSetChangeRequiresVersionChange` failure means the cache changed the EDS resource-name set without changing the EDS version.
- An `XdsEdsSubset` `NoOrphanEndpointResources` failure means the EDS snapshot contains a `ClusterLoadAssignment` that no current EDS cluster can cause Envoy to request.

## Counterexample drills

The repository should keep `XdsAdsSotw.tla` in the safe form checked by `XdsAdsSotw.cfg`. To demonstrate that TLC can find useful failures, make one temporary local edit and rerun TLC:

- Remove `SnapshotClosed(ProposedSentSnapshot(t))` from `SendResponse(t)`. TLC should be able to violate `SentSnapshotsAreDependencyClosed`.
- Change `ClientNack(t)` so that `serverAcceptedVersion'` is updated to `sentVersion[t]`. TLC should be able to violate `NackDoesNotAdvanceAcceptedVersion`.
- Change `Reconnect` so that `sentNonce' = sentNonce`. TLC should be able to violate `NoncesArePerStream`.

Revert the temporary edit after reading the trace. These drills are intentionally not checked in as separate broken models because the MVP keeps the reviewable source tree focused on the correct abstract protocol.

## Liveness

The MVP keeps the TLC configuration safety-focused. The model notes the intended liveness direction: under stable valid desired state and a fair client that ACKs valid responses, every resource type should be able to reach the desired version. A future PR can add fairness constraints and liveness checking if the state space stays practical.
