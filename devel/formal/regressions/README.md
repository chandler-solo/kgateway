# Pinned xDS regression profiles

October 17: `make formal-stack-8e7546` requires all sixteen historical desired
properties and the author's native reset/adoption exclusion property to pass at
`8e75468f4d0e055deb9b8f34e4254831f011e2a3`. It preserves all older profiles and
fixture bytes. Two exact-hash fixture adapters mirror production's current-row
getter and install/assert the initial row before the queued history starts.
They run after fixture injection and do not change production files or weaken
assertions. Runtime evidence is separately supplied by
[`make formal-live-publication`](../live/README.md).

These checks keep the research implementation, reviewed stack, and downstream
adoption obligations separate. `profiles.json` names immutable source revisions,
expected test outcomes, and assertion signatures. Downloads use read-only GitHub
archive requests; all source writes and tests run in fresh temporary directories.
The runner does not change Git state or the supplied candidate tree.

```sh
make formal-stack-regressions
make formal-stack-current
make formal-legacy-contents
```

The historical stack command requires the nine reviewed property tests to execute and fail
with their specific assertion messages at stack tip `e7b569a1fb78`. It separately
requires the known setup-package constructor compilation failure. A compile
error cannot count as executing a property test. The legacy command runs the original
warm contents-gate starvation probe on research `a4cf59d6d803`; that test passes
only when the historical defect reproduces. Its source fixture is preserved
verbatim. The active research tree instead tests derived-empty publication.

`formal-stack-current` adds the immutable October 16 revision `215ebe8e6450`;
it does not move the older profile. It first requires both pristine compilation
failures: the obsolete `Unresolvable` test field and removed NACK constructor.
Only then does it apply two exact-hash-guarded test API adapters in the scratch
copy, require both packages to compile, and run eleven desired-property tests.
The extra properties cover error-only equality suppression and retained-cache
exposure to a new connection while its candidate is held. Production Go file
digests must remain unchanged. All original assertions remain; fixture adapters
only use the new episode/derivation APIs. Green means eleven specific failures
reproduced, not that the stack is correct.

`gh`, Go, Make, and Python with `tarfile` data-filter support are required.
The runner records profile/fixture hashes, Go JSON execution events, logs and a
terminal receipt. It rejects missing tests, skips, duplicate runs, panics and
failures at the wrong assertions. Harness negative tests run before either
profile. The CI job uploads receipts, not complete copied source trees.

For a future stack fix, require the same properties to pass:

```sh
make formal-stack-regressions \
  FORMAL_REGRESSION_ARGS='--expect-fixed --source /absolute/path/to/candidate'
```

This copies the candidate before injecting tests. Its receipt labels the subject
as a candidate copy, not the immutable baseline. An explicit `--artifacts` path
must be fresh; existing source directories are never overwritten or cleaned.
The fixtures call the pinned implementation's internal timer callbacks. If a
candidate changes those interfaces or adds clock checks, adapt the fixture's
clock/entry-point adapter while preserving the tested properties.
Use `formal-stack-current` for the new fixture API. Candidate mode requires
pristine compilation and never applies the baseline's diagnostic test adapters.

## Later October 16 revision: 1e417af796a2

The [revised review](../reviews/2026-10-16-stack-14817-1e417.md) has a separate
immutable profile. `formal-stack-current` retains its historical meaning and
does not silently follow the moving PR head.

```sh
make formal-stack-1e417
make formal-stack-1e417 \
  FORMAL_REGRESSION_ARGS='--expect-fixed --source /absolute/path/to/candidate'
make formal-downstream-adoption \
  FORMAL_ADOPTION_ARGS='--downstream /absolute/path/to/reviewed/kgw-ent --stack-profile stack-14817-1e417af796a2'
```

The immutable run requires pristine proxy-syncer/setup compilation, ten specific
passes and four designated property failures. Candidate mode requires all
fourteen properties to pass. The receipt checker validates each outcome, not
just the package exit code. No production Go files or existing upstream tests
are patched. New fixture copies adapt the removed wrapper reference field and
the prune callback's state/epoch parameters; the older fixtures remain intact.

