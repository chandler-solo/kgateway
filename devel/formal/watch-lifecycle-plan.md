# Plan to verify xDS watch lifecycle correctness

Date: October 21, 2026

Extend the existing cache oracle to model the NACK wrapper's individual cache
calls, watch ownership, publication locking, response forwarding and recurring
timer. Check the same histories against bounded models and the real pinned
cache/server. Preserve the earlier failures as required negative controls.

The first milestone is a bounded, implementation-linked assurance case for
SotW ADS watch handling. It covers the reported parking and retry races,
subscription changes, shared keys, cancellation and backpressure. Its claims
must name the source revision, dependency, configuration, bounds and progress
assumptions. Completing it does not establish correctness of all xDS protocols
or all Go executions.

## Execution status, October 21

The initial implementation is in `watch/` and `regressions/watch_lifecycle.py`.
Immutable historical subjects, the observability and claims heads, and a per-type
composition now have strict executable profiles. The first bounded model slice, local Lean ownership lemmas and
cross-model projection checks are implemented. Three historical race mechanisms
and named EDS/RDS contention have real cache/server coverage in both ADS modes.

This does not close the phases below. The models still lack simultaneous
streams/keys and unbuffered-channel schedules; the Go harness still needs the
full cancellation/replacement/error matrix and production mutations. Internal
watch/timer event replay, generated Go histories, CI source distribution and
new pinned live Envoy validation remain pending. See `watch/README.md` for exact
subjects, validation and limits.

## Existing foundations

The research baseline inspected for this plan is
`f041d3e6288cc52509b0690da461d427327bffb0`. It is a separate subject from the
proposed production stack and any deployed controller.

| Existing component | Reuse | Boundary to extend |
| --- | --- | --- |
| `lean/XdsSpec/CacheOracle.lean` | Pinned admission and publication rules, subscription history, returned resources, cancellation and cache restart | Request/publication events are coarse; the stack's NACK wrapper and timer engine are outside its profile |
| `lean/XdsSpec/GcpWatch.lean` | Historical loss examples and waiter accounting | Small historical abstraction, not the current composed wrapper |
| `tla/WatchDeliveryComposition.tla` | Separate installation, queueing, send, ACK and application; explicit fairness and negative controls | No wrapper program counters, publication lock or recurring retry lifecycle |
| `oracle/check.py` and `publication_cache_oracle_test.go` | Lean process protocol, generated histories, observation comparison and failure receipts | Research implementation profile, not the stack's private setup policy |
| `regressions/check.py`, `profiles.json` and fixtures | Immutable subjects, isolated source copies, exact failure expectations | Add focused watch lifecycle profiles and adapters |
| `lifecycle/trace.go` and `gcpprobe/lifecycle_test.go` | Strict correlation, terminal receipts and real dependency observations | No full watch/timer ownership replay or internal queue instrumentation |

Preserve these profiles and their counterexamples. Add a new wrapper profile;
do not silently change the historical oracle to describe the proposed stack.

## Phase 0 Freeze subjects and preserve the reproductions

**Deliverable:** an immutable profile matrix and stable copies of the review
counterexamples, runnable without changing any existing checkout's Git state.

1. Add profile entries to `devel/formal/regressions/profiles.json`. Resolve and
   record full revisions, source hashes, `go.mod`/`go.sum`, dependency hash,
   constructor names and relevant settings. Start with these subjects:

   | Subject | Revision | Required characterization |
   | --- | --- | --- |
   | Earlier retry fix | `b4d3a09afa389d80f93317e6654e7b6eee6d9492` | Retry check/replacement interleaving and rejected-version return remain negative controls |
   | Serialized recurring retry | `ceaf17029d07428f258b0811db68aa3f973e1cac` | Earlier timer cases pass; initial parking interleaving remains a negative control |
   | Initial parking fix | `2103c02b8e975dedc372187768d4b9de0e64b6da` | All three reported interleavings pass; characterize contention separately |
   | Per-type composition | PR #14865 `4635e171b1162672a7c0f55d1dc5d31c162e40e1`, plus an explicitly pinned compatible stack tip | Answerability differs from the plain ADS profile; composition requires its own results |

