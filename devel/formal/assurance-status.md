# xDS assurance status

This is the current status of the research branch, superseding stronger claims
in historical investigation notes. The program is **open**. Findings and next
actions are tracked in [research-findings.md](research-findings.md).

## Profile

Profile `research-2026-10-16-working-tree`, based on
`a4cf59d6d80372ca9d0baaa4cfe543c228bf1857`. A source SHA alone does not identify
these uncommitted changes; preserve the working diff with execution receipts.

| Subject | Identity and disposition |
|---|---|
| Research | This working tree: derived-empty truth, shared-CLA carry preservation, retained dependency closure, existing trace harness |
| Reviewed proposed stack | `kgateway-dev/kgateway` stack 14817, tip `e7b569a1fb786a58a81cb5c0364d57769786b67d`, base `982e29797b5028ca22d5c4ce41d06f427a8cdcd3`; **not ported into this tree** |
| Downstream source | kgw-ent `1bc4cde8b4edc780135c98d2268f43289978373c`, kgateway dependency `acd13c6f53d1`; adoption requires constructor and connection-state integration |
| Deployment | No live deployment identified by this review |
| Cache/server | `v0.14.1-0.20260702184136-1cd1226616f5`; parked-watch retention and unsubscribe fixes present, immediate-decline loss remains |
| Research/upstream default Envoy tag | `envoyproxy/envoy:v1.39.4` in the current Makefile; not an immutable binary identity |
| Downstream defaults | enterprise standard/FIPS `v1.39.1-patch1`; separate profiles, not covered by upstream binary observations |
| Recorded direct Envoy evidence | v1.39.1 digest `sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4`; bootstrap/runtime-specific |
| Lean | `leanprover/lean4:v4.30.0` |

The [pinned regression profiles](regressions/profiles.json) and runner preserve
historical defects separately from current research behavior. The stack still
has nine failing properties and a setup-package compile failure at its reviewed
revision. Green negative-control receipts mean those defects reproduced.
Research has no stack publication budgets, scoping, NACK decorator, reconnect
rewriting, or snapshot reclaimer; this update does not imply those fixes landed.

## Guarantee table

