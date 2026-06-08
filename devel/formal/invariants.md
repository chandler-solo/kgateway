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
- A stable valid desired snapshot is eventually publishable under a fair client that ACKs valid responses.

## Explicitly unverifiable and dynamic MVP cases

- `cluster_header` route actions.
- Dynamic forward proxy.
- Extension typed configs not recognized by the validator.
- SDS references from extension typed configs not recognized by the validator.
- Dynamically discovered composite filter configs and composite `filter_chain_name` targets that cannot be resolved from the emitted snapshot.