2. Copy the timer-publication, version-return and initial-parking probes from
   the external review artifacts into hashed `.go.txt` fixtures under
   `devel/formal/regressions/fixtures/`. Make fixtures self-contained or declare
   their exact helper dependencies. Keep the Node-copy race test too. Do not
   make temporary review directories a dependency of the finished runner.
3. Add `regressions/watch_lifecycle.py` and runner unit tests. Reuse the existing
   source-copy machinery. Limit adapters to test wiring such as
   `newNackResendSuppressor` versus `newWatchPolicy` and watch-count helpers;
   require exact input hashes. Production behavior must remain the subject's.
4. Resolve the complete stack tip containing the accepted fix when this phase
   starts. Keep the lower-PR and full-stack subjects distinct.
5. Write raw logs, binaries, JSON test output and generated captures only to a
   fresh external artifact directory. Track source fixtures, schemas, minimized
   counterexamples and expectation manifests, not execution receipts.

**Exit gate:** the runner recognizes the exact intended assertions in both ADS
modes, rejects missing/skipped tests and unrelated failures, and reproduces the
historical/fixed matrix without checkout, index, ref or GitHub mutations. A
compile error, tool failure, generic timeout or teardown failure does not count
as reproduction of a watch-loss property.

## Phase 1 Specify the state and observable contract

**Deliverable:** `devel/formal/watch/README.md`, `watch/schema.json`,
`watch/scenarios/*.json`, property definitions and an implementation-to-model
operation map. Scenario expectations refer to the immutable subject IDs in
`regressions/profiles.json`.

Keep descriptive behavior and desired guarantees separate. The descriptive
cache profile must preserve `1cd1226616f5`'s asymmetry: a declined new named ADS
request registers no watch; a declined parked watch survives. The non-ADS
per-type profile answers the subscribed names. Agreement with either reference
is not, by itself, satisfaction of the stronger delivery requirements.

Represent these state components explicitly:

| Component | Required state |
| --- | --- |
| Client interest | Client key, resource type, current request/subscription generation, names, wildcard history and returned-resource history |
| Stream | Unique generation, open/closed state, per-type sent version, reported accepted version and nonce association |
| Cache | Installed resources and opaque version for each key/type; cache replacement distinct from `ClearSnapshot` |
| Wrapper operation | Actor, phase, captured snapshot/version, owned request copy, rejected version and suppression decision |
| Watch attempt | Unique attempt identity, owner generation, registered/answered/declined/cancelled disposition and cancellation capability |
| Publication | Lock ownership, install phase, fan-out phase and API result |
| Retry | Owner request/stream, rejected version, delay class, armed/due/running/stopped state and replacement attempt |
| Response | Captured snapshot and subscription, owner generation, queue/forward/send state; ACK and application separate |

Use this scenario envelope, with required fields and unknown fields checked at
both the runner and Lean entry point:

```text
schema, scenarioId, subjectId, cacheProfile, adsMode, bounds
initialState
events[]: id, actor, kind, arguments, happensAfter[]
expectations[]: property, outcome, witnessOperationIds[]
```

Arguments carry the applicable key, stream/request/attempt/timer IDs, resource
type, names, request version, response nonce and gate token. An event is either
an external stimulus or an awaited observation; observations must come from Go,
not be fabricated from the requested schedule. Use outcome categories
`holds`, `violated` and `outside-profile`; runner/tool failures are a separate
execution result. A required scenario cannot pass as `outside-profile`.

Keep reported accepted version, last sent version and rejected version distinct.
A matching nonce identifies the rejected response; it does not imply the whole
response was applied or rolled back. Model delay classes from base to capped
backoff, with the actual reset rules for ACK and a different rejected version.

