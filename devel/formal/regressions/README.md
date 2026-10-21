# Pinned xDS regression profiles

## October 20 per-type cache composition (PR #14865)

```sh
make formal-stack-pertype \
  FORMAL_PERTYPE_ARGS='--kgateway /path/to/kgateway-clone --artifacts /tmp/pertype-new'
# The three pinned profiles at the stack tip alone (also run in CI):
make formal-stack-c38563 FORMAL_C38563_DIR=/tmp/c38563-new
```

The clone must contain PR #14865's head `4635e171b116`, the stack's base
`93002e65c9fa` and its tip `c38563730069`; it is only read. The runner exports
the PR's tree and overlays every file the stack changed with `diff3 -m` against
the stack's base, refusing any conflict. It applies one exact-hash test adapter
to the stack's `openWatches` helper, injects the PR's six rejected-resource
scenarios driven through `newPolicyHarness`
(`fixtures/pertype_policy_composed_test.go.txt`) and runs the five-package race
suite. Every package must pass with no failing test, and the PR's scenarios,
its two between-installs tests and the policy-composed scenarios must each pass
in both ADS modes. It then runs the three profiles below on the composed
subject with `--expect-fixed`, and checks that production sources and harness
files are unchanged.

| Profile | Subject | Required outcome |
|---|---|---|
| `stack-14817-c38563-backend-closure` | Stack tip `c38563730069` | All nine backend-closure properties pass; one fixture adapter for the traversal's added return value |
| `stack-14817-c38563-histories` | Same tip | All seventeen earlier carry/reconnect properties pass |
| `stack-14817-c38563-rewarming` | Same tip | All six held-version rewarming properties pass |

