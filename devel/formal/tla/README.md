# XdsAdsSotw TLA+ model

## Purpose

`XdsAdsSotw.tla` is an abstract finite model of ADS/SotW-style xDS publication. It is intentionally small: one listener, one route configuration, one cluster, and one endpoint resource. The goal is to make the protocol and dependency invariants executable under TLC, not to model all Envoy fields.

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
java -jar /path/to/tla2tools.jar -config XdsEdsSubsetBug.cfg XdsEdsSubset.tla
```

## Interpreting counterexamples

If TLC reports an invariant violation, read the state trace from the initial state to the failing state. Each action corresponds to one abstract event such as `InputChange`, `SendResponse("EDS")`, `ClientAck("CDS")`, `ClientNack("LDS")`, `StaleClientRequest("RDS")`, or `Reconnect`.

Useful examples:

- A `SentSnapshotsAreDependencyClosed` failure means a type was published before its dependencies were present in the sent snapshot.
- An `AckAdvancesOnlyMatchingNonce` failure means acceptance advanced without the current stream's latest nonce for that type.
- A `NoncesArePerStream` failure means nonce state survived a reconnect incorrectly.
- An `XdsEdsSubset` `NoOrphanEndpointResources` failure means the EDS snapshot contains a `ClusterLoadAssignment` that no current EDS cluster can cause Envoy to request.

## Counterexample drills

The repository should keep `XdsAdsSotw.tla` in the safe form checked by `XdsAdsSotw.cfg`. To demonstrate that TLC can find useful failures, make one temporary local edit and rerun TLC:

- Remove `SnapshotClosed(ProposedSentSnapshot(t))` from `SendResponse(t)`. TLC should be able to violate `SentSnapshotsAreDependencyClosed`.
- Change `ClientNack(t)` so that `serverAcceptedVersion'` is updated to `sentVersion[t]`. TLC should be able to violate `NackDoesNotAdvanceAcceptedVersion`.
- Change `Reconnect` so that `sentNonce' = sentNonce`. TLC should be able to violate `NoncesArePerStream`.

Revert the temporary edit after reading the trace. These drills are intentionally not checked in as separate broken models because the MVP keeps the reviewable source tree focused on the correct abstract protocol.

## Liveness

The MVP keeps the TLC configuration safety-focused. The model notes the intended liveness direction: under stable valid desired state and a fair client that ACKs valid responses, every resource type should be able to reach the desired version. A future PR can add fairness constraints and liveness checking if the state space stays practical.