Versions are opaque identifiers, not monotonic counters. Include
`v1 -> v2 -> v1`, distinct versions with identical bytes, and a separately
identified version-collision negative control. Preserve RF-025's current
zero-marshal version contract. Do not require all input-only pushes to vanish.

Separate operations at the actual synchronization boundaries:

1. Receive/correlate request; cancel or supersede its previous watch.
2. Attempt publication-lock acquisition, including the real `TryLock` failure
   branch and its plain-cache fallback.
3. Read/capture the snapshot version.
4. Register a parked watch through the cache API.
5. Acquire the retry locks, inspect the current version and create a replacement.
6. Observe whether the replacement answered, parked or was declined; cancel the
   appropriate attempt and forward a response without retaining the locks.
7. Install a snapshot and fan out responses; permit partial failure after install.
8. Expire/re-arm/stop a timer; close a stream or supersede its request.

Treat the real cache's locked operations as atomic only where source inspection
justifies it. Do not combine the wrapper's read and registration merely because
they occur in one Go function. Record lock ordering and every potentially
blocking cache/channel operation. Model Go `select` without giving cancellation
an invented priority when both cases are ready.

Define named properties before coding the models:

- **Request accounting:** an open, current interest needing a response retains
  a registered watch, pending response, scheduled retry or identifiable in-flight
  owner. A silent decline cannot erase the client obligation. An in-flight phase
  is not a permanent exemption; require its conditional progress separately.
- **Generation ownership:** an older timer/cancel capability cannot cancel a
  newer watch or deliver into a replacement stream's channel.
- **Response provenance:** a response matches the snapshot and subscription
  captured for its attempt. It need not match a later snapshot at send time.
- **Retry persistence:** a retained, unanswered request does not lose its retry
  merely because the cache temporarily leaves the rejected version.
- **Timer causality:** a successfully parked request retries only through its
  owning due timer or another permitted protocol event. Check the contended
  plain-cache fallback separately; do not impose a parked-watch rate guarantee
  on a branch that deliberately did not park the request.
- **Subscription correctness:** unsubscribe-all differs from initial empty
  wildcard; newly requested resources may be answered at an unchanged version.
- **Installation separation:** failed fan-out does not roll back installed state;
  installation, forwarding, successful transport, ACK and application are distinct.
- **Conditional progress:** a current, uncancelled interest is eventually answered
  when an eligible correction stabilizes and the specified timer, lock, cache and
  transport actions receive fair execution. State each premise explicitly.

Known plain-ADS failures may violate a desired property and still match the
descriptive model. Record that outcome as a named negative control. Never
assume retained admission or successful `TryLock` in order to make a profile pass.

**Exit gate:** every property has a precise predicate, observation boundary,
premises, supported profiles and at least one violating fixture. Cancellation
does not falsely promise to retract a response already queued or sent.

## Phase 2 Build the bounded models and executable oracle

**Deliverable:** `tla/NackWatchLifecycle.tla`, classified `.cfg` files,
`lean/XdsSpec/WatchLifecycle.lean`, `lean/WatchOracleMain.lean`, and an
`xdswatchoracle` entry in `lean/lakefile.toml`.

1. Extend the executable state machine using `CacheOracle`'s subscription and
   answerability definitions. Add wrapper program counters, watch generations,
   locks and timer actions. Do not duplicate a simplified version-only guard.
2. Build the TLA+ scheduler model around the same operation boundaries. Preserve
   the old `GcpWatch` and `WatchDeliveryComposition` models. Use trace projection
   tests to check that shared fixtures have compatible meanings across models;
   do not call fixture agreement a proof of model equivalence.
3. Start with one key, one stream, resources A/B, three version identifiers and
   one response slot. Then check two streams sharing a key, a reconnect with a
   fresh generation, and two keys contending on the global publication lock.
   Add a separate zero-capacity channel configuration for the cache API profile.
4. Declare bounds in each configuration. Do not wrap generation identifiers:
   bound the number of admissions instead, retaining retired identities needed
   to recognize stale callbacks. Any symmetry reduction must preserve shared
   keys, old/new generations and the special unrequested resource.
