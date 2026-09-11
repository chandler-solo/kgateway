# xDS assurance status

This is the current status of the research branch, superseding stronger claims
in historical investigation notes. The program is **open**. Findings and next
actions are tracked in [research-findings.md](research-findings.md).

## Profile

Profile `research-2026-09-10`, read from the post-merge branch:

| Component | Selected revision or configuration |
|---|---|
| Baseline cold-start correction | `8c451a4b5bcf072b7c43159be2a4069eefe2fc5e` |
| Go | `1.26.7` |
| go-control-plane cache/server | `v0.14.0` |
| Envoy protos | `v1.37.1-0.20260529185539-1175069dbb2c` |
| Contrib protos | `v1.36.1-0.20260529185539-1175069dbb2c` |
| Ratelimit protos | `v0.1.1-0.20250507123352-93990c5ec02f` |
| gRPC | `v1.83.2` |
| Protobuf | `v1.36.12-0.20260120151049-f2248ac996af` |
| Envoy image default | `envoyproxy/envoy:v1.39.1` (direct binary profile in envoy-characterization.md) |
| Lean | `leanprover/lean4:v4.30.0` |
| Normal delivery | Snapshot cache, SotW ADS, node-role key |
| Ordered ADS | `EnableOrderedAds`, passed by setup; both modes have probes |

The Makefile tag is not an immutable Envoy binary identity.
[Direct probes](envoy-characterization.md) record a digest and runtime profile;
RF-004 remains open for the untested lifecycle/configuration combinations.
The earlier plan's v1.37.2 was its review baseline, not this merged profile.
Dependency modules and binary versions are separate identities.

## Guarantee table

| Subject | Evidence level | Limit |
|---|---|---|
| Convergence-machine structural invariants | Proved in Lean | Abstract atomic transitions, serial episodes, name-set versions |
| Multi-client frame/isolation | Proved in Lean | Disjoint client components by construction; no shared-key refinement |
| Recovery from deferred state | Constructive existence proof | No fairness, wall-clock bound, or guarantee of coherent future input |
| Finite model safety | Model-checked with bounded names and states | Run output records each explored domain; not arbitrary concurrency |
| Finite model recovery | Reachability checked | Does not imply eventuality on all fair executions |
| Cold empty-endpoint publication | Go tests, Lean safety/recovery, TLC temporal check | Missing CDS still blocks; cache behavior is not Envoy application |
| Cold permanent-missing CDS publication | Cache-boundary probe, TLC temporal counterexample (RF-003) | Whole first publication withheld indefinitely; classification policy proposed, not implemented; nil-translation drop RF-024 reproduced with a synthetic plugin, fix open |
| Snapshot trace validity | Per-event structural checks plus per-client EDS version relation (schema 2) | No lifecycle, acceptance, or activation replay; churn is counted, not failed |
| GCP named response guard | Contradicted/incomplete; post-pin diff recorded | Equal-version subscriptions and both lost-watch paths omitted; unreleased #1356 retains the parked declined watch and stops the unsubscribe-all leak, request-entry decline unchanged |
| GCP ordering/callback behavior | Implementation-characterized | Scripted schedules; ordering is not an activation barrier |
| NACK resend recurrence | Characterized on the real cache with a scripted client and with a real Envoy (RF-012) | One machine; no damping exists; not a CPU budget |
| SotW rejection semantics | Directly characterized for CDS, LDS, EDS, RDS, and SDS; descriptive Lean model (RF-026) | CDS and LDS apply valid siblings on NACK; EDS, RDS, and SDS reject the whole response; convergence models keep the atomic abstraction; Delta untested |
| Cache installation receipts | Trace relation from decision to installation on the per-type version tuple (RF-006) | Delivery, acceptance, and activation not instrumented; transform-only scenarios counted |
| Secret delivery and removal | Directly characterized over SotW ADS (RF-027) | Rotation applies in place; removal is ACKed and revokes nothing; missing secret holds only its listener; a provider-key change is served over SotW; CA and client-certificate secrets, standalone SDS server, and Delta untested |
| Control-plane and proxy restart | Directly characterized through SnapshotCache in both ADS modes | Empty cache is harmless while the proxy keeps serving; reconnect carries versions without nonces; a restarted proxy is served from the warm cache; the kgateway first-publish gate for a cache-less client is not exercised |
| Initial fetch timeouts | Directly characterized at 2 s | Missing EDS and RDS become degraded active states after the timeout, sequentially across init phases; a route ahead of its cluster is a 503 window; default 15 s untested |
| Envoy initialization/traffic | Directly characterized for two pinned profiles plus the references scenario | Empty EDS initializes; panic changes selection; dangling RDS is per-route 503; NACK applies valid siblings (RF-026); broader ENV-A1 remains open |
| Hash injectivity | Open abstraction assumption; contract stated in Lean, version reuse detected per run | Finite-width digest cannot be unboundedly injective; XOR combiner cancels equal per-resource digests |
| KRT recovery | Open; temporal claim stated with explicit fairness (RF-005) | No deployed watchdog exists; progress rests on KRT delivery, which is unverified; a stale-replay watchdog would not help |
| Historical coverage | Complete title/body inventory, forty-four detailed dispositions | Candidate classification, linked-source audit, and non-keyword review remain open |
| Protocol/feature coverage | Source-anchored reachability inventory | Every known registered/deployed path has a disposition; most nondefault paths still need characterization and modeling |

## Required evidence

Run `CGO_ENABLED=0 make formal-lean`. The runner keeps Go JSON receipts and
isolated snapshot traces and fails if required tools or scenario publications
are absent. Declaration checks alone do not prove tests ran. The receipt gate now requires run/test-pass/package-pass events for unit
evidence and rejects skipped descendants. CI runs on every PR change, uploads
artifacts, and runs bounded TLC and both direct Envoy profiles. RF-009 still
tracks full lifecycle/live KGW evidence; RF-015 tracks original broad TLC bounds.
The receipt gate also executes the strict, source-anchored
[`protocol-scope.yaml`](protocol-scope.yaml) inventory check. This detects a
removed or renamed construction point; it does not prove that the inventory is
complete.
TLC is separate from the Lean recovery checker; the three-state
`ReachabilityIsNotLiveness` example must produce a temporal counterexample.

No test pass here closes RF-002/003's product-policy decisions, RF-004's Envoy
characterization, or the program's historical/source coverage obligations.
