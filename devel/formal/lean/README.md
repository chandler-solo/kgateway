# Lean xDS specification

This directory holds a machine-checked, implementation-linked
specification of the kgateway per-client xDS publication pipeline,
written in Lean 4. It supersedes the bounded TLC results for the
convergence model in `devel/formal/tla/` with three stronger artifacts:

1. **Unbounded safety proofs** (`XdsSpec/Proofs.lean`,
   `XdsSpec/MultiClient.lean`). Every invariant the TLA+ configs check
   at two clusters and one client is proven by induction for any
   resource-name universe, unboundedly many configuration episodes
   (`beginNextEpisode` cycles the machine), and any number of
   interleaved clients (`multi_safety`), including the isolation
   theorem: one client's deferral never mutates another client's
   published state. The proofs use no `sorry` and depend only on the
   standard kernel axioms (`propext`, `Quot.sound`) — verify with
   `#print axioms XdsSpec.multi_safety`.
2. **An explicit-state model checker** (`XdsSpec/Checker.lean`,
   `lake exe xdsspec check`). The TLC-style regression gate: the safe
   configuration must satisfy every invariant and the liveness property
   (a coherent input leads to activation or to the already-converged
   steady state), and each of the six bug configurations from the TLA+
   model must keep reproducing its counterexample. A bug config that
   stops failing means an invariant lost its teeth.
3. **Trace conformance** (`XdsSpec/TraceCheck.lean`,
   `lake exe xdsspec trace`). The proxy_syncer Go tests, run with
   `XDS_TRACE_OUT=<file>`, record every `snapshotPerClient` decision
   (defer or publish, with the snapshot data it was made on) as JSONL.
   The checker replays those events against the spec instantiated at
   `Name := String`: publish closure (issue 13868), EDS readiness, and
   no-orphan-CLAs (issue 14184). This is the link that keeps the model
   and `pkg/kgateway/proxy_syncer/perclient.go` from drifting apart —
   an implementation change that publishes a snapshot the spec forbids
   fails CI even if nobody re-reads the model.

What the spec assumes about components kgateway does not own
(go-control-plane's named-watch rule, Envoy warming, hash-based version
digests) is recorded as named assumptions in `ASSUMPTIONS.md`, each
mapped to the Go or e2e test that discharges it. The mapping is itself
gated by `devel/testing/formal_assumptions_test.go`.

## Running

```bash
make formal-lean          # proofs + model check + trace conformance
```

or directly:

```bash
cd devel/formal/lean
lake build                # re-checks every proof
lake exe xdsspec check    # explicit-state model checking
XDS_TRACE_OUT=/tmp/t.jsonl go test -tags e2e -count=1 \
  -run TestSnapshotPerClient ./pkg/kgateway/proxy_syncer/   # from repo root
lake exe xdsspec trace /tmp/t.jsonl
```

Requires [elan](https://leanprover-community.github.io/get_started.html);
the toolchain is pinned by `lean-toolchain`. CI runs all of this in the
`Formal Verification` workflow on PRs touching `devel/formal/` or
`pkg/kgateway/proxy_syncer/`.

## Layout

- `XdsSpec/Spec.lean` — the state machine: a parameterized port of
  `devel/formal/tla/XdsPerClientConvergence.tla` (states, guarded
  transitions, invariants), executable and provable from the same
  definitions. Versions are modeled as content digests; see IMPL-A1 in
  `ASSUMPTIONS.md`.
- `XdsSpec/Checker.lean` — generic BFS safety checker with
  minimal-length counterexample traces, plus a reachability-based
  liveness check (the finite analogue of the TLA+ leads-to property
  under weak fairness).
- `XdsSpec/Convergence.lean` — the concrete two-name instantiation
  matching the seven TLA+ `.cfg` files.
- `XdsSpec/Proofs.lean` — `IndInv`, the strengthened inductive
  invariant, and the `safety` theorem.
- `XdsSpec/MultiClient.lean` — the unbounded-clients lift: `isolation`
  and `multi_safety`.
- `XdsSpec/TraceCheck.lean` — the JSONL trace conformance checker.
- `ASSUMPTIONS.md` — the assumption ledger.

## Relation to the TLA+ models

The TLA+ models remain in `devel/formal/tla/` as the reference the port
was validated against (every bug config reproduces in both). New
invariant work should land here first: the Lean spec is the one with
unbounded proofs and the implementation trace link. If the two ever
disagree, trust the Lean spec and fix the other.

## Future work

- Port the remaining TLA+ models (`XdsAdsSotw`, `XdsEnvoyWarming`,
  `XdsNamedEdsWatch`) onto the same `System`/`Checker` machinery.
- A deductive liveness proof (the model checker covers the finite
  instance; the unbounded statement needs a ranking argument).
- [Veil](https://github.com/verse-lab/veil) integration for SMT-backed
  invariant discovery (counterexample-to-induction) when the models
  outgrow hand-written `IndInv` strengthening.
- Emit traces from the e2e suites (real Envoy) and conformance-check
  those as well.
