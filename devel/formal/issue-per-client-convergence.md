# Per-client xDS convergence model

## Purpose

`XdsPerClientConvergence.tla` is a focused finite model for the highest-value behavior shared by issue 13868, issue 14184, and Envoy startup/warming concerns. It does not model kgateway's full translator, the Kubernetes watch graph, or Envoy internals. It models one old cluster and one new cluster so TLC can exhaustively check the ordering obligations that matter for per-client xDS convergence.

The model asks one practical question: after kgateway has a last-good per-client xDS cache snapshot, can it survive a partial/deferred input, publish a later coherent input, answer Envoy's named EDS request, and move Envoy active state only after CDS/EDS closure exists?

## Assumptions

- There is one Envoy client and one per-client xDS cache.
- The old snapshot is coherent and active.
- The desired input changes from `old` to `new`.
- A partial input may temporarily have route/CDS intent for `new` without endpoint readiness for `new`.
- The partial input appears as an abstract KRT delete/defer event.
- The safe behavior retains the last-good cache while the partial input is unresolved.
- Once input is coherent, the model assumes the new CDS and EDS resources can be computed together.
- Envoy asks for EDS by the named clusters it has learned from CDS.
- Envoy's active state changes only after the EDS response for the new cluster has arrived.

## Safe path

The passing configuration checks this abstract path:

1. `StableOld`: old LDS/RDS/CDS/EDS state is cached and active.
2. `DeferredPartial`: a partial update for the new cluster arrives; the cache retains old last-good state.
3. `CoherentInput`: endpoint readiness catches up and the candidate snapshot is dependency-closed.
4. `PublishedNew`: the coherent new snapshot replaces the cache and records a new EDS version.
5. `WarmingNew`: Envoy learns the new CDS names and opens a named EDS watch.
6. `EdsResponded`: the cache can answer that named EDS watch.
7. `ActiveNew`: Envoy active state moves to the new closed route/cluster/endpoint state.

## Checked invariants

- `DeleteRetainsLastGood`: a KRT delete/defer event cannot clear the last-good per-client xDS cache.
- `PartialDoesNotOverwriteCache`: a partial computed snapshot cannot overwrite the coherent cache.
- `CacheSnapshotClosed`: cached references, CDS, and EDS remain dependency-closed.
- `AlignedEDSRequestRespondable`: once Envoy's known CDS names match the cache, the cache EDS set can answer Envoy's named EDS request.
- `EDSResourceSetChangeChangesVersion`: any EDS resource-set change also changes the EDS version.
- `ActiveSnapshotClosed`: Envoy active route/cluster/endpoint state remains dependency-closed.
- `CoherentInputCanPublish`: a coherent dependency-closed candidate snapshot cannot get stuck because publication is disabled.
- `CoherentNewEventuallyActive`: under weak fairness for publish, learn-CDS, EDS response, and activation actions, coherent input eventually becomes active.

## Counterexample configurations

The bug configurations are intentionally failing TLC configs. They are useful because each one isolates a class of regression:

- `XdsPerClientConvergenceClearOnDeleteBug.cfg`: clears the per-client cache after a delete/defer event and violates `DeleteRetainsLastGood`.
- `XdsPerClientConvergencePartialOverwriteBug.cfg`: publishes a partial candidate into the cache and violates `PartialDoesNotOverwriteCache`.
- `XdsPerClientConvergenceStaleEdsBug.cfg`: publishes stale extra EDS resources after CDS has changed and violates `CacheSnapshotClosed`.
- `XdsPerClientConvergenceVersionReuseBug.cfg`: changes EDS resources while reusing Envoy's accepted EDS version and violates `EDSResourceSetChangeChangesVersion`.
- `XdsPerClientConvergenceActivateBeforeEdsBug.cfg`: moves active state before Envoy has received the named EDS response and violates `ActiveSnapshotClosed`.
- `XdsPerClientConvergenceNoPublishBug.cfg`: accepts a coherent input but never publishes, so TLC reports a liveness violation for `CoherentNewEventuallyActive`.

## How to run

Run the passing model through the normal Docker runner:

```bash
devel/formal/tla/check-docker.sh
```

Run the passing model directly when `tla2tools.jar` is available:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergence.cfg XdsPerClientConvergence.tla
```

Run a counterexample directly:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar -config XdsPerClientConvergenceStaleEdsBug.cfg XdsPerClientConvergence.tla
```

## Design consequence

The model supports a concrete design rule for kgateway per-client xDS publication: do not let partial per-client recomputation mutate the served cache. Treat partial/deferred inputs as non-publication events, retain the last coherent cache, publish only coherent dependency-closed snapshots, filter EDS to currently advertised EDS clusters, bump the EDS version when that resource set changes, and rely on Envoy warming so active state changes only after the named EDS response is available.
