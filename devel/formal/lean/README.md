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
- `PerClusterReadiness.lean` preserves the historical contents-gate C3 profile.
  `DerivedEndpointTruth.lean` proves the current guard is independent of endpoint
  health and checks finite recovery with derived empty input. `ClientIdentity.lean` and `OrderedADS.lean`
  explore bounded identity and delivery schedules. A safe removal transition
  observes route deactivation; a timer alone does not establish that guard.
- `TraceCheck.lean` checks individual snapshot decisions. Schema 2 requires
  scenario/client identities, contiguous sequence numbers starting at one,
  known decisions, explicit arrays, typed resource fields, and a content
  digest per CLA. Each checked trace must contain a publication. Every
  publication kind permits derived empty CLAs. Across consecutive publications to one
  client it enforces the version relation from `VersionDigest.lean`: an
  unchanged EDS version with changed CLA content fails (`version-reuse`);
  unchanged content with a moved version is counted (`version-churn`).
  Cache installation receipts emitted after SetSnapshot must match a pending
  decision for the client on the full per-type version tuple and be
  closed; a failed installation, or a
  decision left uninstalled in a scenario that installs, is a violation.
  Skipped (coalesced) and identical consecutive decisions are counted.
  A scenario that installs must end quiescent, with its last derived
  snapshot installed on every type, before the test returns. Subtests that
  reuse a client key emit a boundary event first, which settles the previous
  segment and resets the per-client relations. A segment that installs
  fabricated wrappers directly declares itself with a direct boundary, and its
  installations are counted rather than matched. These
  per-client rules do not replay watches, wire responses, ACK/NACK, or
  activation.
- `VersionDigest.lean` adds payload revisions, proves the Spec's name-set
  version is payload-blind, states the digest contract the implementation
  must meet on a run's compared domain, and shows the XOR combiner's
  structural cancellation collision. It does not prove the Go hash
  collision free (IMPL-A1 remains an assumption).
- `PublicationSelection.lean` proves current/retained/synthesized selection
  contracts over arbitrary payload and contribution types. Selection keeps each
  pair together, current derived truth wins, explicit exclusion removes that
  key, and omission is not empty truth. Its equality theorem is conditional on
  a projection determining publication; it does not prove Go `Equals` adequate.
- `PartialRejection.lean` is the RF-026 descriptive model of SotW rejection
  for a two-resource response. The atomic abstraction blocks a valid sibling
  behind an unrelated invalid resource; the observed Envoy semantics applies
  the sibling and leaves the accepted version behind. The CLI checks that
  each semantics violates exactly the property the other keeps.

- `InitialFetchTimeouts.lean` proves seven abstract expiry guard/provenance
  properties over unbounded timestamps, provider generations and arbitrary
  names/payloads. A due deadline and owning provider are required, expiration
  preserves receive/accept counts, and cached lookup requires matching identity.
  Disabled/completed subscriptions cannot expire in this relation. These guards
  are specified premises, not verified C++ behavior or temporal liveness. The
  [source-specific timeout suite](../timeouts/README.md) records implementation
  evidence and open cancellation/replacement/error obligations.
- `BackendResourceClosure.lean` proves seven structural properties over an
  arbitrary resource identity type: roots and reachable dependencies are kept,
  reachability is the least dependency-closed superset, and a retained backend
  requires its listener, proxy and secret. A cluster-only set cannot satisfy
  that ownership graph. The graph edges are specification inputs; this does
  not prove that Go discovers every edge or preserves it during publication,
  nor does it establish delivery, application or temporal progress. The
  [updated-stack regressions](../regressions/README.md#october-20-backend-owned-resource-regressions)
  expose the implementation's carry-path violation separately.

The [assumption ledger](ASSUMPTIONS.md) distinguishes characterization from
open obligations. Its Go test checks declarations and anchors, not execution
or universal truth. Passing dependency probes may reproduce known defects.

## Running

```sh
CGO_ENABLED=0 make formal-lean
# Focus only on the new abstract timeout proofs:
make formal-timeout-lean
# Or retain receipts at a chosen path:
CGO_ENABLED=0 FORMAL_ARTIFACT_DIR=/tmp/xds-formal-run ./devel/formal/lean/check.sh
```

The required runner fails when Lean is unavailable, runs the full proxy-syncer,
ledger, dependency-probe, receipt-checker, and xdscheck unit packages, then runs each `TestSnapshotPerClient*`
scenario separately with schema/sequence metadata. It preserves Go JSON
receipts, scenario logs, traces, and model results. Scenario skips fail.
The runner also validates required unit run/pass/package outcomes from JSON
receipts; a skipped required descendant fails. A top-level scenario can contain subtests; scenario identity is not a stream
identity. Each successful trace process emits a terminal scenario/event count;
the checker rejects a missing suffix receipt or mismatched count. These counts
cover emitted events only. The same Make gate now records and replays four
[cache/server lifecycle traces](../lifecycle/README.md) with a separate strict Go
checker. Full joined lifecycle instrumentation and replay remain RF-006 work.

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

The October 16 stack regression harness is separate: `make formal-stack-regressions`
expects nine specific assertion failures and the known setup build failure at the
immutable reviewed tip. `make formal-legacy-contents` preserves the superseded Go
starvation example. See `../regressions/README.md`; negative-control success is
not a passing production correctness test. PublicationTransitions.tla adds the
three timer interaction controls and a proposed, bounded fixed model.
`make formal-stack-current` preserves the updated `215ebe8e6450` profile with
eleven desired-property failures and two separately recorded compilation errors.
`WatchDeliveryComposition` and `ConnectionGenerations` add explicit retry,
silent-client and connection-generation temporal obligations; their fixed
policies are not implementations adopted by the research tree.

The October 16 trace correction distinguishes transform candidates from final
publisher decisions: a newer candidate may precede resolution of an older
wrapper. Required replay fixtures preserve that overlap and an expected
`version-reuse` failure from the old randomized test's small integer row
contributions. The regular sweep hashes its revision inputs; this is not a
collision-freedom proof. The Go XOR negative control remains required.