CI runs `formal-stack-c38563`; the composition needs a clone holding the PR and
stack commits and runs locally. The composed subject is not a published
revision, and its Go tests use a mock ADS stream rather than Envoy. See
[RF-035](../research-findings.md#rf-035-pr-14865s-per-type-snapshot-cache-composed-with-stack-14817).

## October 20 backend-owned resource regressions

```sh
make formal-stack-backend-closure \
  FORMAL_BACKEND_CLOSURE_ARGS='--artifacts /tmp/backend-closure-new'
# Public CI omits the private enterprise Envoy image explicitly:
make formal-stack-backend-closure \
  FORMAL_BACKEND_CLOSURE_ARGS='--no-live --artifacts /tmp/backend-closure-ci-new'
# Require all current desired properties to pass on a proposed repair:
make formal-stack-backend-closure \
  FORMAL_BACKEND_CLOSURE_ARGS='--source /absolute/path/to/candidate --expect-fixed --artifacts /tmp/backend-closure-fixed-new'
```

Each artifact directory must be fresh and outside both the research repository
and any candidate. The candidate is copied before injection and remains
unchanged. This command runs five immutable profiles:

| Profile | Subject | Required outcome |
|---|---|---|
| `stack-14817-before-backend-listeners` | `350554e6f3c3` | Initial ALL emission passes; REFERENCED loses the internal listener's proxy and fails its designated assertion |
| `stack-14817-2fe5-backend-closure` | PR #14717, `2fe5bab3179d` | Two initial-emission tests and finite graph comparison pass; five carry-closure properties fail at designated assertions |
| `stack-14817-e9436e-backend-closure` | Stack tip `e9436e00b3ca` | Same three passes and five failures; a sixth failure requires backend-listener fallback to report scoping disabled |
| `stack-14817-e9436e-histories` | Same tip | All seventeen earlier carry/reconnect properties pass |
| `stack-14817-e9436e-rewarming` | Same tip | All six held-version rewarming properties pass |

The new fixture uses real KRT, `snapshotPerClient`, `NewProxyTranslator` and
SnapshotCache. The gateway still routes to `tunneled` when its translated backend
row disappears; it is neither errored nor carry-expired. The publisher carries
the old cluster but loses its owned listener and header secret. REFERENCED also
loses `proxy`, which only the owned listener names. Separate top-level tests
identify each lost dependency. The metric test retains all translated clusters
after a backend `sni_cluster` fallback and requires the disabled gauge to be one.
Fixtures provide valid protos and row contributions; they do not execute actual
Backend CONNECT translation or prove hashing collision-free.

An independent graph oracle exhausts 512 directed graphs, eight dynamic-selector
masks and seven proper starting subsets over three cluster names: 28,672 cases.
It checks exact membership, contribution maps and restoration of the unfiltered
version. This is finite implementation coverage, not a proof of arbitrary Go
graphs, all extension encodings or publication composition.

Before PR #14718, an exact-hash adapter removes the later constructor's
zero-window scoping option. The history profile applies exact-hash adapters for
the removed unused callback argument to `NewUniquelyConnectedClients`. These
change only injected fixture API calls. Original production sources and old
fixtures remain unchanged; all original assertions remain required.

Unless `--no-live` is supplied, the runner replays the actual KRT/publisher
snapshots through the pinned real cache/server and enterprise standard Envoy
1.39.4-patch2, digest
`sha256:66612e482b84c51de5d64c1f530c42e1ab458082b53a56a0303803d1b6a112d3`.
It uses two workers and zero drain time, with fresh upstream connections. Initial
forwarding must return 200. After corroborating the captured response versions
in Envoy logs and the resource content in active admin state, the counterexample
requires the cluster to remain active,
its owned listener to disappear and three fresh requests to return 503. A fixed
candidate must instead retain listener/proxy and return 200 three times. Eleven
mutations per capture must be rejected by the independent checker. `--no-live`
records an explicit omission; it cannot stand in for Envoy validation. An
unchanged CDS payload can retain its earlier admin version even after Envoy
receives a new response version. The checker allows that case only when the
whole captured CDS set is unchanged and the received version is present in
Envoy logs; changed content still requires the new admin version.

This runtime test replays captured snapshots; it does not run an uninterrupted
Kubernetes controller or the complete CONNECT/auth-header path. Its backend uses
the same pipe/listener/proxy dependency shape. xDS auth, TLS, FIPS, full downstream
plugin coverage and deployment are outside its scope. HTTP plus active admin
content corroborate this failure independently of ACK. The model's proposed
closure rule is not installed in production. See
[RF-034](../research-findings.md#rf-034-backend-owned-resource-closure-after-the-october-20-stack-update).

## October 20 rewarming regression

```sh
make formal-stack-rewarming
```

The public regression CI job requires both profiles and uploads their receipts
as artifacts. To choose fresh artifact directories locally, pass
`FORMAL_REWARM_BASELINE_ARGS='--artifacts /tmp/rewarming-before'` and
`FORMAL_REGRESSION_ARGS='--artifacts /tmp/rewarming-after'`. Neither directory
may already contain files.

This runs the same six injected setup/cache/server properties against two
immutable stack revisions. `faaaaf68d552` must reproduce four failures at
`same-version EDS must follow changed CDS after earlier EDS delivery`, while
the two unchanged-cluster controls pass. At `ab9865b83e63`, all six must pass.
Both pins must compile unchanged before injection; production Go digests must
remain unchanged. No existing fixture or older profile is replaced.

The fixture accepts the changed EDS version before changing same-name CDS,
then requests EDS at that held version while deliberately leaving CDS
unacknowledged. It requires identical assignment bytes in a fresh response,
then ACKs both and checks that the ACK does not start a replay loop. The
schedule runs in ordered and unordered ADS, both with optional NACK/reconnect
rules enabled and with both disabled. Unchanged cluster encodings must not
cause a replay. This checks the real cache/server with mock stream transport;
it does not observe Envoy application or establish a two-second product bound.
The timeout is a test failure guard.

The author's fix is commit `5c5b456be396`, added October 20 at 05:49:49 UTC in
PR #14703 and included in the repaired pin. [RF-033](../research-findings.md#rf-033-downstream-policy-rewarming-and-the-october-20-stack-fix)
records separate live downstream controller/Envoy evidence. Those captures and
logs stay outside Git. The fixed live rerun did not sample a warming interval,
so its conditional DNS insertion did not execute; do not report it as a passing
DNS regression. Alias/shared-assignment, rejection and blocked-send schedules
remain coverage gaps.

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
