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
