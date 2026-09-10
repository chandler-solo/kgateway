# Lean xDS specification

This is an abstract specification with implementation-linked checks, not a
proof of kgateway, go-control-plane, or Envoy. See the current
[assurance status](../assurance-status.md), [findings](../research-findings.md),
and [program plan](../xds-formal-research-plan.md).

## Evidence levels

- `Proofs.lean` proves structural invariants by induction over arbitrary name
  universes. `MultiClient.lean` lifts them to independently indexed clients.
  Episodes are serialized behind activation and client isolation is built into
  the transition relation. These quantifiers do not cover arbitrary Go
  interleavings, shared cache keys, or overlapping configuration revisions.
- `Checker.lean` checks bounded safety and **recoverability**: a goal is
  reachable from each premise state. It does not check fairness or temporal
  leads-to. `CheckerTests.lean` has a graph that passes recoverability while
  `../tla/ReachabilityIsNotLiveness.tla` admits a weakly fair infinite cycle
  avoiding the goal. TLC must reject that temporal property.
- `Liveness.lean` proves `stuck_client_has_recovery_path`: existence of a
  `SafeSteps` path supplied with a coherent candidate by the proof. Its
  construction uses at most five transitions; the theorem encodes neither a
  path-length bound nor scheduling or elapsed time. A deployed watchdog and
  permanent-invalid inputs remain separate obligations (KRT-A1).
- `PerClusterReadiness.lean` checks C0 cold CDS closure, C2 empty endpoint truth,
  and C3 warm transition holds. `ClientIdentity.lean` and `OrderedADS.lean`
  explore bounded identity and delivery schedules. A safe removal transition
  observes route deactivation; a timer alone does not establish that guard.
- `TraceCheck.lean` checks individual snapshot decisions. Schema 1 requires
  scenario/client identities, contiguous sequence numbers starting at one,
  known decisions, explicit arrays, and typed resource fields. Each checked
  trace must contain a publication. First publications permit empty CLAs.
  This catches missing interior records and malformed events, but cannot
  detect a lost suffix or prove lifecycle conformance. It does not replay
  cache installation, watches, wire responses, ACK/NACK, or activation.

The [assumption ledger](ASSUMPTIONS.md) distinguishes characterization from
open obligations. Its Go test checks declarations and anchors, not execution
or universal truth. Passing dependency probes may reproduce known defects.

## Running

```sh
CGO_ENABLED=0 make formal-lean
# Or retain receipts at a chosen path:
CGO_ENABLED=0 FORMAL_ARTIFACT_DIR=/tmp/xds-formal-run ./devel/formal/lean/check.sh
```

The required runner fails when Lean is unavailable, runs the full proxy-syncer,
ledger, and xdscheck unit packages, then runs each `TestSnapshotPerClient*`
scenario separately with schema/sequence metadata. It preserves Go JSON
receipts, scenario logs, traces, and model results. Scenario skips fail.
A top-level scenario can contain subtests; scenario identity is not a stream
identity. A full lifecycle schema and end-of-trace coverage receipt remain
RF-006 work.

For a single scenario, from the repository root:

```sh
XDS_TRACE_OUT=/tmp/snapshot.jsonl XDS_TRACE_SCENARIO=TestSnapshotPerClientClientRemovalRetainsServedCache \
  go test -tags e2e -count=1 -run '^TestSnapshotPerClientClientRemovalRetainsServedCache$' ./pkg/kgateway/proxy_syncer
cd devel/formal/lean
lake exe xdsspec trace /tmp/snapshot.jsonl
```

Use a new or truncated trace file for each process: the emitter appends and
its sequence restarts at one. The Lean version is pinned in `lean-toolchain`.

## Model disagreements

Lean structural proofs and TLC temporal checks complement each other; neither
supersedes the other's evidence. Resolve disagreements against the stated
property and pinned implementation, preserving counterexamples. Known
remaining work belongs in the program plan and findings ledger.
