# Watch lifecycle assurance

This work follows [the phased plan](../watch-lifecycle-plan.md). The research
branch is the harness, not the production watch policy. Tests run against
immutable subjects extracted from local Git objects into external directories.
No source checkout, index or ref is changed by the runner.

## Frozen implementation profiles

The source and fixture hashes are in
[`regressions/profiles.json`](../regressions/profiles.json). The observability-layer
subject is `84ea7c6669dffe3642a8613b0dd7049a06f08d16`, containing the accepted
initial-parking fix; it is distinct from that fix's lower-PR subject.

| Profile | Initial parking | Retry replacement | Rejected version returns |
| --- | --- | --- | --- |
| `watch-early` (`b4d3a09afa`) | Violation | Violation | Violation |
| `watch-retry` (`ceaf17029d`) | Violation | Pass | Pass |
| `watch-parking` (`2103c02b8e`) | Pass | Pass | Pass |
| `watch-stack` (`84ea7c6669`) | Pass | Pass | Pass |

These are **Go/cache/server observations**, reproduced October 21, 2026 in
both default and ordered ADS, under the race detector with automatic retries
disabled. Each profile also runs the source's Node-copy regression. The two
fixed subjects passed their full setup packages: 126 and 162 test/subtest
execution events respectively, including the injected fixtures. Counts include
parent tests; they are not counts of independent scenarios.

All subjects use go-control-plane
`v0.14.1-0.20260702184136-1cd1226616f5`. Its new incompatible named ADS admission
registers no watch, whereas an incompatible parked watch survives publication.
The suite preserves that distinction. These results do not cover a per-type
cache composition, actual Envoy or all concurrent schedules.

## Running the frozen regressions

From the research root:

```sh
make formal-watch-regressions FORMAL_WATCH_ARGS='--profile watch-stack --repository /path/to/kgateway-object-store --full-setup'
```

Use any local kgateway repository containing the full immutable commit. The
runner reads committed objects, ignoring its working tree and branch position.
`--artifacts` can name an empty directory outside both repositories; otherwise
the runner creates one. It records source/dependency hashes, tool version,
commands, logs and JSON execution records there. None are versioned receipts.

The test-only constructor adapter for the complete stack is exact-hash guarded.
The runner hashes production Go files before and after replay. No production
algorithm is copied into the harness or changed to make a subject pass.

A successful characterization run means every expected outcome occurred. It
can include intentionally failing historical tests. Both ADS descendants must
run exactly once. Missing/skipped/duplicate execution, a wrong assertion, build
failure, panic, race report or harness failure rejects the run.

## Observation limits

The cache decorator pauses after an actual snapshot read. Publication may
complete during that pause, or serialize behind the wrapper lock. A bounded
100 ms scheduling window permits both orders; its expiry is not evidence of
lock ownership. The eventual assertions inspect watch state and actual mock
transport responses. The version-return scenario first observes a timer read
of the incompatible version and verifies that a retained watch still answers
a later distinct version if retry recovery fails.

The two-second retry observation is a bounded test budget, not a temporal
liveness proof or an operational latency promise. A non-nil cancellation
function is not evidence of a registered watch. A cache installation does not
prove response delivery, and this harness does not infer Envoy application
from ACK or from a transport send.

## Executable contract and bounded models

`schema.json` defines version 1 of the new watch scenario envelope. The older
lifecycle schema 1 is unchanged. `contract.py` and the Lean entry point reject
missing/unknown fields, duplicate operation identities, unsupported bounds,
invalid causal edges and vacuous expectations. A required scenario cannot
pass as `outside-profile`. Its requested outcome is compared with a separately
computed model outcome.

The first executable slice starts **after a correlated NACK of version `1`**:
accepted version is empty, rejected version is `1`, subscription and returned
history contain `A`, and the cache contains `A` at version `1`. Initial phase
`waiting` additionally seeds a parked watch and armed timer. The initial state
is a declared precondition, not evidence that every real client reaches it.
`reconnect` creates another such correlated interest with a fresh generation;
it does not model the complete stream callback and nonce-correlation protocol.

