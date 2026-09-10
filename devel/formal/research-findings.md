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
