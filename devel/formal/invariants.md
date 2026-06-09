# xDS invariant families

## Snapshot dependency closure

- Every RDS name referenced by an LDS HTTP connection manager exists in the emitted RDS set.
- Every direct cluster name referenced by a route exists in CDS.
- Every weighted cluster entry referenced by a route exists in CDS.
- Every cluster name referenced by a checked OAuth2 token endpoint, injected OAuth2 credential token endpoint, JWT AuthN remote JWKS, ExtAuthz HTTP filter, ExtProc HTTP filter, or global RateLimit HTTP filter exists in CDS.
- Every statically typed ExtProc filter nested under an Envoy `ExtensionWithMatcher` composite action references an existing CDS cluster.
- Every ExtProc per-route override service cluster referenced by virtual host, route, or weighted cluster `typed_per_filter_config` exists in CDS.
- Every service cluster referenced by a checked HTTP gRPC, TCP gRPC, or OpenTelemetry access logger exists in CDS.
- Every service cluster referenced by a checked OpenTelemetry, Datadog, Lightstep, SkyWalking, or Zipkin tracing provider exists in CDS.
- Every EDS cluster has a matching ClusterLoadAssignment by service name, or by cluster name when service name is empty.
- Every emitted ClusterLoadAssignment corresponds to an emitted EDS cluster by service name, or by cluster name when service name is empty.
- Every SDS secret name referenced by a checked TLS transport socket, OAuth2 HTTP filter, generic injected credential, OAuth2 injected credential, or generic-secret formatter exists in the emitted SDS set.
- Resource names are unique within each resource type.

## xDS publication safety

- Server accepted version for a resource type advances only after an ACK for the latest response nonce for that type on that stream.
- NACK does not advance the accepted version.
- DiscoveryRequests with stale nonce do not cause the server to publish or mark acceptance for an old response.
- Reconnect creates a new stream nonce context, while resource versions remain resource-level state.
- After reconnect, a retained coherent per-client snapshot must not be overwritten by a partial snapshot whose dataplane route/listener cluster references are missing from CDS unless those clusters are explicitly errored.
- During startup or reconnect defer windows, Envoy's active snapshot remains coherent while the control plane retains the last coherent per-client cache snapshot.
- A per-client KRT delete/defer event caused by incoherent inputs must retain the last coherent xDS cache snapshot.
- A partial computed per-client snapshot must not overwrite the coherent per-client xDS cache snapshot.
- Once a partial input becomes coherent, the control plane must be able to publish that coherent snapshot.
- Once Envoy's known CDS names match the per-client cache CDS names, the cache EDS resource set must be compatible with Envoy's named EDS request.
- A version-new named EDS response must not contain snapshot resources outside Envoy's requested EDS names.
- If the per-client EDS resource set changes, the EDS version must change.
- Envoy active state must not move to a new route/cluster snapshot before CDS and EDS closure exists.
- CDS ACK alone does not make a cluster active; an active EDS cluster must have CDS state and a ready `ClusterLoadAssignment`.
- An empty `ClusterLoadAssignment` is ACKed EDS state, but it is not ready endpoint state for activating routes that need usable endpoints.
- An active route must not reference a cluster that is not active.
- An active listener using RDS must not reference a route configuration that is not present.
- Make-before-break updates must not remove the old active cluster before active traffic has moved away from it.
- A stable valid desired snapshot is eventually publishable under a fair client that ACKs valid responses.

## Explicitly unverifiable and dynamic MVP cases

- `cluster_header` route actions.
- Dynamic forward proxy.
- Extension typed configs not recognized by the validator.
- SDS references from extension typed configs not recognized by the validator.
- Dynamically discovered composite filter configs and composite `filter_chain_name` targets that cannot be resolved from the emitted snapshot.
