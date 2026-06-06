# xDS invariant families

## Snapshot dependency closure

- Every RDS name referenced by an LDS HTTP connection manager exists in the emitted RDS set.
- Every direct cluster name referenced by a route exists in CDS.
- Every weighted cluster entry referenced by a route exists in CDS.
- Every EDS cluster has a matching ClusterLoadAssignment by service name, or by cluster name when service name is empty.
- Every SDS secret name referenced by a checked TLS transport socket or OAuth2 HTTP filter exists in the emitted SDS set.
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
