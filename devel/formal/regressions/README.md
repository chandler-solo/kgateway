# Pinned xDS regression profiles

These checks keep the research implementation, reviewed stack, and downstream
adoption obligations separate. `profiles.json` names immutable source revisions,
expected test outcomes, and assertion signatures. Downloads use read-only GitHub
archive requests; all source writes and tests run in fresh temporary directories.
The runner does not change Git state or the supplied candidate tree.

```sh
make formal-stack-regressions
make formal-legacy-contents
```

The first command requires the nine reviewed property tests to execute and fail
with their specific assertion messages at stack tip `e7b569a1fb78`. It separately
requires the known setup-package constructor compilation failure. A compile
error cannot count as executing a property test. The second runs the original
warm contents-gate starvation probe on research `a4cf59d6d803`; that test passes
only when the historical defect reproduces. Its source fixture is preserved
verbatim. The active research tree instead tests derived-empty publication.

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

## Evidence boundaries

The tests inspect Go transformations and SnapshotCache, with timer callbacks
driven deterministically. They do not establish wire order, ACK, application,
traffic outcomes, race freedom, or a temporal guarantee. The weighted-header and
opaque-extension cases exercise extension surfaces, not a demonstrated built-in
downstream outage. Scoping cases require REFERENCED mode. Held ancillary closure
and shared-CLA carry are broader composition issues.

The standalone research tree does not contain the stack's timers, scoping,
claims, reconnect wrapper, or reclaimer. Passing `make formal-lean` here cannot
certify those implementations. PublicationTransitions.tla separately models the
three timer schedules and a proposed fixed policy at bounded cache scope.

Downstream constructor/state integration and standard/FIPS Envoy validation
remain explicit adoption obligations in the profile manifest and research plan;
this harness does not report them as executed coverage. Exact-pin dependency
probes remain in `devel/formal/gcpprobe`, including the repaired parked-watch
path and unrepaired immediate-decline negative control.

The required Lean gate also replays two minimized research traces:
`research-overlapping-decisions.jsonl` must pass, while
`research-toy-xor-collision.jsonl` must report `version-reuse`. Both were captured
in the October 16 combined run (sequence/scenario identifiers normalized).
The latter uses hand-assigned integer contributions, not a demonstrated
production hash collision; a matching Go counterexample preserves that limit.