5. Express safety invariants separately from temporal properties. Use weak
   fairness only where continuous enablement is justified. Model timer expiry
   as an explicit action; give it no wall-clock deadline. Do not assume `TryLock`
   eventually succeeds. Keep unfair timer/send executions as negative controls.
6. Check blocked-operation progress and lock cycles. Distinguish legitimate
   quiescence from deadlock; do not add an always-enabled no-op that conceals
   a stuck request. Apply appropriate TLC deadlock checking to each profile.
7. Add Lean proofs for local guard and ownership facts where they can be stated
   over arbitrary model inputs. Classify finite state exploration separately
   from kernel-checked inductive theorems and conditional temporal checks.

Required configuration families include historical retry replacement, one-shot
retry, unprotected initial parking, fixed locking, contended fallback, stale
generation cancellation, response backpressure and missing retry/send fairness.
Classify expected outcomes and exact violated properties in
`tla/check-required.sh`; unexpected tool exits must fail the gate.

**Exit gate:** the three historical mechanisms produce their named
counterexamples; fixed configurations satisfy the properties their cache profile
supports. A counterexample in the contended fallback is investigated and retained,
not removed by strengthening assumptions. Record completed state/transition counts
and bounds. A state/time limit is incomplete, not a successful exhaustive result.

## Phase 3 Drive the real cache and server through the same schedules

**Deliverable:** hashed setup-package fixtures and a deterministic runner in
`regressions/watch_lifecycle.py`, exercising both ADS modes at the frozen subjects.

The research root has no equivalent production NACK wrapper. Inject test fixtures
into isolated copies of each subject's `pkg/kgateway/setup` so they call the real
private policy and constructor. Do not copy the production algorithm into the
harness. Reuse existing `newGatedNackHarness`/cache decorators where present and
provide exact-hash test adapters where constructor names differ.

Use channel barriers around real cache calls and publication completion. Prepare
fixtures on the test goroutine before launching actors. Join every producer on
cleanup. Bound waiting to detect harness failure; do not use sleeps as evidence
that an operation happened or a timer was armed. Permit either ordering when a
correct implementation serializes the formerly failing interleaving.

Start with real Go timers and shortened test durations, observing their cache-call
boundaries. Do not directly invoke an internal timer callback as a substitute for
its scheduling. If later throughput requires an injected clock, make it a separate
instrumented profile and compare its boundary behavior with the real-timer suite
before using its results as evidence.

Run this scenario matrix:

| Scenario | Required observation |
| --- | --- |
| Publication between retry read and replacement | A later answerable snapshot still produces a response; distinguish retained watch from lost watch |
| Rejected version returns after a blocked detour | The recurring retry resumes; no additional client request is supplied |
| Publication between initial read and parking | The initial watch is retained/answered under the repaired synchronization |
| Busy publication lock, named EDS and RDS | Characterize fallback with both compatible and incompatible snapshots; wildcard CDS alone is insufficient |
| Cancel before/during/after retry expiry | No cancellation of the successor watch; all actors finish after stream closure |
| Replacement answered, parked, declined or errored | Keep correct ownership and retry state for each outcome |
| Same key with two streams, including warm/cold members | No cross-stream nonce attribution or cancellation |
| Close/reconnect while a response is queued | Old callbacks never target the new stream's channel; already queued work is accounted for |
| Another key while publication is busy | Observe the actual shared-lock/cache coupling; do not assume isolated clients |
| Channel ready, full or blocked | Forwarding/cancellation behavior matches the model and teardown completes under its stated drain/close assumptions |
| Partial fan-out failure and clear/reinstall | Record installation independently from response success and distinguish API-only clear schedules from production reachability |
| Shared mutable Node metadata | The request-copy mutation causes the intended race report; the fixed subject remains race-free |