| Subject | Evidence level | Limit |
|---|---|---|
| Convergence-machine structural invariants | Proved in Lean | Abstract atomic transitions, serial episodes, name-set versions |
| Multi-client frame/isolation | Proved in Lean | Disjoint client components by construction; no shared-key refinement |
| Recovery from deferred state | Constructive existence proof | No fairness, wall-clock bound, or guarantee of coherent future input |
| Finite model safety | Model-checked with bounded names and states | Run output records each explored domain; not arbitrary concurrency |
| Finite model recovery | Reachability checked | Does not imply eventuality on all fair executions |
| Cold empty-endpoint publication | Go tests, Lean safety/recovery, TLC temporal check, live XdsWarming and XdsStarvation suites passed once against the research image | Missing CDS still blocks; the live suites are pass/fail without traces and the starvation suite is a regression pin, not a #14184 reproducer |
| Derived empty warm publication | Guard proofs plus concrete KRT/cache regressions | No endpoint-health gate; new live scenario code requires a fresh run on each named runtime profile |
| Retained dependency and version provenance | Go regression tests | Shared assignments preserve current bytes/hash; supported ancillary/transitive dependencies retained; opaque and recognized dynamic extensions conservatively retain eligible CDS; arbitrary extension semantics and revocation remain open |
| Overlapping publication timers | Finite TLC model and pinned stack Go counterexamples | One key, three revisions, logical deadlines; proposed fixed model is not a refinement of stack Go |
| Cold permanent-missing CDS publication | Cache-boundary probe, TLC temporal counterexample (RF-003) | Whole first publication withheld indefinitely; classification policy proposed, not implemented; nil-translation drop RF-024 reproduced with a synthetic plugin, fix open |
| Snapshot trace validity | Per-event structural checks plus per-client EDS version relation (schema 2) | No lifecycle, acceptance, or activation replay; churn is counted, not failed |
| GCP named response guard | Executable characterization on the pinned dependency | Parked declines retain watches; immediate declines still lose requests; subscription history and response events matter independently of version equality |
| GCP ordering/callback behavior | Implementation-characterized | Scripted schedules; ordering is not an activation barrier |
| NACK resend recurrence | Characterized on the real cache with a scripted client and with a real Envoy (RF-012) | One machine; no damping exists; not a CPU budget |
| SotW rejection semantics | Directly characterized for CDS, LDS, EDS, RDS, and SDS; descriptive Lean model (RF-026) | CDS and LDS apply valid siblings on NACK; EDS, RDS, and SDS reject the whole response; convergence models keep the atomic abstraction; Delta untested |
| Cache installation receipts | Trace relation from decision to installation on the per-type version tuple (RF-006) | Delivery, acceptance, and activation not instrumented; transform-only scenarios counted |
| Secret delivery and removal | Directly characterized over SotW ADS (RF-027) | Rotation applies in place; removal is ACKed and revokes nothing; missing secret holds only its listener; a provider-key change is served over SotW; CA and client-certificate secrets, standalone SDS server, and Delta untested |
| Control-plane and proxy restart | Directly characterized through SnapshotCache in both ADS modes, including a restart during rewarming (RF-028); TLC counterexample and three passing configurations in `ReconnectWhileWarming.tla`; live XdsWarming restart-during-warming test passed once | Empty cache is harmless while the proxy keeps serving; reconnect carries versions without nonces; a restarted proxy is served from the warm cache; a reconnect while a cluster is warming re-requests EDS, LDS, RDS but not CDS until EDS completes the warming, so a withheld or equal-version-parked EDS blocks CDS repairs for up to the EDS initial fetch timeout (15 s in kgateway's bootstrap, where the EDS cache then completes the warming; unbounded only with the timeout disabled); the kgateway first-publish gate for a cache-less client is not exercised |
| Initial fetch timeouts | Directly characterized at 2 s | Missing EDS and RDS become degraded active states after the timeout, sequentially across init phases; a route ahead of its cluster is a 503 window; the separate default-timeout rewarming correction is recorded in RF-028; no universal end-to-end bound |
| Envoy initialization/traffic | Directly characterized for two pinned profiles plus the references scenario | Empty EDS initializes; panic changes selection; dangling RDS is per-route 503; NACK applies valid siblings (RF-026); broader ENV-A1 remains open |
| Hash injectivity | Open abstraction assumption; contract stated in Lean, version reuse detected per run | Finite-width digest cannot be unboundedly injective; even sets of distinct contributions can share an XOR version; the toy collision remains a negative control |
| KRT recovery | Open; temporal claim stated with explicit fairness (RF-005) | No deployed watchdog exists; progress rests on KRT delivery, which is unverified; a stale-replay watchdog would not help |
| Historical coverage | Complete title/body inventory, seventy-two detailed dispositions | Candidate classification, linked-source audit, and non-keyword review remain open |
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

RF-002's endpoint-contents policy is settled: derived empties publish. The
research implementation now follows that policy, while legacy counterexamples
remain runnable. Underived/missing input can still hold this research profile
indefinitely. No test pass establishes full lifecycle refinement, temporal
progress of Go, deployment correctness, or completed historical/source coverage.

## October 16 working-tree verification

| Check | Observed result | Evidence boundary |
|---|---|---|
| `CGO_ENABLED=0 make formal-lean` | 320 top-level Go tests / 567 test and subtest passes across nine packages; 39 required receipt obligations; 50 Lean model expectations; 27 implementation trace scenarios | Guard proofs, finite models, Go behavior and cache-boundary trace conformance; not a Go refinement theorem |
| `devel/formal/tla/check-required.sh` | All 42 required configurations matched their expected outcomes; the new fixed timer model explored 38 states | Includes expected safety/temporal counterexamples; original wide ADS bounds remain deferred under RF-015 |
| `make formal-stack-regressions` | Nine exact property failures reproduced at the stack pin; known setup compilation failure checked separately | Historical negative controls; this does not approve the stack |
| `make formal-legacy-contents` | Original contents-gate starvation test reproduced on its immutable baseline | The fixture is byte-identical to the original test |
| `make formal-regression-harness` | Five harness tests passed | Rejects missing/wrong outcomes and reuse of existing artifacts or writes inside candidate source |
| Trace replay fixtures | Candidate/resolver overlap passes; toy XOR collision reports the required `version-reuse` | Single publisher per client assumed; no production hash collision established |
| `make analyze` with no fixes and `--new-from-rev=HEAD`; `make lint-actions` | No new Go lint issues; workflow lint passed | YAML formatting disabled during analysis to avoid unrelated file changes |
| Warming suite through `make go-test`, selecting no tests | Compiles | No live test executed |

The Lean runner records HEAD, the tracked working diff, added-file digests,
dependency digests, tool versions, and execution receipts. New live warming,
downstream adoption, enterprise/FIPS, cancellation-race, and composed
cache-to-application evidence remain open in the research plan. No dependency
bump, downstream source change, commit, or deployment is part of this update.
