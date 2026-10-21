# xDS assurance status

This is the current status of the research branch, superseding stronger claims
in historical investigation notes. The program is **open**. Findings and next
actions are tracked in [research-findings.md](research-findings.md).

## October 20 per-type snapshot cache composition (PR #14865)

The subjects are PR #14865 at `4635e171b116` (main `86f5499cea01` merged in)
and stack tip `c38563730069`, composed as a rebase of the stack onto the PR
would produce. The composition is not a published revision. The program stays
open.

| Conclusion | Evidence level and boundary |
|---|---|
| Under go-control-plane's ADS rule, one rejected cluster or listener freezes its children's updates, also after its removal | `PerTypeCacheRejection`: required temporal counterexample to `ChildrenConverge`; bounded, one parent type |
| The PR's per-type caches deliver the accepted parents' children | Same model, per-type rule: `ChildrenConverge` and `NoFutureChildren` hold on all reachable states; bounded, no Go refinement |
| Per-type installs need the PR's per-node lock for the stack's connection-start clear | `PerTypeInstallClear`: `NoStaleAfterClear` fails without the lock and holds with it; bounded, abstract install/clear steps |
| The stack works over the per-type caches | `make formal-stack-pertype`: five-package race suite (1,699 test events) on the composed subject; the PR's six scenarios through the stack's watch policy in both ADS modes; mock ADS stream, no Envoy |
| The stack's recorded properties survive the composition | The nine backend-closure, seventeen history and six rewarming properties pass on the composed subject and on the pinned tip alone; RF-034's live replay was not re-run |