Nine previous desired properties now pass. The global claim leak still fails.
The older raw-cache reconnect fixture bypasses the new admission hooks, so it
is not selected for the new profile: a passing serial hook scenario and a
failing concurrent clear/publication scenario replace that coverage. Two other
new failures exercise an actual prune timer during a reference-ahead hold and
carry expiry without a subsequent candidate. The admission probe releases its
suspended publisher even if a corrected candidate serializes admission behind
it; success does not require observing an erroneous early clear.

The new downstream profile preserves the constructor failure and adds the
`SetConnectionStartHooks` contract to the three existing missing methods. It
retains the same five-package suite, source digest checks and scratch-only
replacement. No interface assertion establishes correct callback wiring.

## Subsequent October 16 revision: 7d930bb2183b

The [current review](../reviews/2026-10-16-stack-14817-7d930.md) preserves another
immutable profile:

```sh
make formal-stack-7d930
make formal-stack-7d930 \
  FORMAL_REGRESSION_ARGS='--expect-fixed --source /absolute/path/to/candidate'
make formal-downstream-adoption \
  FORMAL_ADOPTION_ARGS='--downstream /absolute/path/to/reviewed/kgw-ent --stack-profile stack-14817-7d930bb2183b'
```

All fourteen previous properties must pass on this pin. Two new histories must
fail at their specific assertions: coherent recovery followed by another carry
episode, and a queued KRT callback resuming after connection admission has
replayed a newer deferred row. The latter uses real KRT dispatch, current-row
lookup, warm-stream admission callbacks and SnapshotCache. Candidate acceptance
requires all sixteen properties to pass. Production sources remain unchanged.

`CarryEpisodes` and `QueuedPublication` separately explore these mechanisms in
bounded temporal models. Their proposed repair modes are not Go refinements.
The [receipt](../receipts/2026-10-16-stack-7d930.json) retains the exact failures,
three race-enabled repetitions, 57-config TLC run and full research gate output.
The new stack's 1,105 existing race cases pass despite these two counterexamples.

## Evidence boundaries

The tests inspect Go transformations and SnapshotCache, with both real elapsed
timers and directly invoked callbacks in explicitly identified cases. They do
not establish wire order, ACK, application, traffic outcomes, universal race
freedom, or a temporal guarantee. The weighted-header and
opaque-extension cases exercise extension surfaces, not a demonstrated built-in
downstream outage. Scoping cases require REFERENCED mode. Held ancillary closure
and shared-CLA carry are broader composition issues.

The standalone research tree does not contain the stack's timers, scoping,
claims, reconnect wrapper, or reclaimer. Passing `make formal-lean` here cannot
certify those implementations. PublicationTransitions.tla separately models the
three timer schedules and a proposed fixed policy at bounded cache scope.

Downstream constructor/state integration has a separate executable negative
profile, with no writes to the supplied checkout:

```sh
make formal-downstream-adoption \
  FORMAL_ADOPTION_ARGS='--downstream /absolute/path/to/reviewed/kgw-ent'
```

It checks downstream HEAD `1bc4cde8b4ed`, records working-state/source hashes,
copies it, and appends a local dependency replacement pointing at the immutable
updated stack archive. It requires the constructor compile failure, runs every
top-level test in the five selected xDS/authentication, peering, selector,
gateway-parameter and SDS packages, then injects three compile-time contracts
for the missing client-state methods. The image Make target is used for the
constructor check with Docker/Rust outputs explicitly omitted; this is Go
compilation evidence, not an image build. Existing suite success does not
exercise the absent shared warm/cold stream semantics or prove state wiring.

Selected standard/FIPS direct runtime scenarios now have their own
[`runtime` profiles](../runtime/README.md); they do not validate downstream
controller adoption. Exact-pin dependency
probes remain in `devel/formal/gcpprobe`, including the repaired parked-watch
path and unrepaired immediate-decline negative control.

The required Lean gate also replays two minimized research traces:
`research-overlapping-decisions.jsonl` must pass, while
`research-toy-xor-collision.jsonl` must report `version-reuse`. Both were captured
in the October 16 combined run (sequence/scenario identifiers normalized).
The latter uses hand-assigned integer contributions, not a demonstrated
production hash collision; a matching Go counterexample preserves that limit.
