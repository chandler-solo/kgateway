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
| Snapshot trace validity | Per-event structural checks | No payload-version, lifecycle, acceptance, or activation replay |
| GCP named response guard | Contradicted/incomplete | Equal-version subscriptions and both lost-watch paths omitted |
| GCP ordering/callback behavior | Implementation-characterized | Scripted schedules; ordering is not an activation barrier |
| Envoy initialization/traffic | Directly characterized for two pinned profiles | Empty EDS initializes; panic changes selection; broader ENV-A1 remains open |
| Hash injectivity | Open abstraction assumption | Finite-width digest cannot be unboundedly injective |
| KRT recovery | Open | Existential model heartbeat is not a deployed progress guarantee |
| Historical coverage | Complete title/body inventory, ten detailed dispositions | Candidate classification, linked-source audit, and non-keyword review remain open |
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
