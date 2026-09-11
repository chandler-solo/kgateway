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

Before implementing isolation, derive the resource dependency graph from
actual emitted payloads. It must include listener-to-route, route-to-cluster,
cluster-to-endpoint, and listener/filter/transport-to-secret references. Shared
secrets and route configurations can connect otherwise independent resources.
Unknown typed configs require an explicit conservative disposition.

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
