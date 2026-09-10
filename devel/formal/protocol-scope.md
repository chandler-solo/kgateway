# xDS protocol scope inventory

[`protocol-scope.yaml`](protocol-scope.yaml) is the machine-readable inventory
for profile `research-2026-09-10`. It answers which protocol and implementation
paths must receive a formal disposition before this research program can make a
bounded completeness claim.

## Discovery method

No single source enumerates the relevant state space. The inventory is the
union of four views:

1. **Registered server surface.** Start at each production gRPC registration,
   inspect the pinned generated service descriptor, and inspect the pinned
   go-control-plane server implementation. Registration makes the descriptor's
   complete method set callable. For the main control plane this includes ADS
   SotW and Delta plus individual EDS, CDS, RDS, and LDS SotW, Delta, and Fetch.
   The separate SDS server registers secret SotW, Delta, and Fetch.
   RPC enumeration is insufficient by itself because ADS can carry resource
   types that have no separately registered service.
2. **Constructed implementation surface.** Record the cache, server options,
   node-key function, callbacks, authentication, and listener used by each
   registration. A library feature is excluded only when production
   construction makes it unreachable. Shared mechanisms still enter the source
   and historical review because defects can transfer between cache types.
3. **Client and bootstrap surface.** Inspect every generated bootstrap and
   overlay. The normal Envoy bootstrap selects SotW ADS, but static resources,
   conditional local EDS, timeouts, and SDS create behavior outside the main
   dynamic snapshot relation. Independently enumerate the resource families
   placed in each snapshot and all references between their payloads.
4. **Historical and adversarial surface.** Add mechanisms found by the bug
   corpus, direct dependency probes, model counterexamples, and compositions
   across cache receipt, transport send, Envoy acceptance, initialization, and
   traffic. This view can add state that public APIs and normal configuration do
   not reveal, such as stale nonces, lost watches, partial sends, and rewarming.

An entry is `open`, `partially-modeled`, `partially-characterized`,
`characterized-only`, or `excluded`. Every entry names a source file and anchor,
current evidence, and a concrete action. Exclusion means unreachable in the
named profile; it does not mean the mechanism is irrelevant to historical
review.

## Completeness boundary

The strict test verifies schema, unique IDs, controlled dispositions, source
files and anchors, and finding references. It is an executable drift detector,
not a proof that humans found every path. New registrations, bootstrap variants,
feature flags, xDS dependencies, corpus mechanisms, and deployment modes must be
added when discovered.

The program may claim the agreed profile is accounted for only after all
critical entries have appropriate model, implementation, and live-system
evidence, and every remaining open or excluded entry has an accepted severity
disposition. RF-019 and RF-020 currently prevent that claim.