Observe actual responses and watch state after intermediate operations and after
the final eligible publication. A cache readback alone cannot satisfy a delivery
assertion. Returned cancellation functions are opaque capabilities: a non-nil
function can be a no-op and does not prove a watch was registered.

Use independent mutations in external overlays: remove only retry serialization,
remove only initial-parking serialization, make retry one-shot, restore
cancel-before-successful-replacement, remove the request clone, and replace
`TryLock` with blocking acquisition. Each must fail its designated property or
race detector check; an unrelated build error is insufficient. Failure of a
new intended property on the unmodified subject is a finding, not a harness
mutation success.

**Exit gate:** model-predicted schedules have matching Go observations and the
expected historical results. Real-timer, cancellation and backpressure probes
complete under `-race` with automatic retries disabled. Preserve API reachability
and actual server reachability as separate evidence labels.

## Phase 4 Add lifecycle conformance and generated histories

**Deliverable:** a new strict watch trace profile, differential replay and
minimized fixtures connected to `xdswatchoracle`.

1. Add a separate versioned watch schema under `devel/formal/watch/`, reusing
   strict parsing and terminal-count patterns from `lifecycle/trace.go`. Keep
   schema 1 lifecycle traces readable under their original checker; do not
   silently reinterpret old records.
2. Record operation/actor IDs, stream and request generations, attempt ownership,
   captured versions, lock attempts/results, timer identity/state, cancellation,
   installation readback and response-channel observations. Correlate callback
   attempts, successful mock transport and ACK/NACK by type and nonce separately.
   Record no secret bodies, credentials or raw Node metadata.
3. Assign observation IDs to attempts and cancellation capabilities. Controlled
   watch counts are enough only when the scenario makes ownership unambiguous.
   Where concurrent registration/cancellation cannot be observed externally,
   use exact-source event-only dependency overlays in isolated copies. Record
   their hashes and compare observable outcomes with the unmodified dependency.
   Do not invent internal watch IDs from opaque callbacks.
4. Preserve invocation/return, lock and channel happens-before edges. A recorder's
   total sequence across goroutines does not by itself establish causal order.
   Where an abstract transition is not directly observable, bounded replay may
   search for a legal model execution consistent with the observations. Label
   that result trace consistency, not verified refinement or instrumentation
   completeness. Reject unbounded stuttering as an explanation for lost work.
5. Add negative checker tests for missing registration/terminal events, reused
   identities, stale timer effects, fabricated sends, wrong nonces and generation
   mismatches. Application events require independent application instrumentation.
6. Generate bounded histories using the same schema: publish, request, NACK,
   subscription change, close/reconnect, wait for timer boundary, release a
   barrier, drain transport and cancel. Use valid dependency-aware generation
   alongside explicitly classified invalid histories. Never derive expected
   outcomes from the actual Go decision.
7. Minimize discrepancies while preserving the concurrency ordering and the
   property violation. Classify Go, model, adapter and checker defects before
   changing expectations. Preserve all earlier minimized failures.

**Exit gate:** Go and Lean agree on the frozen valid corpus, independent
properties have their expected outcomes, and checker/comparator corruptions are
rejected. Generated-case receipts prove execution beyond baseline corpus replay.
Finite successful traces are not evidence of eventual progress on every run.

## Phase 5 Integrate focused gates and upstream composition

**Deliverable:** Make targets, required outcome manifests, CI selection and an
updated assurance table.

Add proposed targets `formal-watch-models`, `formal-watch-regressions`,
`formal-watch-oracle` and aggregate `formal-watch`. Extend existing required
Lean/TLC and receipt gates rather than creating a permissive parallel runner.
Use Make targets for all Go and lint commands; include the `e2e` tag when
including all source. New target names are implementation work, not commands
available in the current tree.

Give the new watch runner an explicit profile selector and artifact directory.
Add focused model selectors to the existing required runners without changing
their full-run defaults. Use `regressions/watch_lifecycle.py` for subject
construction and `watch/check.py` for model/oracle orchestration; both consume
the same schema and outcome manifest. Plan execution once per aggregate target
so selecting the model, regression and oracle stages does not run the same Go
suite three times.