| State/operation | Code boundary | Current evidence |
| --- | --- | --- |
| `initial-read`, `initial-park` | Policy `CreateWatch`: `TryLock`, `GetSnapshot`, inner `CreateWatch` | Frozen Go probes; split Lean/TLA steps |
| `publish-begin`, `install`, `fanout` | Policy publication mutex; inner cache installation and watch responses | Model operations; existing cache probes support installation/fan-out distinction |
| `expire`, `retry-begin`, `retry-read` | Real timer, publication lock, watch lock, snapshot capture | Frozen timer probes; abstract timer fairness is explicit |
| `retry-cancel`, `retry-replace` | Historical cancellation-before-replacement versus replacement-before-cancellation | Historical negative and fixed Go profiles |
| `forward`, `send` | Private response channel, forwarding goroutine, mock stream transport | Separate model states; current Go probes observe successful mock sends |
| `cancel`, `reconnect` | Cancellation capability and fresh interest generation | Local Lean ownership lemmas and finite histories; broader Go schedules remain pending |
| `ack`, `apply` | Reported acceptance versus independent proxy application | Abstract separation only; no application instrumentation in this slice |

The Lean oracle reuses `CacheOracle.admit`, including returned-resource history
and the incompatible-new-request rule. Named RDS is normalized to EDS because
these admission rules coincide; full-state CDS/LDS, TTLs, glob subscriptions and
arbitrary subscription evolution are outside this slice. The `per-type` option
is a model of projecting named resources before admission. It is **not** an
implementation validation of the PR #14865 composition.

`NackWatchLifecycle` explores one key/type/stream, names A/B, three opaque
versions, and one slot each for the retry response and downstream channel.
Versions are never ordered by freshness. TLA+ separates acquisition, snapshot
capture, installation, fan-out, cancellation, replacement and forwarding.
The Lean slice additionally checks two non-reused generations in finite
histories. Two simultaneous streams/keys and zero-capacity channels are still
pending; the model must not be cited as covering them.

The current property predicates are:

- `request-accounting`: a live interest has a watch, response, timer, or
  in-flight owner. This does not prove the owner eventually makes progress.
- `parked-watch-retained`: at the named witness point, a watch or response
  remains. This stronger, scenario-specific check exposes initial parking loss.
- `retry-persistence`: a waiting, unanswered, retained watch keeps its timer.
- `generation-ownership`: a registered watch belongs to the current generation.
  The local theorem separately proves that cancelling a different owner does
  not change that watch; cancellation does not retract an existing queue entry.
- `EventuallyAnswered` (TLA+): **if an answerable final correction is installed
  and stays installed**, a response is eventually sent under the declared
  operation/timer/forward/send fairness assumptions. No wall-clock bound follows.
- `installation-means-send` and `ack-means-application` are deliberately false
  stronger claims, retained as negative controls.

A separate `LockStarvation` configuration disproves unconditional publication
completion under weak fairness: the lock can be repeatedly unavailable at the
publisher's acquisition opportunities. The model does not assume eventual
`TryLock` success. This abstract schedule is not a production starvation finding.
Terminal unanswered executions are checked as temporal failures with TLC's
raw deadlock check disabled; there is no always-enabled idle action that hides
pending work.

`WatchTraceReplay` applies the TLA+ actions to eleven shared Lean histories and
compares every projected state. It requires complete replay, so a blocked
operation cannot silently truncate a history. A corrupted projection must fail
`OracleProjection`, not a parser/tool check. Fixture agreement is not a proof
of model equivalence or of refinement from Go.

Run `make formal-watch-models` for the contract checks, classified TLC outcomes,
Lean corpus, malformed-input controls and cross-model projections.
`make formal-watch-oracle` runs only the Lean corpus and contract checks.
`make formal-watch` adds one selected Go profile; provide `FORMAL_WATCH_ARGS`
as shown above. The aggregate does not rerun the Go suite for each model stage.

## Remaining implementation gates

This is the first bounded slice of phases 0–2, plus the historical real-cache
schedules. It does not complete the plan's first milestone. Remaining work is
explicit: freeze and execute per-type composition; expand to
simultaneous streams/keys and unbuffered channels; instrument actual watch and
timer ownership; add partial-order Go trace replay and generated Go histories;
exercise cancellation, backpressure, clear/reinstall and production mutations;
integrate CI selection; and run pinned live Envoy scenarios. The existing
counterexamples and older assurance profiles remain unchanged.
