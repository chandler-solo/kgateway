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
3. **A liveness theorem** (`XdsSpec/Liveness.lean`). The safety proofs
   say nothing about progress, and the production stale-endpoints
   incident lived in exactly that gap (assumption KRT-A1: a dropped KRT
   fan-out event left a client deferred forever). The spec models the
   defer watchdog as `heartbeatRederive` and proves
   `stuck_client_converges`: any reachable deferred state converges
   within one heartbeat re-derivation, in at most five steps, for any
   name universe. The model checker carries the finite-instance
   counterparts: `DroppedFanoutBug` (no coherence event) violates
   `DeferredPartial ~> Converged`; adding the heartbeat restores it.
4. **The per-cluster readiness model**
   (`XdsSpec/PerClusterReadiness.lean`) distinguishes C0 first cache
   publication from C2 endpoint truth and C3 warm route transitions.
   C0 publishes complete CDS with empty CLAs, including after cache
   restart; the cold model has no endpoint-recovery action. Its safe
   system preserves closure and can reach publication. Requiring usable
   endpoints strands it; bypassing missing CDS violates closure.
   C2 publishes a previously referenced backend's empty truth. C3 holds a
   warm flip onto a newly referenced unready backend. The warm whole-type
   RDS/LDS/SDS hold can still starve unrelated changes indefinitely.
   `formal-model-map.yaml` maps these obligations to concrete tests.
   The finite Lean progress checker checks reachability, not temporal
   liveness under fairness; TLC checks fair cold progress separately.
5. **Trace conformance** (`XdsSpec/TraceCheck.lean`,
   `lake exe xdsspec trace`). The proxy_syncer Go tests, run with
   `XDS_TRACE_OUT=<file>`, record every `snapshotPerClient` decision
   (defer or publish, with the snapshot data it was made on) as JSONL.
   The checker validates individual events using predicates over
   `Name := String`: publish closure (issue 13868), EDS presence/readiness, and
   no-orphan-CLAs (issue 14184). `publish-first` permits empty CLAs while retaining closure checks, and unknown decisions fail parsing. This does not replay the protocol state machine. This is the link that keeps the model
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
- `XdsSpec/Liveness.lean` — the stuck-client progress theorem
  (`stuck_client_converges`) behind assumption KRT-A1.
- `XdsSpec/PerClusterReadiness.lean` — the guard-#3 granularity model:
  per-cluster make-before-break, with both rejected gate variants as
  counterexamples.
- `XdsSpec/OrderedADS.lean` — the ADS wire-delivery ordering layer
  (`WithOrderedADS`): quiet-stream additions are CDS-first either way
  (cache type ordering), busy-stream randomization is closed by
  WithOrderedADS, ACK skew delivers RDS-first in both modes, and
  removals ship in the wrong order in both modes (they need the grace
  window). Assumption GCP-A3; probed deterministically against the real
  server in `pkg/kgateway/proxy_syncer/xds_delivery_order_probe_test.go`.
- `XdsSpec/TraceCheck.lean` — the JSONL trace conformance checker.
- `ASSUMPTIONS.md` — the assumption ledger (including the open KRT-A1
  liveness assumption and GCP-A3's probe-discharged delivery-ordering
  characterization).

## Relation to the TLA+ models

The TLA+ models remain in `devel/formal/tla/` as the reference the port
was validated against (every bug config reproduces in both). New
invariant work should land here first: the Lean spec is the one with
unbounded proofs and the implementation trace link. If the two ever
disagree, trust the Lean spec and fix the other.

## Future work

- Port the remaining TLA+ models (`XdsAdsSotw`, `XdsEnvoyWarming`,
  `XdsNamedEdsWatch`) onto the same `System`/`Checker` machinery.
- Unbounded proofs for the per-cluster readiness model (it is currently
  model-checked only; the convergence spec carries the unbounded
  results).
- Flip KRT-A1 to discharged once the defer watchdog lands with its
  tests.
- [Veil](https://github.com/verse-lab/veil) integration for SMT-backed
  invariant discovery (counterexample-to-induction) when the models
  outgrow hand-written `IndInv` strengthening.
- Emit traces from the e2e suites (real Envoy) and conformance-check
  those as well.
