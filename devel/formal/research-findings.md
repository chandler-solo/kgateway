# xDS research findings and action items

This ledger tracks implementation defects, model limitations, and unfinished
research separately. A passing characterization test can reproduce a defect;
it does not mark that defect fixed. See [the program plan](xds-formal-research-plan.md).

## RF-001 Cold publication waits for usable endpoints

- Status: fixed on the research branch.
- Evidence: `TestSnapshotPerClientFirstPublishWithEmptyEndpoints`, the original
  three setup fixtures, Lean cold systems, and `XdsEnvoyWarmingColdEmpty.cfg`.
- Resolution: first cache publication requires nonexempt referenced CDS, but
  permits empty CLAs. Unknown trace decisions are rejected. Empty cluster
  initialization is separate from usable backend traffic.
- Action: run the live `xds_starvation` and `xds_warming` suites against the
  resulting image; cache-level restart tests do not characterize live Envoy.

## RF-002 Warm whole-type starvation

- Status: open implementation limitation.
- Evidence: `resolveDeferredPerCluster` holds RDS/LDS/SDS while a newly
  referenced backend has no usable endpoints, potentially forever.
- Action: add a permanent-empty warm transition with unrelated route and
  certificate changes; choose and model a bounded or isolated fallback policy.
  The C0 cold-start correction deliberately retains the warm C3 policy.

## RF-003 Permanent missing CDS can still starve first publication

- Status: open implementation limitation.
- Evidence: `syncXds` still defers with no cache and nonempty `missingReferenced`.
- Action: reproduce permanent derivation failure separately from transient lag
  and select an explicit fail-closed/degraded outcome. Do not synthesize a
  security-sensitive replacement cluster or assume a future ready event.

## RF-004 Envoy activation assumption is unproven

- Status: open environment assumption (ENV-A1).
- Evidence: warming e2e tests run through the kgateway gate; the initial-route
  test adds a host to a running gateway. They do not establish an independent
  Envoy usable-endpoint activation guarantee.
- Action: drive the pinned Envoy directly, testing missing/empty/ready EDS,
  rewarming, partial rejection, SDS, and startup/application observations.

## RF-005 Recoverability was described as temporal liveness

- Status: terminology/checker regression fixed; temporal refinement remains open.
- Evidence: `CheckerTests.lean` reaches G from A/B, but the corresponding
  weakly fair TLC model cycles A/B forever. `stuck_client_has_recovery_path`
  proves existence, not eventual scheduling or a wall-clock bound.
- Action: use explicit fair temporal specifications for progress; state and
  validate coherent-input and watchdog execution assumptions in composition.

## RF-006 Snapshot traces are not lifecycle conformance

- Status: strict schema and scenario coverage implemented; replay remains open.
- Evidence: required fields, decisions, sequence gaps/duplicates, empty and
  defer-only traces now fail. Emitter write failures fail the test process.
- Action: add terminal event-count receipts to detect truncated suffixes,
  subtest/stream generations, and stateful cache/watch/wire/application replay.
  Sequence checks cannot detect a missing suffix or entirely omitted scenario.

## RF-007 go-control-plane response model omits real behavior

- Status: open dependency/model defect, pinned to root module v0.14.0.
- Evidence: `CreateWatch` responds at equal version for new unreturned names;
  declined immediate requests are not registered; `respondSOTWWatches` deletes
  declined parked watches. `SetSnapshot` installs before sends can fail.
- Action: check in isolated probes and a descriptive watch lifecycle model;
  distinguish installation from response success. A passing defect probe is
  not a fixed cache. Track repaired watch retention and eligibility separately.

## RF-008 Finite hash evidence overstated as injectivity

- Status: assumption reopened; payload/version refinement remains open.
- Evidence: finite-width XOR digests cannot be injective over unbounded content;
  the current Lean version abstraction omits same-name payload changes.
- Action: add payload revisions and a collision assumption or revision allocator;
  retain finite-corpus tests as characterization only.

## RF-009 Required evidence execution and CI coverage

- Status: partial. Required Lean runner now fails on missing tools, runs full
  relevant unit packages, isolates snapshot scenarios, rejects skips, and
  retains logs/JSON receipts. Declaration mapping now requires explicit status.
- Action: validate required test outcomes from receipts, add dependency/setup/
  identity/ledger/e2e workflow triggers and artifact upload, and execute TLC
  negative expectations in CI. Existence of a named Go function is not execution.

## RF-010 Ordered delivery does not prove remote application

- Status: open protocol/composition limitation.
- Evidence: ordering probes exhibit ACK-skew and removal windows in both modes;
  Lean graceful removal requires observed deactivation. Setup now supports
  `EnableOrderedAds`, so earlier documentation of non-adoption was stale.
- Action: characterize actual application barriers, delayed/NACKed updates, and
  timeout policy. Do not equate a finite grace timer with route deactivation.
