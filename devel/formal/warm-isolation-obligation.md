# Warm publication isolation obligation

RF-002 is now represented at two evidence levels: the Go cache-boundary probe
`TestWarmEmptyBackendHoldsUnrelatedRouteAndSecret` and the temporal model
`tla/WarmTypeStarvation.tla`. They describe the current whole-type hold policy
under permanently empty input. Neither claims live certificate activation.

## Temporal assumptions

The model starts after an initial publication, with both a blocked route flip
and an independent update pending. No endpoint-ready transition exists because
permanent emptiness is legal input. Weak fairness forces publication to run;
it does not force endpoints to appear. Under the current policy, publication
continues indefinitely while independent progress fails. The two-valued CDS
revision abstracts repeated rebuilds, not actual version allocation.

The isolated configuration specifies the desired progress property under an
assumed independent component. It is a proposed policy witness. Its passing
result does not prove that the implementation can identify that component or
that arbitrary mixtures of old and new resources are safe.

## Required implementation refinement

`xdscheck.DependencyGraphOf` now derives the reference graph from emitted
protos along the checker's traversal: listener-to-route, listener or
route-to-cluster (route actions, filters, access logs, tracing),
cluster-to-endpoint, and listener/cluster/filter-to-secret. `Components`
partitions it; shared secrets and route configurations connect otherwise
independent chains, dangling references stay attached to their component,
and any typed config the checker cannot unpack marks its resource opaque so
the component is not independent. The graph is exactly as complete as the
checker's traversal, which is fallible by construction (RF-021). Unit tests
cover separation, a shared secret, dangling references, opacity, and the
blackhole exemption; the basic HTTP routing fixture partitions without opaque
or dangling members.

Choose a publication unit that preserves reference closure and security
semantics when a blocked component retains older state. In particular, secret
revocation and invalid security policy must not become indefinite last-good
retention. Component-level cache closure is also insufficient to establish
Envoy application order; test acceptance and activation separately.

Action items remain RF-002 and RF-021: compute and validate the partition,
specify deletion/revocation precedence, implement the chosen policy, prove or
check its refinement, and run live unrelated route/certificate changes while
another backend remains empty. Do not mark those items discharged by the
isolated model's Boolean assumption.
