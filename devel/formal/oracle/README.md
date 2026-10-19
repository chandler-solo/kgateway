# Executable Lean xDS oracles

Lean 4 is the executable reference here. Go's native fuzz engine generates
histories, sends the same raw fixture inputs to a persistent native Lean
process and to kgateway, and compares decisions and observations after each
event. A disagreement needs investigation: it may identify a Go defect, a
reference-model error, or an adapter error.

This is an implementation-linked test harness for two **research** profiles,
based on root revision `a753dfef9b211400e55e7d553773104ae7183c0e`:

| Profile | Actual implementation exercised | Observation boundary |
|---|---|---|
| `research-publication-v1` | Candidate helpers, `XdsSnapWrapper.Equals`, `syncXds`, actual SnapshotCache; a separate example suite drives `snapshotPerClient` through real KRT joins | Candidate metadata, all five type/resource/version sets, remembered EDS contributions, writes, unchanged captured candidates and other client keys |
| `research-cache-1cd122661-v1` | Publication plus pinned SnapshotCache, `CreateWatch`, cancellation closures and SotW Subscription | Cache response channels, returned-resource history, subscription state, watch counts and installed snapshots |

The cache profile requires
`github.com/envoyproxy/go-control-plane v0.14.1-0.20260702184136-1cd1226616f5`.
The runner rejects another pin pending an explicit profile review. It does not
silently invert historical failure expectations when upgrading a dependency.
The proposed stack `8e75468f4d0e055deb9b8f34e4254831f011e2a3`, historical
profiles in `../regressions`, and any deployed controller are separate subjects.
The proposed stack's timer engine is not in the research publication path and
is not tested by this adapter.

## Run and reproduce

Use the repository Go version and the toolchain in `../lean/lean-toolchain`.
From the repository root:

```sh
make formal-oracle
make formal-oracle FORMAL_ORACLE_ARGS="--mutations --histories 1000 --fuzztime 180s --artifacts /tmp/my-fresh-oracle-run"
make formal-oracle FORMAL_ORACLE_ARGS="--mutations --fuzztime 20000x --artifacts /tmp/my-fixed-count-run"
```

The artifact directory must be fresh and outside the repository. The default
is a new temporary directory. `--fuzztime` applies separately to each of the
two targets and accepts either a duration or Go's execution-count suffix `x`;
`--parallel` defaults to two fuzz workers. `--minimizetime` defaults to ten
seconds per minimization attempt and can be increased for a failing input.

An execution-count budget includes Go's baseline corpus replay. For example,
`--fuzztime 20x` permits only twenty executions, not twenty seconds. If the
corpus contains 447 inputs, that budget ends during baseline replay. Go can
report `PASS` for those inputs, but this runner still fails the fuzzing gate
because no generated cases ran. The error reports the completed/total
baseline count and suggests a larger budget. Use `--fuzztime 20s` for twenty
seconds, or a count larger than the corpus (such as `20000x`). The corpus can
grow as Go caches interesting inputs; duration budgets can also end during
baseline replay. Failed receipts retain baseline counts and progress.

The runner builds both
native executables, runs examples, 1,000 reproducible publication histories,
the KRT adapter suite and protocol/property checks, then native coverage-guided
fuzzing. The CI workflow also enables the four production mutations.

Receipts include source and executable hashes, HEAD and working diff, tool and
dependency versions, commands, parameters, test completion and fuzz progress.
Required test skips, missing execution/completion records, source changes
during verification, and absent fuzz execution fail the run. Failed receipts
are retained. Go saves minimized failures in `pkg/kgateway/proxy_syncer/testdata/fuzz`;
keep those inputs even after correcting the model or implementation.

For direct replay, build with `lake build xdsoracle xdscacheoracle` in
`devel/formal/lean`, then from the repository root:

```sh
export XDS_ORACLE_BIN="$PWD/devel/formal/lean/.lake/build/bin/xdsoracle"
export XDS_CACHE_ORACLE_BIN="$PWD/devel/formal/lean/.lake/build/bin/xdscacheoracle"
CGO_ENABLED=0 go test -tags e2e,formal_oracle -run '^FuzzPublicationOracle/e5b872685be363d7$' ./pkg/kgateway/proxy_syncer
CGO_ENABLED=1 go test -race -tags e2e,formal_oracle -run '^TestPublicationOracle|^FuzzPublication' ./pkg/kgateway/proxy_syncer
```

Normal builds exclude these tests through the `formal_oracle` build tag.
Mutations use external Go overlays, leaving production files unchanged. The
runner uses only read-only Git commands and performs no GitHub operations.

## Reference and fixture contract

[`PublicationOracle.lean`](../lean/XdsSpec/PublicationOracle.lean) uses lists
and pure transitions. Inputs describe translated cluster/endpoint rows,
row-hash contributions, route references, ancillary dependencies, opaque or
dynamic constructs, bootstrap exceptions, errors and client identity. They
do not contain Go's computed readiness flags, closure or publication decision.
`build` and `publish` are separate events, so a captured older candidate can
be published after a newer build. Two clients interleave; deletion retains
the served cache in this research profile.