Use these execution tiers:

| Tier | Work |
| --- | --- |
| Relevant PR changes | Small exhaustive model configurations; fixed historical fixtures in both ADS modes; focused race and checker tests |
| Acceptance of a new source profile | One full setup package race run and required composed-package checks; retain each earlier profile's focused counterexamples |
| Scheduled/extended gate | Larger two-key/two-stream bounds, seeded generated histories, bounded fuzzing and instrumented/unmodified cross-checks |
| Changes to scoping/publication/identity | The existing five-package composition suite and relevant current formal gates |

Do not run every package twice under ALL and REFERENCED. Keep watch-policy
tests shared; add targeted scoping transition cases where scoping changes the
published resource set. Run both ADS modes because their server scheduling
differs. Broaden repetition only for an actual change, failure or unresolved
scheduling concern.

Update `assurance-status.md`, `protocol-scope.yaml`, the applicable assumption
entries in `devel/testing/formal-assumptions.yaml`, and the findings ledger with
the actual property, profile, evidence and remaining obligation. Inspect the
current ledger before assigning a new RF identifier. Keep historical results
dated and immutable.

**Exit gate:** missing tools, skips, incomplete model exploration, wrong negative
failures, stale artifacts, malformed traces and insufficient generated execution
all fail closed. Keep code, specifications, fixtures and manifests as versioned
source; execution artifacts stay external. Implementation does not include Git
commits, ref changes, pushes or
GitHub writes without the user's separate explicit instruction.

## Phase 6 Validate Envoy boundaries and state the remaining proof obligation

**Deliverable:** a small pinned live profile and an assurance case that separates
abstract properties, model exploration, Go correspondence and runtime observations.

Reuse the existing direct Envoy and live-publication infrastructure. Render the
subject's actual bootstrap and keep source-specific initial-fetch timeouts in
the profile. Use an isolated test environment through the repository framework;
do not change an existing cluster's Helm-managed resources by hand.

Exercise actual Envoy NACKs, corrections, endpoint/route changes while a parent
resource is rejected, and reconnect with held versions. Preserve plain-ADS
negative results separately from the per-type-cache composition. Include only
timer/parking schedules that the observed Envoy requests can actually reach;
otherwise retain their Go API or scripted-server evidence label.

Capture wire responses, proxy identity, immutable image digest, relevant admin
state and traffic observations. Callback-before-send, successful cache enqueue,
ACK, warmed resources and traffic success are separate observations. Do not
require derived empty endpoints to become nonempty or successful traffic to
claim correct empty truth.

Finish an operation map from each modeled transition to the Go synchronization
and observation points. List the remaining assumptions: cache lock behavior,
timer execution, lock acquisition, channel draining, stream processing and input
stability. A general refinement proof must show that every allowed Go execution
maps to allowed model steps and preserves progress assumptions. Differential
agreement and finite trace consistency do not discharge that obligation.

**Exit gate:** every reported guarantee names its evidence level and premises.
Infrastructure absence leaves this phase incomplete; it does not invalidate
completed bounded/Go work or count as a live pass. Report quantitative deadlines
only after separate bounds on scheduling and transport have been justified.

## Completion criteria and implementation order

Prepare reviewable changes in this order: profile fixtures and runner;
state/schema/property contract; bounded models and executable oracle; real Go
schedule replay; trace and generated-history checks; CI/upstream composition;
live boundary validation and the final assurance update. Each change depends
on the preceding phase's exit gate and preserves existing negative controls.

The first milestone is complete when phases 0 through 5 pass for the explicitly
listed profiles and bounds. Phase 6 adds the live assurance boundary. Neither
milestone establishes general Go refinement or complete xDS-system correctness.
Any newly exposed property failure must be resolved or remain an explicit open
finding for that profile; matching a descriptive oracle cannot turn it into a
passing correctness claim.
