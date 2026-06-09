# Envoy startup and warming model

## Question

Envoy xDS ACKs are easy to overread. The xDS protocol says ACK means the client considered the individual resources valid and intends to apply them, but it does not mean the configuration has been applied successfully.

Envoy also warms clusters and listeners before they can serve traffic. Clusters warm until the matching EDS `ClusterLoadAssignment` response is supplied. Listeners that refer to RDS warm until the corresponding `RouteConfiguration` is supplied. Routes are not warmed, so the management server must sequence RDS after the referenced clusters are already usable.

The focused model asks whether a small startup/update sequence preserves active dataplane coherence when those distinctions are respected.

Primary source: <https://www.envoyproxy.io/docs/envoy/latest/api-docs/xds_protocol>

## Model

The model is `devel/formal/tla/XdsEnvoyWarming.tla`.

It abstracts:

- A cold startup path from no active listener to a fully active listener.
- A make-before-break update from `old` to `new`.
- CDS ACK, EDS ACK, RDS ACK, and LDS ACK as separate events.
- Active clusters separately from CDS-ACKed clusters.
- Active listener and route state separately from ACKed listener and route resources.

The model intentionally has only two names, `old` and `new`. The point is not resource cardinality; it is the ordering relationship between active dataplane state and acknowledged xDS resources.

## Safe Invariants

`XdsEnvoyWarming.cfg` checks:

- `ActiveClustersHaveCDSAndEDS`: a cluster cannot be active merely because CDS was ACKed; EDS must also be present.
- `ActiveRouteReferencesActiveCluster`: an active route cannot point at an inactive cluster.
- `ActiveListenerHasRouteConfig`: an active listener cannot point at an RDS route config that is not present.
- `ActiveListenerAndRouteAgree`: the active listener's route identity matches the active route.
- `StartupActiveOnlyAfterClosure`: cold startup reaches active only after CDS, EDS, RDS, and LDS closure exists.
- `NoBreakBeforeMake`: the old active cluster is not removed before the active route has moved away from it.

## Counterexamples

`XdsEnvoyWarmingAckImpliesActiveBug.cfg` demonstrates why ACK is not active:

- Envoy starts cold.
- CDS for `new` is ACKed.
- A buggy transition marks `new` active before EDS arrives.
- `ActiveClustersHaveCDSAndEDS` fails.

`XdsEnvoyWarmingRouteBeforeClusterBug.cfg` demonstrates the route-before-cluster drop shape:

- Envoy starts with `old` active.
- CDS for `new` is ACKed, but EDS for `new` has not arrived.
- A buggy transition activates an RDS route to `new`.
- `ActiveRouteReferencesActiveCluster` fails.

`XdsEnvoyWarmingListenerBeforeRouteBug.cfg` demonstrates the listener-before-RDS shape:

- Envoy starts cold.
- CDS and EDS for `new` are ACKed.
- A buggy transition activates the listener before RDS for `new` exists.
- `ActiveListenerHasRouteConfig` fails.

## Result

The safe model supports this statement:

> Startup and make-before-break updates preserve active dataplane closure when clusters become active only after EDS, listeners become active only after RDS, and route updates are sequenced after referenced clusters are active.

It does not prove Envoy internals. It is an executable abstraction of the ordering obligations kgateway must respect when publishing xDS.

## How to Run

Run the passing warming model:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar \
  -config XdsEnvoyWarming.cfg \
  XdsEnvoyWarming.tla
```

Run the intentional counterexamples:

```bash
cd devel/formal/tla
java -jar /path/to/tla2tools.jar \
  -config XdsEnvoyWarmingAckImpliesActiveBug.cfg \
  XdsEnvoyWarming.tla

java -jar /path/to/tla2tools.jar \
  -config XdsEnvoyWarmingRouteBeforeClusterBug.cfg \
  XdsEnvoyWarming.tla

java -jar /path/to/tla2tools.jar \
  -config XdsEnvoyWarmingListenerBeforeRouteBug.cfg \
  XdsEnvoyWarming.tla
```