The oracle applies the corrected derivation policy: a derived empty CLA is
valid truth; an absent row is distinct even if synthesis later produces the
same bytes. Errored clusters cannot be carried back. Shared assignments keep
current bytes and contributions. Held configurations preserve supported
dependencies, including the research scanner's conservative lexical matches;
opaque/dynamic configurations retain all eligible old CDS.

Versions follow RF-025's October 14 zero-marshal rule: XOR existing row
contributions, preserve contributions with carried rows, and use FNV-1a name
contributions for synthesized empties. Input-only changes can change versions
without changing bytes. The fixture's row hashes describe its own input
records; it does not claim to validate upstream endpoint-translation hashing.

[`CacheOracle.lean`](../lean/XdsSpec/CacheOracle.lean) composes publication with
named/wildcard subscriptions, returned resources, request versions, watch
entry and parked publication, cancellation, stream generation and cache
restart. It preserves the pin's asymmetry: incompatible immediate requests
lose their watch; incompatible parked watches survive. Unsubscribe-all is
distinct from legacy empty wildcard. Equal-version new subscriptions can
receive responses at request entry even though parked publication checks
version differences. An explicit partial-send example observes CDS delivery,
cancels an unbuffered EDS send, reads back the new installation, and retries
the retained watch without a new request.

The JSONL entry points reject wrong schema/profile, unknown or missing fields,
invalid hashes and malformed event histories. Each line is an independent
history; multiple lines reuse the native process. Hashes are decimal strings
to preserve all 64 bits. Events and structures are defined in the Lean files
and mirrored by the Go fixture types.

The fast native fuzzer uses up to 64 four-byte operations, four dynamic cluster
names, six assignment names and two clients. It includes direct/weighted
routes, EDS aliases, aggregate dependencies, empty/healthy/unhealthy endpoints,
errors, omitted gateways, secret rotation, stale candidate publication,
equality and deletion. Cache fuzzing adds five resource types, subscription
changes, cancellation and reconnects. Cache replacement and partial-send
cancellation have deterministic examples. Randomized asynchronous KRT
delivery, gRPC scheduling, nonces, ACK/NACK and timer expiry are outside these
generators.

## Independent checks and evidence limits

Full resource protos are compared, not just version strings. An additional
EDS relation rejects reuse of any version within a client history with
different observed resource bytes (including after intervening versions),
even if Lean and Go agree. The intentional XOR collision test demonstrates
that agreement with the algorithm does not prove collision freedom. Different
versions with identical bytes are allowed under the current contract.

Four Go source mutations must fail a named differential assertion: restore
the superseded contents gate, overwrite a current shared assignment with a
retained one, replace XOR with addition, and ignore derivation metadata in
equality. Comparator corruption and receipt-corruption tests check the
harness's own failure behavior. The KRT example adapter checks the fast
adapter's candidate correspondence on those fixtures; it is not a proof that
all real KRT schedules refine the model. The Go-side proto fixture encoder
is shared by expected and actual fixture construction, a remaining common
dependency.

| Evidence | What it supports | What it does not establish |
|---|---|---|
| Lean kernel-checked guard and selection theorems | Stated properties of the executable model, including current/retained pair precedence | A refinement proof of kgateway, the JSON adapter, native compiler or cache |
| Differential examples and fuzz execution | Agreement on the recorded generated histories for the named revision/pin | Exhaustive state-space coverage, full Kubernetes translation, arbitrary extensions or concurrency |
| KRT adapter examples | Actual joins construct the expected frozen candidates | Delivery/fairness of every intermediate KRT update |
| Real cache observations | Installation, watch disposition and channel responses in the tested schedules | gRPC delivery, ACK, Envoy applied state or traffic |
| Existing TLC/live suites | Their separately stated bounds, fairness and pinned runtime observations | Additional evidence from running this oracle harness |

Reachability is not temporal liveness. Cache installation is not delivery;
a response and an ACK are not evidence of applied configuration. In
particular, the default EDS-cache/15-second fallback and disabled-timeout
Envoy profiles remain separate, external characterization. This work does
not rerun or replace those tests or the historical counterexamples.

## Preserved development findings

* An early random run exposed nondeterministic nested protobuf `Any` map
  encoding in the fixture. Deterministic encoding and a repeated-encoding
  regression fixed the adapter; this was not a kgateway defect.
* The initial XOR mutation passed its chosen passthrough example. A filtered
  publication example was added so the mutation actually traverses the fold.
  Both failed and corrected validation receipts are retained externally.
* Native fuzzing minimized `e5b872685be363d7`: an old cluster's EDS service-name
  matched another CDS name. Go's documented conservative scanner retained
  that dependency; the original Lean graph omitted it. The corrected model
  and saved input preserve this oracle-model counterexample. It is not
  reported as a newly discovered Go defect.
* A 180-second publication run ended with Go's `context deadline exceeded`
  instead of a differential assertion or saved failure. Its receipt remains
  failed; fixed-execution-count validation is recorded separately. A timeout
  is not counted as passing fuzz evidence.

Classify future disagreements before changing expectations. Add a separately
named policy profile when intended behavior changes; preserve historical
profiles and any independent property counterexample.