All 87 required TLC outcomes match, including the four new configurations.
The stack's watch-count test helper needs one edit when it rebases onto the
PR; production sources are not adapted.
[RF-036](research-findings.md#rf-036-pr-14865s-per-type-snapshot-cache-composed-with-stack-14817)
records the subjects, evidence and limits; the
[acceptance commands](regressions/README.md#october-20-per-type-cache-composition-pr-14865)
reproduce them.

## Later October 20 updated-stack closure review

The reviewed PR #14717 revision is `2fe5bab3179dd1a3009baed8612aecde5a392941`;
the final stack tip is `e9436e00b3ca13922a22d40d831345e28ce3ec22`. These are
immutable source subjects, separate from the research production code and any
deployed downstream. Publication correctness is **disproved by a P2
counterexample** at these pins; this does not mean every update fails.

| Conclusion | Evidence level and boundary |
|---|---|
| PR #14717 fixes initial selection of proxy clusters named only by a kept backend's own listener | Before/after actual KRT/publisher fixture: REFERENCED fails at parent `350554e6f3c3` and passes at `2fe5bab3179d` and final tip; ALL controls pass |
| The new helper computes the intended finite closure | Actual helper/scanner versus an independent graph oracle on all 28,672 three-name inputs; three race-enabled repetitions at the final tip; no unbounded Go proof |
| Carrying an old backend can drop its owned listener and secret; REFERENCED also drops its proxy | Five designated desired-property failures in actual KRT/publisher/cache at both PR revision and final tip; temporary missing translated row, route still present, no explicit error or expiry |
| Dropping that owned listener can break forwarding | Actual emitted snapshots replayed through the real cache/server and enterprise standard Envoy 1.39.4-patch2: baseline 200, active carried cluster without its listener, then fresh 503 requests in both modes; no full CONNECT/Kubernetes deployment claim |
| A backend-listener dynamic fallback is hidden from the disabled-scoping gauge | Final-tip implementation test: emit-all fallback, gauge expected one but actually zero; source also omits the transition warning for this cause (P3) |
| Closure must include the root's reachable owned dependencies | Seven abstract Lean structural proofs and four bounded TLC profiles; current selection counterexamples and proposed closure controls; no Go refinement, delivery or liveness theorem |

The earlier rewarming repair remains a separate finding and obligation. Its
historical live observations below are not new live executions of this tip.
[RF-034](research-findings.md#rf-034-backend-owned-resource-closure-after-the-october-20-stack-update)
and the [acceptance matrix](regressions/README.md#october-20-backend-owned-resource-regressions)
record exact tests, scope and remaining work. The backend carry remedy exists
only in the specification; production source is unchanged. Raw captures,
receipts and logs remain outside Git.

Validation: the final five-profile matrix matches 42 required Go executions
and two live counterexamples, rejecting 22 capture mutations. All seventeen
earlier history and six rewarming properties pass at the new tip. The final
nine-property fixture matches its designated outcomes in three race-enabled
repetitions. Full `CGO_ENABLED=0 make formal-lean` passes 50 model expectations,
603 research Go test events, 27 snapshot traces and four lifecycle traces;
all 83 required TLC outcomes match. The native five-package updated-stack race
suite passes 1,961 test events. Fifteen harness tests, fixture checks, workflow
lint and whitespace checks pass. Public CI explicitly omits the private-image
live replay; no remote workflow was run by this session.

## October 20 downstream rewarming correction

The latest rewarming comparison is proposed stack `faaaaf68d552` versus
`ab9865b83e63`, using downstream `dcfc92b5c210` with its dependency replaced
only in an isolated copy. These subjects supersede older stack identities for
this finding; the October 19 profile below and its results remain historical.
The research implementation has not adopted the stack's replay policy.

| Conclusion | Evidence level and boundary |
|---|---|
| A BackendConfigPolicy update can leave the old cluster serving for about 15 seconds when EDS arrives before changed CDS | Live downstream setup/plugins/KRT, envtest Kubernetes and enterprise Envoy 1.39.4-patch2: five stalls in 32 add/remove transitions across ALL and REFERENCED; wire, admin state and traffic checked separately |
| An RDS route to a new DNS backend can return 500 while its CDS waits behind that warming | Live historical REFERENCED run: route present, cluster absent, 500s during warming, recovery to 200 after CDS resumes |
| The author's October 20 fix answers the held-version request and removes this measured stall | Source commit `5c5b456be396`; live repaired-tip rerun: 16 transitions applied in 215–229 ms, four EDS-before-CDS schedules received a fresh same-version EDS response 1–5 ms after CDS, no mid-run EDS timeout |
| The failure and repaired server behavior are durable regression obligations | `make formal-stack-rewarming`: six properties per immutable pin; four designated historical failures and two controls, then six repaired passes, with mock stream transport and CDS deliberately unacknowledged |
| The modeled replay path can progress without expiry | Four bounded TLC profiles with explicit weak fairness; not the Lean reachability claim and not a Go liveness or quantitative deadline proof |

The live runs used ARM64 Docker Desktop, two Envoy workers, disabled xDS
authentication, an isolated Kubernetes API and a fixture HTTP upstream. They
execute downstream controller translation but not a deployed Kubernetes
installation or every `main.go` option/controller. The fixed run did not
execute its conditional new-DNS insertion because no warming interval was
sampled. FIPS, alias/shared assignments, rejection, send failures, multiple
streams and overlapping changes remain unvalidated for this repair.
[RF-033](research-findings.md#rf-033-downstream-policy-rewarming-and-the-october-20-stack-fix)
records the exact subjects, remaining obligations and external capture locations.
No new product policy or timeout default is introduced into this research tree.

Validation of this update: full `CGO_ENABLED=0 make formal-lean` passes 50 model
expectations and 603 Go test events, with 27 snapshot and four lifecycle traces.
All 79 required TLC outcomes match. The new immutable before/after regression,
nine harness tests, 18 repaired race-test passes and workflow lint pass. CI now
requires both immutable rewarming profiles; no remote workflow was run by this
session. Raw logs and receipts remain external artifacts.

## October 19 timeout extension

[Source-specific timeout formalization](timeouts/README.md) supersedes older
claims that every missing xDS dependency has a finite Envoy bound, or that an
unset-CDS/LDS fixture reproduced the complete kgateway bootstrap graph.

| Conclusion | Evidence level and boundary |
|---|---|
| CDS/LDS explicitly disable initial-fetch expiry; local EDS uses 1s; generated EDS/RDS use the unset 15s default | Pinned source inspection, actual chart rendering and separately checked executed/admin bootstraps |
| Disabled sources can block initialization; default EDS/RDS expiry can yield degraded active state | Timed bounded model and direct runtime observations; finite silence windows are not infinite-wait proofs |
| EDS rewarming can complete from cached data without a fresh EDS response | Direct real-cache/server/Envoy histories with counter increments, active/warming admin state and HTTP; no general cache-freshness theorem |
| Expiry needs the owning provider and a due deadline and cannot invent receive/accept counts or select a wrong cache identity | Seven abstract Lean safety theorems; C++/Go refinement and runtime provider-replacement races remain open |
| Timers do not establish a global fifteen-second deadline or complete configuration | Required model counterexamples; real scheduling and network bounds are unproved |

The fifteen added TLC configurations bring the required suite to 75, all matching
their designated outcomes; the original wide ADS model remains explicitly
deferred. Full `make formal-lean`, four race-enabled probe tests, ten timeout
harness tests, five live harness tests, scoped Go lint and workflow lint pass.
The existing sixteen direct Envoy profiles and twelve legacy runtime/image
profiles were rerun successfully.

The corrected live setup/bootstrap matrix completed all 36 histories: 24 repaired
passes and 12 historical counterexamples. Its final independent audit checked
156 admin/application observations, 2,318 HTTP samples and 504 capture mutations
without changing original captures or fixture hashes. These are new October 19
observations; older run counts below remain historical. Pinned Helm 3 rendering
was byte-identical to the Helm 4 bootstraps used for this complete live run.

The direct timeout acceptance matrix passed all 64 histories across four image
digests and both worker counts: 176 independent observations and 624 rejected
mutations, using repository-pinned Helm 3. Details and limits are recorded in the
[timeout profile](timeouts/README.md). Its supplied translations are separate
from live setup/KRT publication coverage. Enterprise standard/FIPS binaries
remain in scope, without a claim of full downstream controller execution or
validated deployment. Generated artifacts stay outside Git. No production
timeout default or publication policy is changed.

## Profile

October 19 profiles: `stack-14817-8e75468f4d0e`,
[`live/profiles.json`](live/profiles.json) and
[`timeouts/profiles.json`](timeouts/profiles.json), with research starting HEAD
`7b44c4c84721c2a7df465cc1271a28e88cf12be5` plus the working-tree timeout
formalization, harness and documentation changes. The earlier held-reconnect observations remain pinned to stack
`215ebe8e6450`; the new live profile separately checks repaired `8e75468f4d0e`
and historical `7d930bb2183b`.

| Subject | Identity and disposition |
|---|---|
| Research | Lifecycle/composed-runtime work, live setup/KRT/timer histories, timed bounded models and new abstract timeout Lean proofs; production publication code unchanged |
| Updated proposed stack | `kgateway-dev/kgateway` stack 14817, pinned at `8e75468f4d0e055deb9b8f34e4254831f011e2a3`; **not ported into this tree**; earlier immutable profiles and counterexamples preserved |
| Downstream source | kgw-ent `1bc4cde8b4edc780135c98d2268f43289978373c`, kgateway dependency `acd13c6f53d1`; ordinary upgrade adaptation is assumed; no downstream controller deployment validated |
| Deployment | No live deployment identified by this review |
| Cache/server | `v0.14.1-0.20260702184136-1cd1226616f5`; parked-watch retention and unsubscribe fixes present, immediate-decline loss remains |
| Research/upstream default Envoy | v1.39.4; the new live profile pins digest `sha256:74bd4eb5629708d3fadca1a5e6b85d208f338d7bb54e06b69565905c5294cc01` separately from the older v1.39.1 direct probes |
| Downstream defaults | enterprise standard/FIPS `v1.39.1-patch1`; four direct runtime scenarios now measured on each digest in `runtime/profiles.json`; downstream controller integration remains unvalidated |
| Recorded direct Envoy evidence | v1.39.1 digest `sha256:57e14a549d7bd43c8d3f6d03e8cfa653e037d4b38e133acd9b54f38c524401b4`; bootstrap/runtime-specific |
| Lean | `leanprover/lean4:v4.30.0` |

The [October 16 review](reviews/2026-10-16-stack-14817-7d930.md) remains a record
of 7d930's queued-build rollback and stale carry-age bugs. Its immutable profile
still requires those failures. Both properties pass after the author's fixes
at 530c4f. A further split reset/adoption timer race was reproduced on that
revision; 8e7546 makes cleanup and generation adoption one critical section.
`make formal-stack-8e7546` checks all sixteen historical desired properties plus
the native atomic reset/adoption test. Fixture-only adapters mirror the new
current-row initialization and explicitly assert the original initial-cache
premise; historical fixture bytes and assertions are retained.

The [live publication harness](live/README.md) now composes actual
NewControlPlane/authenticator/connection callbacks/watch policies, real KRT
delivery, elapsed timers, gRPC, independent Envoy admin state and HTTP
forwarding. Its fixture TokenReview authority and supplied translated snapshots
remain explicit boundaries. The additional bridge is test-only; original stack
production files and dependencies are unchanged. The research implementation
has not adopted the stack's production publication policy.

October 17 execution completed the corrected EDS-version profile: 24 repaired
history runs pass, and both historical bugs reproduce in all six image/worker
profiles (12 counterexample runs). The 36 race-enabled histories produced 156
independent admin/HTTP application observations and 2,390 continuous HTTP
samples on arm64 Docker Desktop. A final offline audit with the current checker
rejected 13 mutations per real capture (468 checks), verified exact
fixture/source/dependency hashes,
and left the original captures unchanged. Arbitrary fixture EDS versions are
not used: ready/carried A remains at contribution/version 17 and an omitted
assignment uses 0, with independent byte/version consistency checks.

The 17-property Go profile, all 60 expected TLC outcomes, 31 Python harness
tests, workflow lint and scoped Go lint pass. Earlier exploratory captures with
opaque fixture EDS versions are superseded by this corrected run. Generated
execution receipts and logs are external artifacts, not repository content.
These results establish the specified controlled histories, not a complete
controller, deployment, unbounded-concurrency or temporal-liveness proof.

The October 16 validation recorded a complete
required research run: 50 Lean expectations, 598 Go cases, 27 snapshot/four
lifecycle traces, the then-required 57 TLC configurations, sixteen direct Envoy
scenarios and all historical controls. The original wide ADS bounds remain
deferred. Four upstream runtime scenarios passed initially; an enterprise
warming attempt failed before ADS traffic, then all eight standard/FIPS
scenarios passed on a fresh rerun. Both outcomes are retained. The new stack's
four-package race suite separately passed 1,105 cases. These runs are not
whole-repository/e2e/image/deployment validation.

## Guarantee table

| Subject | Evidence level | Limit |
|---|---|---|
| Convergence-machine structural invariants | Proved in Lean | Abstract atomic transitions, serial episodes, name-set versions |
| Multi-client frame/isolation | Proved in Lean | Disjoint client components by construction; no shared-key refinement |
| Recovery from deferred state | Constructive existence proof | No fairness, wall-clock bound, or guarantee of coherent future input |
| Finite model safety | Model-checked with bounded names and states | Run output records each explored domain; not arbitrary concurrency |
| Lean finite checker recovery | Reachability checked | Does not imply eventuality on all fair executions |
| Timed initialization progress | Bounded TLC temporal checks with explicit weak fairness | Conditional on the representative chain and progress actions; disabled-source counterexamples remain required; no real scheduling or network deadline |
| Cold empty-endpoint publication | Go tests, Lean safety/recovery, TLC temporal check, live XdsWarming and XdsStarvation suites passed once against the research image | Missing CDS still blocks; the live suites are pass/fail without traces and the starvation suite is a regression pin, not a #14184 reproducer |
| Derived empty warm publication | Guard proofs plus concrete KRT/cache regressions | No endpoint-health gate; new live scenario code requires a fresh run on each named runtime profile |
| Retained dependency and version provenance | Go regression tests | Shared assignments preserve current bytes/hash; supported ancillary/transitive dependencies retained; opaque and recognized dynamic extensions conservatively retain eligible CDS; arbitrary extension semantics and revocation remain open |
| Selection and equality contract | Lean proofs over arbitrary payload/contribution types plus research Go tests | Pair provenance and precedence are abstract; equality adequacy is a premise, not a proved property of Go; zero-marshal XOR collision assumptions remain |
| Overlapping publication timers | Finite TLC model and pinned stack Go counterexamples | One key, three revisions, logical deadlines; proposed fixed model is not a refinement of stack Go |
| Admission-to-send progress | Bounded TLC temporal checks plus real cache/server probes | Fixed assumes retained admission and fair correcting publication after failure; ImmediateLost/NoRetry remain required temporal counterexamples; no wall-clock bound |
| Reconnect cache/queue generations | Bounded TLC safety/temporal checks; current stack's real admission callbacks and cache reproduce a concurrent clear/refill failure | Serial hook behavior passes; the two-hook implementation is not the model's atomic fence; old-queue negative control is abstract, shared authenticated streams remain open |
| Cold permanent-missing CDS publication | Cache-boundary probe, TLC temporal counterexample (RF-003) | Whole first publication withheld indefinitely; classification policy proposed, not implemented; nil-translation drop RF-024 reproduced with a synthetic plugin, fix open |
| Snapshot trace validity | Per-event structural checks plus per-client EDS version relation (schema 2) | No lifecycle, acceptance, or activation replay; churn is counted, not failed |
| Controlled lifecycle observations | Four real cache/server traces, strict Go replay and negative mutation tests | Records readback, watch disposition, response callbacks, mock transport and nonce-correlated ACK/NACK; separate from snapshot traces and live Envoy observations; no internal enqueue or application inference |
| Held reconnect rollback and recovery | Actual pinned stack gate, GCP cache/server, real gRPC and independent Envoy admin/HTTP observations on upstream and enterprise standard/FIPS | One key/proxy, two controlled controller instances, warm-state stub, delayed Send and direct timer invocation; raw unordered ADS excludes setup decorators/authentication; six observations per image; no downstream controller or natural timer scheduling evidence |
| Held reconnect model projection | Same TLA Step relation checks three observed 13-state projections; fenced bounded model checks 20 states plus conditional temporal recovery | Existential interval adapter is not verified refinement; malformed projection must fail ReplayOK; old-cache/stale-timer negative controls required; fairness of input, transport, application and timer execution remains assumed |
| Repaired publication histories through setup and Envoy | Live profile at 8e7546 checks queued reconnect, fresh carry episodes, reset/adoption exclusion and undrained cancellation; historical 7d930 supplies two required application-level counterexamples | Supplied translated snapshots and fixture TokenReview; Start wiring mirrored by a tagged test bridge; finite observations on one key/proxy; no full controller, shared-key, TLS or deployment proof |
| Admission/pending timer boundary | AdmissionPending checks atomic reset/adoption in 12 states and a separate captured-origin proposal in 22; split mode reproduces NoEarlierPendingResponse failure | Bounded safety model; no Go refinement map, general revision-freshness theorem or temporal liveness claim |
| GCP named response guard | Executable characterization on the pinned dependency | Parked declines retain watches; immediate declines still lose requests; subscription history and response events matter independently of version equality |
| GCP ordering/callback behavior | Implementation-characterized | Scripted schedules; ordering is not an activation barrier |
| NACK resend recurrence | Raw-cache/research recurrence characterized with a scripted client and real Envoy (RF-012); new stack's bounded retry/backoff passes Go/server tests | The proposed decorator changes recurrence; no new live Envoy retry/cancellation measurement or CPU bound |
| SotW rejection semantics | Directly characterized for CDS, LDS, EDS, RDS, and SDS; descriptive Lean model (RF-026) | CDS and LDS apply valid siblings on NACK; EDS, RDS, and SDS reject the whole response; convergence models keep the atomic abstraction; Delta untested |
| Cache installation receipts | Trace relation from decision to installation on the per-type version tuple (RF-006) | Delivery, acceptance, and activation not instrumented; transform-only scenarios counted |
| Secret delivery and removal | Directly characterized over SotW ADS (RF-027) | Rotation applies in place; removal is ACKed and revokes nothing; missing secret holds only its listener; a provider-key change is served over SotW; CA and client-certificate secrets, standalone SDS server, and Delta untested |
| Control-plane and proxy restart | Directly characterized through SnapshotCache in both ADS modes, including a restart during rewarming (RF-028); TLC counterexample and three passing configurations in `ReconnectWhileWarming.tla`; live XdsWarming restart-during-warming test passed once | Empty cache is harmless while the proxy keeps serving; reconnect carries versions without nonces; a restarted proxy is served from the warm cache; a reconnect while a cluster is warming re-requests EDS, LDS, RDS but not CDS until EDS completes the warming, so a withheld or equal-version-parked EDS can block CDS repairs until EDS completion or initial-fetch expiry (nominally 15s on generated EDS, where cached EDS can complete warming; no hard scheduling bound, and disabled timers permit indefinite waiting); the kgateway first-publish gate for a cache-less client is not exercised |
| Initial fetch timeouts | Pinned source/rendering, fifteen timed TLC configurations, seven abstract Lean safety theorems and source-specific direct runtime histories (RF-032); historical synthetic 2s probe retained | Parent CDS/LDS timers are disabled; local EDS uses 1s; default EDS/RDS expiry can initialize degraded state or use cached EDS. Readiness is not completeness, cache is not fresh receipt, and nominal durations do not prove a global deadline; runtime replacement/error/rejection coverage and refinement remain open |
| Envoy initialization/traffic | Directly characterized for two pinned profiles plus the references scenario | Empty EDS initializes; panic changes selection; dangling RDS is per-route 503; NACK applies valid siblings (RF-026); broader ENV-A1 remains open |
| Enterprise standard/FIPS runtime | Direct observations on both pinned v1.39.1-patch1 images: legacy warming/references/secrets/restart plus live publication and source-specific timeout histories, including one and two workers | Finite arm64 Docker observations; no full downstream controller/peering/enterprise extension integration, standalone SDS, or certification claim |
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

## Earlier October 16 publication-policy verification

The following receipt belongs to the previous extension, subsequently committed
as `12ec187d6042`. It is preserved separately from the lifecycle extension below.

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
bump, downstream source change, commit, or deployment was part of that update.

## October 16 updated-stack and lifecycle extension

- **Abstract proof:** `PublicationSelection.lean` compiles selection/provenance
  contracts and the conditional equality obligation. It adds no Go refinement
  or collision-freedom claim.
- **Bounded temporal evidence:** all 50 required TLC configurations matched
  their expected outcomes. The new fixed watch model explored 13 states and the
  fixed generation model 16; six new controls reproduce the intended safety or
  temporal failures. Original broad ADS bounds remain deferred.
- **Implementation evidence:** the updated stack runner reproduced eleven exact
  assertion failures after independently recording its two pristine compilation
  failures; production Go digests remained unchanged. The downstream copy
  reproduced the constructor mismatch and all three missing client-state
  methods, with 64 top-level tests / 137 test events passing in selected packages.
- **Runtime evidence:** all twelve digest-pinned combinations passed (four
  scenarios each on upstream, enterprise standard and enterprise FIPS Envoy).
  Required phase receipts, admin dumps, HTTP/TLS observations, image architecture
  and hashes are retained. These run the direct probe, not a kgw-ent controller.
- **Trace evidence:** `make formal-lean` requires four fresh cache/server traces
  in addition to the 27 existing snapshot scenarios; the checker rejects
  incomplete or uncorrelated observations. Snapshot-to-lifecycle joining and
  independent application correlation are still open.
- **Validation:** the final Lean gate passed 50 executable model expectations,
  325 top-level Go tests / 597 test events across ten packages, 27 snapshot
  scenarios and all four lifecycle traces. The selected lifecycle race run
  passed 25 test events without a reported race. Eight regression-harness tests,
  one runtime-receipt mutation test, Go analysis and workflow lint also passed.

Commands, exact identities and receipt hashes for this extension are summarized
in [the extension receipt](receipts/2026-10-16-lifecycle-extension.json). Public CI requires the updated
stack baseline, lifecycle receipts and upstream runtime profile. Private
downstream/enterprise profiles run explicitly; they are not silently skipped or
claimed covered by public CI. No product dependency, deployment or Git state was
changed by this extension.
