# xDS research findings and action items

This ledger tracks implementation defects, model limitations, and unfinished
research separately. A passing characterization test can reproduce a defect;
it does not mark that defect fixed. See [the program plan](xds-formal-research-plan.md).

## RF-001 Cold publication waits for usable endpoints

- Status: fixed on the research branch. Live suites executed.
- Live evidence (2026-09-11): `TestKgateway/XdsWarming` (three tests) and
  `TestKgateway/XdsStarvation` (three tests) passed against the research
  image built from `c23bf8591e` (`ghcr.io/kgateway-dev/kgateway:v1.0.1-dev`,
  image id `b5121ad85ee07`, envoy-wrapper `90cc9fa71ab40` on the Makefile's
  `envoyproxy/envoy:v1.39.1`) on a kind cluster `xdsformal` with its own
  MetalLB pool, `PERSIST_INSTALL=true`, install namespace `kgateway-test`,
  controller restarts 0. Six of six tests passed in 120 s. The starvation
  suite's own note applies: it pins anti-starvation properties for an
  ExternalName reference whose defer window is transient at laptop scale; it
  is not a reproducer of the #14184 wedge. Run logs are local receipts, not
  committed.

- Evidence: `TestSnapshotPerClientFirstPublishWithEmptyEndpoints`, the original
  three setup fixtures, Lean cold systems, and `XdsEnvoyWarmingColdEmpty.cfg`.
- Resolution: first cache publication requires nonexempt referenced CDS, but
  permits empty CLAs. Unknown trace decisions are rejected. Empty cluster
  initialization is separate from usable backend traffic.
- Evidence added: the direct `restart` scenario shows a warm Envoy keeps
  serving across a control-plane restart with an empty SnapshotCache,
  reconnects with accepted versions and no nonce, receives nothing for an
  equal-version republish, and applies a later revision. The kgateway
  first-publish gate for a client with no cache entry (RF-003) is the part
  this scenario does not exercise.
- Action: run the live `xds_starvation` and `xds_warming` suites against the
  resulting image; cache-level restart tests do not characterize live Envoy.

## RF-002 Warm whole-type starvation

- Status: open implementation limitation.
- Evidence: `resolveDeferredPerCluster` holds RDS/LDS/SDS while a newly
  referenced backend has no usable endpoints, potentially forever.
- Evidence added: `TestWarmEmptyBackendHoldsUnrelatedRouteAndSecret` drives
  actual `syncXds` and SnapshotCache through nine revisions with an empty new
  backend. CDS and the empty CLA publish while an unrelated route configuration
  addition and synthetic generic-secret rotation remain at the initial state.
  This characterizes cache composition, not certificate use or Envoy activation.
- Temporal model: `WarmTypeStarvationCurrent.cfg` checks the same permanent
  empty-input abstraction under weakly fair publication. Its expected temporal
  counterexample keeps rebuilding while the independent update never applies.
  `WarmTypeStarvationIsolated.cfg` specifies a proposed independent-component
  publication policy that holds the blocked flip and permits independent
  progress. The latter is not implemented or a proved refinement of Go.
- Action: extend to live certificate rotation and choose and model a bounded
  or isolated fallback policy. Repeated finite input is not a temporal proof.
  For isolation, establish the dependency partition across RDS/LDS/SDS and
  prove that mixed resource versions do not retain revoked security state or
  activate dangling references. The abstract independent Boolean assumes that
  partition; it does not compute it.
  The C0 cold-start correction deliberately retains the warm C3 policy.

## RF-003 Permanent missing CDS can still starve first publication

- Status: open implementation limitation; cache-boundary and temporal
  evidence added, policy decision open.
- Evidence: `syncXds` still defers with no cache and nonempty `missingReferenced`.
- Evidence added: `TestColdMissingReferencedClusterWithholdsAllTypes` drives
  actual `syncXds` and SnapshotCache through ten deferred revisions whose
  routes reference a cluster absent from CDS. The cold proxy receives no
  snapshot of any type, including a ready unrelated listener, route, cluster,
  endpoint assignment, and rotating secret; every decision is
  `defer-first-publish`. The same reference recorded as errored publishes
  everything else fail closed, and a reference that later arrives publishes on
  that revision. Nothing in `syncXds` distinguishes the two arrivals.
- Temporal model: `ColdMissingCdsStarvation.tla` checks the first-publication
  guard under weakly fair rebuilds. `Current.cfg` (permanent absence) produces
  the expected progress violation; `Transient.cfg` (input assumed to become
  coherent) converges; `Classified.cfg` specifies a proposed policy that
  records a still-unresolved reference as errored after an abstract bound,
  and `NoSilentDanglingPublish` holds in all three. The bound, its trigger,
  and whether the classification is per proxy or per derivation are not
  chosen by the model, and the policy is not implemented.
- Source paths that can make a reference permanently missing without an
  errored record are tracked under RF-024.
- Data-plane bound: the direct `timeouts` scenario shows Envoy converts a
  dependency that never arrives into a degraded active state after its
  initial fetch timeout (2 s in the run, 15 s by default), a cluster with no
  hosts or a listener with no routes, and then reports ready. Any
  classification bound chosen here should be compared with that default:
  withholding longer than Envoy would wait trades a degraded proxy for an
  unconfigured one.
- Action: choose and implement an explicit outcome for a reference that stays
  unresolved: classify as errored after a bound derived from the startup
  probe budget, or surface a status condition and keep waiting. Do not
  synthesize a security-sensitive replacement cluster or assume a future
  ready event. Verify that the chosen policy refines the model's
  `Classify` step, and reproduce the warm variant: a permanently missing
  newly referenced cluster holds RDS/LDS/SDS the same way RF-002's empty
  backend does.

- Observability note (2026-09-11, from the shared-base CDS stack review, head
  5da2acd8ec, verified read-only): the stack lowers the "per-client inputs
  not ready; deferring snapshot" log in `perclient.go` from Info to Debug,
  citing per-client volume at startup. That line is the only default-level
  signal that a client's snapshot is being withheld, which RF-003's
  permanent-missing case and RF-009's live evidence both depend on. If the
  downgrade lands, the deferral needs another default-visible signal: a
  gauge of currently deferred clients derived from collection state, or a
  rate-limited Info line per client after the deferral exceeds a bound.
- Stack update (2026-09-12): the stack keeps the line at Debug but adds a
  gauge of clients whose snapshot is currently withheld (e6136dbcb2), which
  is the default-visible signal asked for above. Residual: no per-client
  identification without raising the log level.

## RF-004 Envoy activation assumption is unproven

- Status: partly characterized directly; ENV-A1 remains open beyond tested profiles.
- Evidence: warming e2e tests run through the kgateway gate; the initial-route
  test adds a host to a running gateway. They do not establish an independent
  Envoy usable-endpoint activation guarantee.
- Evidence added: `envoy-characterization.md` records direct missing/empty/ready,
  same-version rewarming, unhealthy and endpoint-loss probes in both panic modes.
- Evidence added: the `references` scenario records a dangling RDS reference
  (accepted, per-route 503), an inline LDS route to the same absent cluster
  (listener rejected), and partial CDS/LDS rejection that applies the valid
  resources of the NACKed response (RF-026).
- Evidence added: the `rejection` and `secrets` scenarios cover EDS and RDS
  multi-resource rejection and SDS rotation, removal, and absence.
- Evidence added: the `restart` scenario covers a control-plane restart with
  an empty cache against a warm proxy.
- Evidence added: the `restart` scenario also restarts the proxy container
  against the warm cache; the fresh Envoy is served every type immediately.
- Evidence added: the `timeouts` scenario measures the initial fetch timeout
  for missing EDS and RDS, their sequential composition through the init
  manager phases, and a route ahead of its cluster (503, no NACK).
- Evidence added: the `init` scenario shows a partial named EDS response
  holds initialization and the LDS request until the missing assignment
  arrives; a CDS re-push in between neither blocks nor completes it (Envoy
  #21425 does not reproduce on the pin).
- Action: extend the pinned direct harness to restart during warming, worker
  application observations, validation-context secrets, and Delta xDS.

## RF-005 Recoverability was described as temporal liveness

- Status: terminology/checker regression fixed; temporal refinement remains open.
- Evidence: `CheckerTests.lean` reaches G from A/B, but the corresponding
  weakly fair TLC model cycles A/B forever. `stuck_client_has_recovery_path`
  proves existence, not eventual scheduling or a wall-clock bound.
- Evidence added: `KrtRecovery.tla` states the recovery claim with explicit
  weak fairness under four assumptions. Only KRT dependency delivery or a
  watchdog that rereads authoritative inputs makes a deferred-partial client
  converge; no delivery and no watchdog, or a watchdog replaying the stuck
  cached derivation, produce the expected temporal counterexamples. Source
  inventory found no watchdog or periodic re-derivation in the deployed
  proxy syncer or setup, so the deployed profile relies on KRT delivery.
- Corroboration: kgateway #14047 reports a TrafficPolicy left permanently
  invalid after a transient OIDC discovery failure until a generation change
  or restart, and was closed as stale without a fix. That is the
  no-delivery, no-watchdog configuration of `KrtRecovery.tla` observed in
  production.
- Action: collect implementation evidence that KRT fan-out delivery is fair
  for per-client snapshots, or add a watchdog that rereads authoritative
  inputs and prove it refines `WatchdogRederive`; compose with the cache and
  Envoy models under the same fairness.

## RF-006 Snapshot traces are not lifecycle conformance

- Status: strict schema and scenario coverage implemented; per-client
  version relation and cache installation receipts added; wire, acceptance,
  and activation replay remain open.
- Evidence: required fields, decisions, sequence gaps/duplicates, empty and
  defer-only traces now fail. Emitter write failures fail the test process.
- Evidence added: successful test completion emits a terminal scenario/event
  count under the emitter lock. Lean rejects missing/mismatched receipts,
  duplicate terminals, and subsequent events. Negative guards cover suffix
  loss; the required runner still enumerates scenarios independently.
- Evidence added: syncXds emits `installed` or `install-failed` after
  SetSnapshot with the installed content. Decisions queue per client in
  order; each installation must match a pending decision by EDS version and
  be closed, skipped older decisions count as superseded (KRT coalescing),
  and an identical consecutive decision counts as a suppressed
  recomputation. install-failed is a violation, and a decision left
  uninstalled in a scenario that installs anything is a violation; the
  twelve transform-only scenarios that never call syncXds have their pending
  decisions counted. Subtests that reuse a client key emit a boundary
  event that settles the previous segment and resets the relations; without
  it, a decision from one subtest was matched against an installation from
  the next and reported as a mismatch in one run. A subtest that installs
  fabricated wrappers directly (the per-cluster readiness table) declares a
  direct segment; the relation cannot tell such an installation from a
  mismatch, so it counts them, and the randomized and real-translation
  scenarios keep the strict relation. One transform-only test now also installs and asserts
  the served cache. Developing the rule exposed that the transform can emit
  the same decision several times per input change (suppressed by KRT
  equality) and that installations can lag several decisions; both are
  legitimate and now explicit in the relation. This is the first lifecycle
  stage beyond the decision itself.
- Evidence added: every decision and installation event carries the
  per-type version tuple (cluster, endpoint, listener, route, secret); the
  installation relation matches on the whole tuple, so a route or secret
  change with an unchanged EDS version cannot pair with the wrong decision.
  Publications or installations without the tuple are malformed.
- Action: add subtest/stream generations and stateful cache/watch/wire/
  application replay. Terminal counts cover emitted events, not transitions
  omitted by instrumentation; audit transition coverage separately.

## RF-007 go-control-plane response model omits real behavior

- Status: dependency defects characterized at root module v0.14.0; the pin
  moved on 2026-09-12 (upstream kgateway #14654, go-control-plane
  `1cd122661`, #1498 including #1356): the declined parked watch is now
  retained and delivered to, the request-entry decline still registers no
  watch; model refinement open.
- Lineage: replaying the probes against unreleased upstream `bf9b60b56`
  (#1356) and `1cd122661` (#1498) shows the parked declined watch is retained
  there (GCP-A5 repaired expectation) while the request-entry decline still
  registers no watch. No root-module release contains either change as of
  the 2026-09-04 checkout. Update 2026-09-12: `1cd122661` is now the
  root pin via the upstream merge; `TestParkedNamedWatchIsRetainedOnDeclinedResponse`
  asserts retention and delivery of the next aligned snapshot, and
  `TestDeclinedNewRequestRegistersNoWatch` still passes as a defect probe. See `gcpprobe/README.md` profile diff and
  `gcpprobe/replay-profile.sh`.
- Evidence: `CreateWatch` responds at equal version for new unreturned names;
  declined immediate requests are not registered; `respondSOTWWatches` deletes
  declined parked watches. `SetSnapshot` installs before sends can fail.
- Evidence added: `gcpprobe` reproduces both paths, equal-version subscription,
  and partial installation. `GcpWatch.lean` checks discard and retention policies.
- Evidence added: `TestClearSnapshotOrphansParkedWatchOnPin` shows ClearSnapshot
  drops the node status while its parked watch stays registered and unanswered
  (corpus #505).
- Evidence added: `TestUnrequestedBootstrapClusterCLAWithholdsEDSForOlderProxy`
  pins the superset decline that produced kgateway #14471 during a rolling
  upgrade: one CLA a proxy generation never requests withholds that proxy's
  whole EDS response across revisions until the snapshot is filtered.
- Deployment reachability: no production code in this repository calls
  ClearSnapshot; the per-client Delete callback retains the cache entry
  (GCP-A2). The orphaned-watch path is therefore unreachable in the deployed
  profile unless a caller is added.
- Action: compose the named-watch model with installation and stream lifecycle;
  distinguish installation from response success. A passing defect probe is
  not a fixed cache. Track repaired watch retention and eligibility separately.

## RF-008 Finite hash evidence overstated as injectivity

- Status: contract stated and runtime detection added; the collision
  assumption itself remains open.
- Evidence: finite-width XOR digests cannot be injective over unbounded content;
  the current Lean version abstraction omits same-name payload changes.
- Evidence added: `VersionDigest.lean` adds payload revisions and proves the
  Spec's name-set version cannot distinguish a same-name payload change, so
  the convergence proofs' version claims are name-set claims. It states
  `DigestContract` (determinism on content-equal sets, collision freedom on
  the compared domain) and derives both directions the checker enforces.
  `xorDigest_cancels` records that two resources with equal per-resource
  digests cancel to the empty set's version, a combiner-level collision.
  Schema-2 snapshot traces now carry each CLA's FNV-1a digest, and
  `TraceCheck.lean` fails a run where a client's EDS version is unchanged
  while its CLA content changed (`version-reuse`) and counts unchanged
  content with a moved version (`version-churn`). This is the first
  per-client stateful trace rule.
- Action: decide between keeping the collision assumption (documented, with
  the trace rule as the detector) and a revision allocator (collision free
  but no longer idempotent for equivalent content, which PR #14516's churn
  suppression relies on). Review any nonzero `version-churn` count from the
  required runner against the carry-forward version suffix and fixture
  passthrough versions before treating it as an implementation defect.
- Review note, PR #14604 (head 5da2acd8ec, unmerged), relayed from a
  review session and checked against the diff: the PR changes the
  `UccWithEndpoints` marker from `+krtEqualsTodo` to `+noKrtEquals
  EndpointsHash is a content hash over the same inputs`. That is an equality
  claim: KRT treats a row as unchanged when Client, endpointsName, and
  EndpointsHash match, without comparing the CLA. The interner the PR adds
  verifies protobuf equality before sharing a proto, but KRT change
  detection does not, so the claim needs both halves of `DigestContract`:
  `sound` (equal inputs give equal hashes, which the fold provides) and
  `injectiveOn` (equal hashes give equal built CLAs), which is assumed, not
  proven, on main and in the PR alike. A collision or an uncovered input
  produces a row KRT never republishes, which no snapshot trace can see
  because no publication happens; `version-reuse` detects only published
  staleness. The marker should say the assumption, or the Equals should
  compare the CLA digest it already has access to. See RF-029 for the
  input-coverage question.

## RF-009 Required evidence execution and CI coverage

- Status: required unit receipt validation and broader CI implemented; full
  lifecycle/e2e and extended-bound coverage remain open. Required Lean runner
  fails on missing tools, runs full
  relevant unit packages, isolates snapshot scenarios, rejects skips, and
  retains logs/JSON receipts. Declaration mapping now requires explicit status.
- Evidence added: `checkreceipts` rejects absent, skipped, failed, malformed,
  or truncated required test outcomes. Workflow runs on all PR changes, uploads
  receipts, and includes bounded TLC and pinned direct Envoy.
- Evidence added: the live `XdsWarming` and `XdsStarvation` suites were
  executed once against the research image (see RF-001); the run produced no
  snapshot trace, so the trace relations were not applied to it.
- Action: emit and check snapshot traces from a live run (the e2e harness
  would need to set the trace environment on the controller and collect the
  file); until then live evidence is pass/fail only.
- Action: add lifecycle trace receipts and live KGW e2e execution. Existence of
  a named Go function is not execution; RF-015 tracks original TLC bounds.

## RF-010 Ordered delivery does not prove remote application

- Status: open protocol/composition limitation.
- Evidence: ordering probes exhibit ACK-skew and removal windows in both modes;
  Lean graceful removal requires observed deactivation. Setup now supports
  `EnableOrderedAds`, so earlier documentation of non-adoption was stale.
- Evidence added: the direct `timeouts` scenario shows a route published
  ahead of its cluster answers 503 until the cluster and its endpoints arrive,
  then recovers without a reconnect; the ordering window is a 503 window,
  not a rejection.
- Action: characterize actual application barriers, delayed/NACKed updates, and
  timeout policy. Do not equate a finite grace timer with route deactivation.

## RF-011 Immediate cache send can block other nodes

- Status: characterized API hazard in v0.14.0; deployed reachability open.
- Evidence: `TestFullImmediateChannelBlocksOtherNode` enters the immediate path
  with a full caller-supplied channel. It blocks another node's SetSnapshot;
  draining the channel releases both. All probe goroutines are joined.
- Action: audit wrapper and ordered-ADS shared-queue capacity and cancellation.
  Default per-type fresh capacity-one channels avoid this particular supplied
  full-channel schedule; that is not a proof for every server/wrapper mode.

## RF-012 Rejected version repeats on matching NACK

- Status: characterized dependency behavior; retry/isolation policy open.
- Fix (2026-09-12): branch `chandler/xds-suppress-nack-resend`, commit
  0d6a79470b, based on main 23c2c8ea40 (worktree
  `~/git/kgateway/.claude/worktrees/xds-suppress-nack-resend`, not pushed).
  A decorator around the SnapshotCache records the last version sent per
  cache node and type from the server's response callback; a request
  carrying an error detail whose type's current snapshot version equals
  that last-sent version is handed to the cache claiming the current
  version, so the cache parks the watch until the snapshot changes. A NACK
  after the snapshot moved passes through and is answered at once. Setting
  `KGW_XDS_SUPPRESS_NACK_RESEND`, default on; counter
  `envoy_xds_nack_resends_suppressed_total`. Six harness tests in both ADS
  modes plus a test that pins the loop on the undecorated cache. This is a
  kgateway-side remedy; the resend itself remains library behavior on the
  current pin.
- Evidence: `TestRepeatedNackResendsSameVersionAndCorrectionRecovers` drives
  32 repeated NACKs through the actual cache/server and receives the same
  version with fresh nonces, then receives a corrected snapshot.
- Evidence added: `envoyprobe -scenario references -snapshot-cache` measures
  the recurrence with a real Envoy v1.39.1: about 6,800 CDS NACK round trips
  and about 3,900 LDS NACK round trips in two seconds, in both ADS modes,
  each re-applying the valid siblings (RF-026) and re-rejecting the invalid
  resource; a corrected snapshot ends it. One machine, concurrency one.
- Field corroboration: kgateway #14453 reports an LDS NACK retried in a
  roughly ten-per-second loop in a deployed gateway while the listener still
  reports Programmed=True; the loop is this resend mechanism bounded by the
  real client's request rate.
- Action: model rejected payload identity, damping/reset/cancellation and healthy
  type/client progress. The scripted recurrence is not a real-Envoy CPU estimate.

## RF-013 EDS health, selection, and success differ

- Status: characterized Envoy behavior, not a new Envoy defect.
- Evidence: direct v1.39.1 probe marks its sole reachable host UNHEALTHY.
  With default panic behavior traffic returns 200; with panic threshold zero
  it returns 503. In both cases process readiness remains 200.
- Action: inventory panic threshold, priorities, fail-on-panic, degraded hosts,
  active health checking, outlier ejection, and transport reachability. Do not
  equate kgateway's `clusterLoadAssignmentHasUsableEndpoint` predicate with Envoy host selection.
  `EnvoyAvailability.lean` records the one-priority distinction only.

## RF-014 Same-version EDS can be required by cluster rewarming

- Status: directly characterized lifecycle requirement; composition open.
- Evidence: direct Envoy changes CDS connect timeout without changing cluster
  name or EDS content/version. Candidate remains warming and old traffic works;
  replaying the identical CLA/version completes warming. The trace records an
  EDS request at the existing accepted version.
- Action: compose this schedule with go-control-plane returned-resource/version
  eligibility and prove or implement a rewarming response trigger. The direct
  scripted server intentionally bypasses SnapshotCache, so this observation
  alone does not prove the integrated cache/server strands Envoy.

- Bootstrap note (2026-09-11, from RF-028): the indefinite park measured here
  assumes the EDS config source's initial fetch timeout is disabled. kgateway's
  bootstrap enables `use_eds_cache_for_ads` and leaves that timeout unset
  (15 s), so in the deployed profile a same-name rewarming parked at an equal
  EDS version completes from the cached assignment when the timeout expires.
  The park is a 15 s window there, not a permanent strand.

## RF-015 Broad ADS state space is not an established CI receipt

- Status: explicit verification bound, extended run open.
- Evidence: the original XdsAdsSotw run was stopped after millions of distinct
  states without exhaustion. That is not a passing model-check result.
- Resolution: make bounds explicit constants while preserving the original
  config, and use the separately named `XdsAdsSotwCI.cfg` in the required job.
  Its bounds are two versions, two nonces per type, two streams, one stale
  request; logs record explored states and tool identity.
- Action: exhaust the original bounds with adequate resources and report the
  result separately (`TLC_INCLUDE_WIDE=1`). CI emits a deferred-wide-model
  receipt; it must not imply every original bound was checked.

## RF-017 Real cache/server can strand same-name cluster rewarming

- Status: reproduced integration defect/limitation with v0.14.0 and Envoy
  v1.39.1; mitigation remains open. Pin update 2026-09-12: the discard half
  of the strand (a declined parked watch deleted, GCP-A5) is repaired in the
  new go-control-plane pin, so a proxy parked behind a misaligned snapshot is
  delivered the next aligned one without re-requesting; the equal-version
  park itself is unchanged (`TestResubscribeAtEqualVersionIsAnsweredOnPin`
  and the envoyprobe warming scenario behave as before), so the same-name
  rewarming window remains and is bounded only by the bootstrap note below.
- Evidence: `envoyprobe -snapshot-cache` changes CDS connect timeout while EDS
  names/content/version stay fixed. Envoy re-requests EDS at its accepted
  version; cache eligibility parks the watch. Unchanged SetSnapshot completes
  but the candidate stays warming throughout a checked 400 ms window. An
  explicit EDS revision completes warming. Repeat with `-ordered` to exercise
  the shared ADS queue. `RewarmingComposition.lean` has no recovery path when
  only same-version requests/republishes are allowed; adding an EDS revision
  provides an existential recovery witness.
- Action: design and verify a rewarming response trigger across cache, stream,
  nonce, and subscription state. A global EDS version bump is not yet a fix:
  delivery/ACK skew can place EDS ahead of CDS, and unnecessary replies can
  introduce loops. Extend to real KGW same-name cluster changes, NACK, TLS,
  and multi-client schedules before extracting a product mitigation.

- Bootstrap note (2026-09-11, from RF-028): the indefinite park measured here
  assumes the EDS config source's initial fetch timeout is disabled. kgateway's
  bootstrap enables `use_eds_cache_for_ads` and leaves that timeout unset
  (15 s), so in the deployed profile a same-name rewarming parked at an equal
  EDS version completes from the cached assignment when the timeout expires.
  The park is a 15 s window there, not a permanent strand.

## RF-018 Unsubscribe-all is treated as wildcard by snapshot responses

- Status: repaired in the root pin as of 2026-09-12 (go-control-plane
  `1cd122661` via upstream kgateway #14654); reproduced in v0.14.0 before
  that, both ADS modes.
- Evidence on the new pin: `TestUnsubscribeAllReceivesNothingFromCache`
  (immediate and parked entry paths: no response, no watch registered, a
  later snapshot answers nothing) and `TestUnsubscribeAllLeaksNothingOnWire`
  (both ADS modes: no watch parks, a changed snapshot sends nothing, a
  resubscribe at the old version is answered with the new content). The
  earlier defect probes were inverted in place rather than deleted.
- Evidence (v0.14.0, before the pin moved): `TestUnsubscribeAllStillReceivesResourcesFromCache` covered immediate
  and parked response paths. `TestUnsubscribeAllResourceLeaksOnWire` used the
  actual cache/server: after named subscription, an empty names list clears
  the subscription, but a later changed snapshot still sends that resource.
  `GcpSubscription.lean` needs distinct legacy-wildcard and unsubscribe-all
  states; the prior fixed-named-set watch model could not express this trace.
- Source: subscription history in `stream/v3/subscription.go` is discarded by
  `simple.go:respond/createResponse`, which inspect raw request names.
- Lineage: both unsubscribe-all probes stop reproducing at upstream
  `bf9b60b56` (#1356, cache response rewrite): the cache answers no
  unsubscribed watch and the server parks none. #1498 (`1cd122661`) adds a
  send-time filter for queued superseded responses in ordered ADS; the probes
  do not construct that schedule, so its effect is unobserved. Neither change
  is in a released root module; v0.14.0 is the newest tag at the 2026-09-04
  checkout. The earlier attribution of the probed repair to #1498 was wrong.
- Evidence added: `TestQueuedSupersededResponseIsDroppedOnSubscriptionChange`
  builds the #1498 shape with the real cache by publishing from the request
  callback while a parked named watch is being superseded. The cache answers
  and discards the parked watch, yet only the new watch's response reaches
  the wire in both ADS modes: ordered ADS drains and drops same-type queued
  responses before creating the new watch, and default mode abandons the old
  per-watch channel. Because fan-out and cancel serialize on the cache mutex,
  SnapshotCache cannot answer a watch after cancel returns, so the #1498
  injection point does not exist for it. The probe also passes at
  `1cd122661`.
- Action: enforce subscription-aware eligibility/filtering at cache and send
  boundaries, and test empty responses, full-state types, wildcard history,
  and queued supersession. GCP PR #1498 adds a send-time subscription filter
  in newer source. Ancestry validated: unreleased. The real-cache schedule is
  constructed and shows no leak on the pin, so #1498 is not a kgateway fix
  lead for SnapshotCache; #1356 is. Remaining: audit any cache wrapper or
  alternative cache for responses after cancellation before relying on this,
  and decide whether kgateway adopts a pseudo-version or waits for a release
  (done upstream: #14654 adopted the pseudo-version). KGW deployment
  reachability and the complete stream/queue refinement remain open.

## RF-016 Historical inventory is not completed bug coverage

- Status: all seven title/body inventories captured; a first classified batch
  of eleven go-control-plane keyword candidates is recorded; detailed
  classification of the remaining candidates and the linked-source audit
  are open.
- Evidence: the exporter records every retrieved issue/PR page and cursor,
  cutoff, repository identity, payload checksum, keyword hits, and unreviewed
  dispositions. GitHub rejected page-number pagination at Envoy page 100;
  following server-supplied cursors is necessary for large repositories.
- Evidence added: `corpus-inventory.json` records completed enumeration;
  `bug-corpus.json` records seventy-two dispositions: eleven from discovery, twenty-one from go-control-plane, sixteen from kgateway, six from Envoy Gateway, eleven from Envoy, three from Gloo, and four from solo-kit, with two current hold-outs (EG #9519 and kgateway #14429), with their detail pages
  captured privately. Gloo/kgateway repository IDs prevent lineage loss.
- Evidence added: the two held-out go-control-plane mechanisms were probed on
  the pin without new model state. #431 is repaired in v0.14.0 (a
  resubscription at the unchanged version is answered); #505 reproduces
  (ClearSnapshot orphans the parked watch).
- Action: review comments/reviews and linked fixes across the candidate set,
  audit relevant source history and release ancestry, classify every candidate,
  and review non-keyword sources and held-out mechanisms. Search or title/body
  inventory counts are not a claim to have reviewed all historical xDS bugs.

## RF-019 Registered control-plane paths exceed the modeled ADS path

- Status: reachability inventory added; nondefault RPC behavior remains open.
- Evidence: the production gRPC server registers ADS plus individual EDS/CDS/
  RDS/LDS services. Their generated registrations expose SotW, Delta, and
  Fetch methods; Fetch is rejected by callbacks only when xDS auth is enabled.
  The normal generated Envoy bootstrap selects SotW ADS, but registration is
  still an externally reachable implementation path subject to auth/network.
- Action: characterize and either support or disable individual SotW, Delta,
  and Fetch paths. Audit node identity/auth, named and wildcard subscriptions,
  version maps, removal, reconnect, and shared cache state. Inventory bootstrap
  overlays and the optional local-cluster EDS fetch timeout. Keep Linear/Mux/
  heartbeat caches excluded by construction while importing shared mechanisms.

## RF-020 Secret discovery is a separate unmodeled control plane

- Status: registered surface inventoried; security/lifecycle model open.
- Evidence: `pkg/sds/server` constructs an ADS=false SnapshotCache and registers
  SecretDiscoveryService on a loopback server. This exposes StreamSecrets,
  DeltaSecrets, and FetchSecrets; tests use Fetch. It is separate from the main
  ADS server and its per-client publication gate.
- Evidence added: the direct `secrets` scenario characterizes rotation,
  removal, and a never-delivered secret over SotW ADS (RF-027). Removal is
  ACKed and revokes nothing.
- Evidence added: `TestSDSStreamAndDeltaServeAnyNodeIdentity` shows the
  standalone server answers StreamSecrets and DeltaSecrets, like
  FetchSecrets, with the configured client's snapshot for any Node identity.
  All three RPC modes are reachable; none authenticates.
- Action: model secret rotation, deletion/revocation, last-good versus
  fail-closed behavior, version reuse, reconnect, identity, and partial send.
  Determine which three RPC modes are supported and disable unused modes or
  add executable evidence. Availability fixes must not retain revoked secrets.

## RF-021 Main ADS carries an under-modeled secret dependency graph

- Status: resource-family inventory corrected; full closure and lifecycle open.
- Evidence: main per-client snapshots contain Cluster, Endpoint, Route,
  Listener, and Secret resources. Secrets travel through ADS without a
  separately registered SecretDiscoveryService on that server. The formal
  closure work concentrates on route/cluster/endpoint edges, while the concrete
  checker has a larger, necessarily fallible traversal of listener, transport
  socket, HTTP filter, formatter, and nested typed-config secret references.
- Evidence added: `xdscheck.DependencyGraphOf` extracts the reference graph the
  checker traverses, including every secret edge it recognizes, and marks
  resources with unreadable typed configs opaque (RF-002 partition).
- Evidence added: the direct `secrets` scenario covers the downstream TLS
  certificate edge for rotation, removal, and absence (RF-027).
- Action: extract a versioned resource/reference inventory from emitted protos,
  cover every supported secret edge and deletion/rotation schedule, and add
  held-out fixtures for typed configs the checker cannot unpack. Relate cache
  publication to Envoy validation, warming, worker activation, and traffic.
  Do not infer resource coverage from the list of registered gRPC services.

## RF-022 Stale nonce discards subscription changes before cache admission

- Status: finite schedule characterized against the pinned cache/server;
  real-client starvation and protocol-policy disposition remain open.
- Evidence: `TestStaleNonceDropsSubscriptionChangeAndWatch` runs default and
  ordered ADS. After sending a newer response, the server observes an older
  nonce with expanded names in its callback, but admits no replacement cache
  watch. A subsequent coherent snapshot has no waiter. Repeating the names
  with the current nonce receives that snapshot. A second request callback
  provides a serial-loop barrier before inspecting the first stale request's
  outcome; callback receipt alone does not establish subscription admission.
- Source: pinned v0.14.0 `pkg/server/sotw/v3/{xds,ads}.go` explicitly discards
  stale-nonce requests before updating subscriptions and cites Envoy #10363.
- Action: model callback observation, subscription admission, response nonce,
  and pending client requests separately. Construct an actual Envoy schedule
  and establish fairness/retry assumptions before claiming sustained starvation.
  Audit versioned protocol requirements before changing nonce handling; this
  probe records existing behavior and does not establish that ignoring stale
  ACKs is itself a protocol defect.

## RF-023 SDS isolation relies on reachability, not the configured client name

- Status: Fetch identity behavior characterized; deployment exposure audit open.
- Evidence: `TestSDSFetchDoesNotAuthorizeNodeIdentity` uses the production gRPC
  registration/options and cache with an in-memory transport and synthetic
  public resource. Absent, matching, and different Node IDs receive the same
  snapshot without credentials. `Server.ID` ignores Node and returns the
  configured cache key; the SDS gRPC options install no authentication.
- Scope correction: `pkg/sds/run.go` defaults `SdsServerAddress` to loopback
  but reads it from environment. `Server.Run` binds the supplied address.
  Loopback is a default deployment boundary, not an enforced server invariant.
- Evidence added: StreamSecrets and DeltaSecrets behave like FetchSecrets:
  any Node identity receives the configured client's secrets.
- Action: audit chart/environment overrides and network reachability; decide
  whether to enforce loopback or authenticate supported remote clients. Model the configured
  client as a cache key and represent the actual trust boundary explicitly.
  This synthetic probe does not establish exposure in an installed deployment.

## RF-024 Backend translation without a plugin is dropped, not errored

- Status: reproduced with a synthetic plugin registration; not reachable with
  the three built-in backend plugins; fix open.
- Evidence added: `TestNilBackendTranslationIsDroppedNotErrored` builds the
  real per-client cluster collection with a contributed group/kind whose
  `BackendInit` has no `InitEnvoyBackend` and with an unregistered group/kind.
  Neither backend produces a row, while a backend with pre-existing errors
  produces a named errored row. `TranslateBackend` does return an error for
  both dropped backends; the collection discards it with the nil cluster. Fed
  to `findMissingReferencedClusters`, both names are nonexempt missing
  references. The test pins the drop and must be inverted when the fix lands.
- Evidence: `NewPerClientEnvoyClusters` skips a backend whenever
  `TranslateBackend` returns a nil cluster (`backends.go`). That happens when
  the backend's group/kind has no contributed translator or the contributed
  `BackendInit` has no `InitEnvoyBackend`. The returned error is discarded
  with the cluster, so the backend appears in neither the CDS resource set nor
  `erroredClusters`. Every other failure path returns a named blackhole
  cluster with an error and is recorded as errored.
- Consequence: a route that references such a backend has a permanently
  missing, nonexempt cluster. Under the research policy this withholds a cold
  proxy's entire first publication (RF-003) and holds a warm proxy's
  RDS/LDS/SDS flip indefinitely (RF-002). On the baseline policy the same
  input yields a per-route 503 for RDS routes and a listener rejection for
  inline or TCP proxy routes. The policy converts a per-route failure into a
  per-proxy one.
- Action: record a nil translation result as an errored cluster so the
  existing fail-closed exemption applies, or reject such backends at
  collection time with a status condition. Add a probe with a synthetic
  backend plugin lacking `InitEnvoyBackend` and assert the resulting
  `erroredClusters` entry. Audit extension plugins outside this repository
  for the same registration shape before treating the path as unreachable.

- Fix status (2026-09-11): a fix exists on a main-based branch, commit
  0841e73066 on `worktree-kxds-rf024-nil-translation-errored` (based on main
  c6fab73abb, not pushed), written by a separate session working from this
  ledger. `TranslateBackend` returns `buildBlackholeCluster(backend)` with the
  error on both defensive checks, so the errored-cluster machinery records
  the row, excludes it from CDS, filters its CLA, and reports status; the nil
  guard in `NewPerClientEnvoyClusters` remains and logs the discarded error.
  Tests there: `TestBackendTranslatorReturnsBlackholeForUnsupportedBackendKinds`
  and `TestUnsupportedBackendTranslationIsRecordedAsErrored`, the inversion of
  this branch's `TestNilBackendTranslationIsDroppedNotErrored`. Not ported to
  the research branch: the remedy choice (errored record versus collection-
  time rejection) is the maintainer decision listed in the plan, and the
  port must replace the receipt test with the inverted one. Remaining action
  from that commit: audit out-of-tree plugins for a `BackendInit` without
  `InitEnvoyBackend`.
- Second instance, shared-base CDS stack (head 5da2acd8ec; PRs #14600,
  #14691 to #14695, #14604), relayed by the review session and verified here
  read-only at that head: `TranslateBackendBase` in
  `pkg/kgateway/translator/irtranslator/backend.go` returns nil for a
  group/kind with no contributed translator or a `BackendInit` without
  `InitEnvoyBackend`, and the base transform in `proxy_syncer/backends.go`
  turns a nil base into no row (as does its renamed-cluster guard), so the
  stack drops the same backends with no errored record, no CLA filtering,
  and no status. The stack's `TestTranslateBackendBase_NilForUnsupportedGroupKind`
  pins the drop and must be inverted with the fix; the main-branch fix
  0841e73066 does not apply there because `TranslateBackend` no longer
  exists in that shape. Action: whichever lands first, the other needs the
  same blackhole-with-error return in `TranslateBackendBase` and a row for
  the errored base.
- Stack update (2026-09-12): done in the stack at fae9ef8cd8 ("never return
  a nil base") and e004fa5920 ("record renamed and unsupported backends as
  errored bases"); `TranslateBackendBase` returns the blackhole cluster with
  the error on both checks. The main-branch fix 0841e73066 remains the fix
  for main without the stack.

## RF-025 EDS version strings move without content changes

- Status: observed in required trace runs by the schema-2 version relation;
  counted as `version-churn`, not failed; production reachability of one
  mechanism confirmed by source, the other needs a real-translation trace.
- Evidence: the required runner's `TestSnapshotPerClientRandomizedEventSequencesConformToSpec`
  trace records twelve publications whose (CLA name, digest) content equals
  the client's previous publication while the EDS version string differs.
  `TestSnapshotPerClientEndpointOnlyUpdateOnlyChangesEDSVersion` records one.
  No `version-reuse` violation occurred in any scenario.
- Mechanism 1, carry-forward suffix: `resolveDeferredPerCluster` versions a
  held-flip composition as `<candidate filtered version>-carry-<hash of
  carried names>`. When the carried set restores exactly the previously
  published content, the version still moves. The trace shows a candidate
  whose filtered set was one CLA (its version equals that CLA's digest)
  gaining the carried CLA back and publishing identical content under a new
  version. This is implementation behavior, not a fixture artifact.
- Review note, PR #14604 (head 5da2acd8ec, unmerged): the per-client EDS
  version string is the XOR of every row's EndpointsHash
  (`perclient.go`, the EndpointResources transform), and the PR replaces the
  row hash `LbEpsEqualityHash ^ additionalHash` with an FNV-1a fold of three
  parts. Every EDS version string therefore changes on upgrade to a build
  carrying the PR while no CLA content changes: one content-identical EDS
  push per connected client, which Envoy ACKs; names are unchanged so no
  warming or rejection follows. This is a one-time instance of this
  finding's churn, and the trace rule would count it as `version-churn`
  across an upgrade boundary. The local-cluster row hash is separate and
  unchanged.
- Mechanism 2, branch-dependent version function: `filterEndpointResourcesForClusters`
  returns the endpoint collection's own version (`EndpointsHash`, derived
  from translation inputs in `cla.go`) when nothing is dropped or
  synthesized, and an XOR of per-CLA proto digests otherwise. The same
  content therefore has two version strings depending on whether an
  unrelated CLA was filtered or synthesized in that revision. Unit fixtures
  fabricate `EndpointsHash`, so the randomized trace exaggerates this; the
  branch flip itself is reachable in production whenever a CDS removal
  precedes its CLA removal, or an EDS cluster's CLA arrives after the cluster.
- Corroboration: Envoy Gateway #8889 reports content-equivalent listener
  updates draining active WebSocket connections, and Envoy #46383 reports
  large repeated CDS pushes deferring EDS subscriptions at scale; churn has
  data-plane costs beyond CPU.
- Consequence: each occurrence is one spurious EDS push per client. Envoy
  re-applies identical endpoints; go-control-plane answers because the
  version differs. This is a cost and observability issue, not a safety
  violation, and it is the same class that PR #14516 fixed for label hashing.
  It also shows that `EndpointsHash` and the proto digest are not the same
  version function, so IMPL-A1 must be stated for both.
- Evidence added: `TestSnapshotPerClientRealEndpointTranslationVersionRelation`
  runs the real endpoint translation through snapshotPerClient and syncXds
  as a trace scenario. Adding a pod changes the EDS version and the CLA;
  removing it restores the CLA and the same version string, so on this path
  `EndpointsHash` is a content-idempotent function of the endpoint set and
  the version relation sees production-shaped versions with no reuse and
  no churn. The two churn mechanisms above remain the carry suffix and the
  branch-dependent version function, neither of which this scenario reaches.
- Action: version the EDS resource set from the published CLA protos on every
  branch, including the carried composition, so equal content yields equal
  version; or document the churn as accepted. Emit traces from a run with the
  real `translateEndpoints` (envtest or e2e) and apply the version relation to
  check that `EndpointsHash` moves exactly when CLA content moves; the unit
  fixtures cannot establish that. Decide whether `version-churn` becomes a
  failure once fixtures stop fabricating versions.
- Fix (2026-09-12): branch `chandler/xds-eds-content-version`, commit
  38869f7c37, based on main 23c2c8ea40 (worktree
  `~/git/kgateway/.claude/worktrees/xds-eds-content-version`, not pushed).
  Each endpoint row carries `ContentHash`, a digest of the built assignment;
  the client's EDS version is a sorted FNV-1a fold of (name, content digest,
  cluster version digest) over the published set, recomputed after the
  static and errored filters. The `-errors-` suffix and the XOR of input
  hashes are gone, so input-only changes (policy generation bumps, plugin
  contributions resolving identically, an upgrade that changes the input
  fold) no longer push, and a filtered set shares its version with a
  directly built set of equal content.
- Design finding from that fix, relevant to RF-014 and RF-017: the input
  hash's policy fold was load-bearing. A BackendTLSPolicy change alters the
  cluster, Envoy rebuilds it and re-requests EDS at its accepted version,
  and only a moved EDS version answers that request; content-only
  versioning would have left every policy-driven rebuild warming until the
  bootstrap's 15 s EDS fetch timeout (`TestPerClientSnapshotUpdatesWhenBackendTLSPolicyConflictsAddedLater`
  caught it). The fix therefore folds each assignment's cluster version in,
  which re-pushes an unchanged assignment exactly when its cluster changed
  and never otherwise. `version-churn` in the trace checker will count
  those CDS-coupled pushes; they are the RF-014 requirement, not churn, and
  the rule should be refined to exempt an EDS push whose cluster version
  moved in the same publication before churn becomes a gate failure.

## RF-026 SotW rejection applies the valid resources of a NACKed response

- Status: directly characterized on Envoy v1.39.1 for CDS, LDS, EDS, RDS,
  and SDS; the semantics differ by type; model fidelity gap open; Delta
  untested.
- Evidence: `envoyprobe -scenario references` sends a CDS response with a
  valid change to cluster `a` and an invalid cluster `bad`. Envoy NACKs the
  response, its request keeps the previous version, and `a` is nevertheless
  active with the new connect timeout at the rejected version. The LDS phase
  repeats this with a valid stat-prefix change to `front` and an invalid
  inline listener: `front` is active with the new prefix while its dump
  version stays at the previous version. Receipts are in the `references`
  artifact directory of `check-envoy.sh`.
- Model fidelity: `XdsAdsSotw.tla`'s `ClientNack` leaves accepted state and
  the applied snapshot unchanged, and the Lean convergence machine has no
  partial-acceptance transition. Both under-approximate Envoy: after a NACK
  the client can be running a mixture of the previous and the rejected
  response. The syncXds comment that a rejected snapshot leaves the client on
  its previous configuration is wrong at Envoy as well as at the cache
  (RF-007).
- Model added: `PartialRejection.lean` states both semantics for a two-resource
  response. Over a bounded graph the atomic abstraction violates
  `ValidSiblingIsolated` and the observed partial semantics violates
  `AcceptedVersionDescribesApplied`; `ServerViewMatchesClientVersion` holds
  under both because the NACK request reports the old version honestly. The
  invariants that survive partial acceptance are therefore those stated over
  applied content, not over accepted versions.
- kgateway audit: `pkg/kgateway/setup/envoy_error.go` is the only NACK
  consumer. It increments `envoy_xds_rejects_total`, raises
  `envoy_xds_rejects_active` per gateway and type, and logs once per
  (stream, gateway, type). No status condition, readiness signal, rollback,
  or resend suppression is derived from ACKs or NACKs, so no kgateway logic
  currently infers applied configuration from accepted versions. The metric
  cannot distinguish a wholesale rejection from one whose valid siblings
  applied; both raise the same per-type gauge.
- Cache path: through SnapshotCache the rejected snapshot is resent on every
  NACK (RF-012), so the partial application recurs thousands of times per
  second until a corrected snapshot arrives; applied state and traffic were
  unchanged across the window.
- Evidence added: `envoyprobe -scenario rejection` shows EDS and RDS behave
  differently. A CLA that fails proto constraint validation and a route
  configuration whose regex fails to compile each reject the whole response;
  the valid sibling CLA and route configuration are not applied, and both
  RDS subscriptions log the rejection. Partial acceptance is a property of
  the CDS and LDS API implementations, which apply resources one at a time,
  not of SotW handling in general. The cache path resends the rejected EDS
  and RDS versions on every NACK at the same rate as CDS and LDS.
- Corroboration: Envoy #32880 asks the protocol to specify per-resource
  versus per-response NACK semantics; the measurements here answer it per
  type for v1.39.1. Envoy Gateway #9463 reports one invalid TLS secret
  rejecting an entire SDS update; the secrets scenario now reproduces it on
  the pin: a valid rotation beside a key-mismatched sibling is not applied.
- Consequence: a NACK does not protect unrelated resources from a response
  that also carries an invalid one; it protects only the invalid resource.
  Isolation is better than the atomic-rollback model predicts, but the
  control plane's accepted-version accounting, and any recovery logic that
  resends "the last accepted version", describe state Envoy is not in. A
  resend of the rejected version re-applies the valid parts idempotently and
  repeats the NACK (RF-012).
- Action: add a per-type acceptance semantics to the ADS and convergence
  models (partial for CDS and LDS, whole-response for EDS and RDS) and
  re-derive which invariants survive; audit kgateway status and
  readiness reporting that infers applied configuration from ACKed versions;
  extend the probe to RDS with several route configurations, EDS with several
  CLAs, SDS, and the SnapshotCache path where the rejected version is resent
  on every NACK; and record whether Delta xDS behaves the same.

## RF-027 SDS removal does not revoke a delivered certificate

- Status: directly characterized on Envoy v1.39.1 over SotW ADS; kgateway
  deletion behavior audit open.
- Evidence: `envoyprobe -scenario secrets` rotates a TLS listener's SDS
  certificate in place, then sends an SDS response that no longer carries the
  subscribed secret. Envoy ACKs the empty response, advances its accepted
  version, keeps the secret active in its dump, and keeps completing
  handshakes with the last delivered certificate for the whole observation
  window. A listener whose secret never arrives stays warming and refuses
  connections while readiness and other listeners are unaffected.
- Consequence: at the protocol boundary, removing a Secret resource is not a
  revocation mechanism. Revocation requires replacing the secret's content
  or removing every listener and cluster that references it in the same or
  an earlier publication. Any publication policy that retains or carries
  forward last-good secrets while holding another type (RF-002) inherits
  this: a held SDS type cannot revoke, but neither can an unheld one by
  deletion alone.
- kgateway audit: downstream listener certificates are inlined into LDS by
  the listener translator; a deleted Kubernetes Secret yields
  `InvalidCertificateRef`, the filter chain is not generated, and the
  listener changes or disappears in the same publication, so revocation
  travels with LDS. BackendTLSPolicy CA material is inlined into CDS; a
  missing Secret is an `InvalidCACertificateRef` policy error and the cluster
  fails closed. The only kgateway-emitted SDS resources on the main ADS are
  the OAuth2 client and HMAC generic secrets from the traffic policy plugin;
  a deleted client Secret fails that policy's translation, which removes the
  OAuth2 filter and its two Secret resources in the same publication, so no
  filter keeps referencing the removed secret. The system CA reference is
  the bootstrap static secret, and Istio mTLS secrets come from the
  istio-agent SDS server, outside this control plane. Residual: Envoy keeps
  a removed generic secret loaded until restart, since removal revokes
  nothing; no kgateway filter references it after the policy is dropped.
- Evidence added: `TestTranslatedOAuth2SnapshotDropsFilterAndSecretsWhenClientSecretDeleted`
  translates the OAuth2 fixture with its client Secret removed. Every OAuth2
  HTTP filter and both SDS generic secrets leave the snapshot in the same
  publication, no listener references the client secret, the gateway's
  listeners are still published, and xdscheck reports no errors. kgateway's
  only SDS revocation path therefore removes the reference, as RF-027
  requires.
- Action: state the revocation semantics in the secret model required by
  RF-020 and RF-021 (revocation is reference removal or content replacement,
  never resource deletion); extend the probe to validation-context secrets,
  upstream client certificates, the standalone SDS server in `pkg/sds`, and
  Delta xDS.

## RF-028 Reconnect during rewarming does not re-request CDS

- Status: directly characterized on Envoy v1.39.1 over SotW ADS through the
  real v0.14.0 SnapshotCache in both ADS modes; kgateway exposure follows
  from RF-001 and RF-003 and is not yet exercised end to end.
- Evidence: `envoyprobe -scenario restart` (both `-snapshot-cache` profiles)
  publishes a CDS revision whose cluster `a` has a new connect timeout and
  withholds the EDS revision, so the candidate cluster is warming while the
  old active `a` keeps serving. The ADS server is then stopped and a fresh
  server with an empty cache takes the port. Within the five-second window
  after the reconnect Envoy re-requests EDS at its accepted version `r2`,
  LDS at `r3`, and RDS at `r3`, each with an empty nonce, and never
  requests CDS. The empty cache answers nothing, the candidate stays
  warming, and traffic stays 200 on the old cluster. Publishing an EDS
  revision completes the warming, after which Envoy issues its CDS request
  and the next CDS revision applies. Recorded observations:
  `restart-during-warming` (`cds_requested_within_5s: false`,
  `still_warming: true`, `responses: 0`, `traffic: 200`) and
  `rewarmed-after-restart` (`cds_requested_after_warming: true`) in the
  gate artifacts for `restart-cache` and `restart-ordered-cache`.
- Mechanism: while a cluster is warming Envoy pauses CDS discovery until the
  warming cluster initializes; a stream reconnect during that pause resumes
  the other types but the paused CDS subscription is not re-sent until the
  pause lifts. Envoy #36951 and #34334 report this shape over Delta xDS; the
  same shape holds over SotW on the pinned binary, so those corpus entries
  move from "Delta only" to "shared mechanism, reproduced over SotW".
- Consequence for kgateway: a controller restart or ADS stream reset while a
  proxy is rewarming a cluster leaves that proxy without a CDS request. Any
  publication policy that withholds EDS for that client (RF-001 cold gate,
  RF-002 and RF-003 whole-type holds, or RF-017's parked equal-version
  rewarming) keeps the candidate warming, and CDS cannot progress until an
  EDS response for the warming cluster arrives. The proxy is not broken in
  the interim: the previously active cluster keeps serving. But a CDS-only
  repair, such as removing the misconfigured cluster, cannot reach the proxy
  until EDS is released. On the deployed path this is the RF-014 same-version
  EDS requirement composed with a reconnect: after a restart the new cache
  entry's EDS version is typically equal to the proxy's accepted version,
  so the reconnect EDS request parks under the equal-version rule and the
  warming never completes until content changes.
- Not covered: Delta xDS, and the
  initial-fetch-timeout interaction (warming clusters with no timeout wait
  indefinitely; RF-003 recorded the timeout defaults).
- Bootstrap correction (same day): kgateway's Envoy bootstrap enables
  `envoy.restart_features.use_eds_cache_for_ads`, and its EDS and RDS config
  sources leave `initial_fetch_timeout` unset, which is Envoy's 15 s default
  (the bootstrap's CDS and LDS sources are the ones set to 0s). The probe had
  written an explicit 0s on every EDS and RDS source, which disables the
  timeout and with it the EDS cache fallback. Remeasured with `-eds-cache`:
  with the timeout disabled the pause holds exactly as above; with the
  timeout unset (kgateway's shape) the warming candidate completes from the
  cached ClusterLoadAssignment when the timeout expires, Envoy re-requests
  EDS at the same accepted version and then CDS, and the next CDS revision
  applies (`restart-cache-kgateway-bootstrap` profile, observation window
  20 s). The scripted server never answered EDS; the cache did.
- Corrected consequence for kgateway: the stuck-repair window after a
  reconnect during rewarming is bounded by the EDS initial fetch timeout,
  15 s per warming episode in the deployed bootstrap, for any cluster whose
  ClusterLoadAssignment Envoy has already received under that name. It is
  unbounded only if a deployment sets that timeout to 0s on EDS sources, or
  for a cluster Envoy has never received an assignment for, where the same
  timeout activates the cluster empty instead (RF-003 timeouts scenario).
  The same fallback bounds the RF-014 and RF-017 same-name rewarming parks
  in the deployed bootstrap; those findings were measured with the timeout
  disabled and their indefinite duration applies to that shape only.
- Repair candidates, re-ranked: (a) leave as is and document the 15 s
  bound; (b) set an explicit shorter `initial_fetch_timeout` on kgateway's
  EDS config sources to shrink the window, at the cost of activating a
  never-received cluster empty sooner (RF-003 trade-off); (c) the
  unconditional first response after connect, which removes the window for
  same-name clusters without touching the timeout. `ReconnectWhileWarming.tla`
  carries (c) as `FirstResponseUnconditional` and the fallback as
  `EdsCacheFallback`; both configurations pass. Which to adopt is still the
  maintainer decision.
- Fix (2026-09-12, option c): branch `chandler/14184-stack-respond-on-reconnect`
  on top of the gh stack's `chandler/14184-stack-eds-content-version`
  (worktree `~/git/kgateway/.claude/worktrees/14184-stack-respond-on-reconnect`,
  not pushed). The cache decorator that carries the NACK rule (renamed
  `watchPolicy`) gains a second rule: a request with no error detail, no
  nonce and a held version is a reconnecting proxy's first request; for the
  endpoint type the decorator clears the version so the current or next
  snapshot answers it. Setting `KGW_XDS_RESPOND_ON_RECONNECT`, default on;
  counter `envoy_xds_reconnect_responses_total`. Re-measured first: on the
  new go-control-plane pin the empty-cache restart pause is unchanged and an
  equal-version republish to the parked watch is still silent, while against
  a warm cache the pin already answers a fresh stream's first request of any
  type (nothing returned on the stream yet), so the rule is scoped to the
  empty-cache-then-republish path and to EDS, the type a warming cluster
  waits on. Both facts are pinned by tests on the undecorated cache.
- Live evidence (2026-09-11): `TestKgateway/XdsWarming/TestRouteUpdateSurvivesControllerRestartWhileNewClusterWarms`
  passed on a fresh `xdsformal` kind cluster against the research image
  built from c23bf8591e (production code unchanged since). The route is
  retargeted to a service with no endpoints, the old cluster keeps serving,
  the kgateway deployment is restarted in that window, old traffic is
  asserted consistent across the restart, and after the new backend appears
  the reconnected proxy activates the new cluster and serves it. The three
  pre-existing XdsWarming tests passed in the same run (four of four). The
  run is pass/fail without a snapshot trace (RF-009), so it establishes the
  observable contract (no old-traffic break, convergence after the restart
  without a second input change), not the CDS pause or the 15 s bound
  themselves; those remain direct-Envoy measurements.
- Action: (1) treat "reconnect while warming" as a required scenario for
  any per-client publication gate: after a restart, a client whose accepted
  EDS version equals the derived version still needs an EDS response for
  the warming cluster, which the equal-version park does not send (RF-014,
  RF-017); a version bump on reconnect for clients with warming candidates,
  or an unconditional first response after connect, are the candidate
  repairs and need a decision. (2) Extend the live `XdsWarming` suite with a
  controller restart during a cluster rewarm: done, see the live evidence
  above. (3) Done: `ReconnectWhileWarming.tla`
  models the paused-CDS reconnect; the current equal-version rule fails
  `EventuallyRepaired` unless endpoints change, and the proposed
  unconditional first response passes. `XdsAdsSotw.tla` still re-requests
  every type on reset; the pause lives in the dedicated model, matching how
  RF-003 and RF-005 were treated.

## RF-029 PR #14604 review candidates: retainer race, nested equality cost, hash input coverage

- Status: candidates relayed from a review session of kgateway PR #14604
  ("proxy_syncer: intern equivalent per-client CLAs", head 5da2acd8ec,
  unmerged) and checked here against the PR diff, the pinned protobuf
  source, and the pinned krt source. None is reproduced by a test on this
  branch; none is a correctness defect in delivered xDS content.
- Candidate 1, retainer forget race (`pkg/kgateway/proxy_syncer/cla.go` in
  the PR, `claRetainer.forget` called from a `RegisterBatch` delete hook on
  `kgatewayEndpoints`): in the pinned krt every registered handler is a
  `processorListener` with its own queue goroutine (`processor.go`), so the
  derived collection's transform and the delete hook run concurrently with
  no ordering between them. A backend deleted and recreated in quick
  succession can run the recreate's transform, which seeds from and then
  replaces the retained set, before the late `forget` for the delete drops
  that same entry. The next pass then interns from nothing: already stored
  rows keep their proto (their Equals reports no change) while newcomers get
  a fresh instance, so equal clients hold two protos until the next endpoint
  change, contrary to the type comment's convergence claim. Bounded, memory
  only, self-healing on the next content change.
- Candidate 2, nested equality cost: the pinned protobuf
  (`google.golang.org/protobuf` v1.36.12 pre-release) short-circuits only at
  the top level (`proto/equal.go`, identical pointers) and its fast-path
  `equalMessage` in `internal/impl/equal.go` walks every field of nested
  messages with no pointer-identity check. The interner's confirmation
  therefore costs a full walk over every `LbEndpoint` even when the two
  candidates share nested pointers, on every transform pass for every
  client. No benchmark was run here; recorded as the reason a hash-bucket
  hit is not cheap.
- Candidate 3, hash input coverage (main and the PR), resolved covered: on
  main the row hash is `LbEpsEqualityHash ^ additionalHash`, where the first
  covers the endpoint set plus backend policy versioning and the second the
  endpoint plugins' contributions. `PrioritizeEndpoints` also reads the
  client's labels and locality (covered by the Client comparison) and, when
  no plugin set `PriorityInfo`, the backend's `TrafficDistribution`. A
  Service `trafficDistribution` change does reach `LbEpsEqualityHash`:
  `NewEndpointsForBackend` writes the distribution byte into the upstream
  hash, which `pkg/krtcollections` already pins in
  `TestEndpointsForUpstreamWithDifferentTrafficDistributionButSameEndpoints`
  (four distributions, identical endpoints, four distinct hashes), and the
  kubernetes plugin recomputes the backend IR on a spec change because
  `BackendObjectIR.Equals` compares the field. The editor path is different:
  `SetTrafficDistribution` assigns
  without refreshing the hash, so a plugin-driven change is versioned only
  by that plugin's returned contribution; the one production caller
  (BackendConfigPolicy's zone-aware hook) returns the policy reference and
  generation, and its removal drops the contribution to zero, so both
  directions move the row hash. Two tests added here complete the receipt:
  `TestSetTrafficDistributionIsVersionedByThePluginContribution`
  (endpoints) states the editor contract, and
  `TestUccWithEndpointsRowChangesWhenOnlyTrafficDistributionChanges`
  (proxy_syncer) is the row-level Equals check action 1 asked for; the
  existing krtcollections test joins the required receipts. The PR's "omitted the load-balancing context" wording therefore
  describes bucket separation for interning, not a staleness gap on main.
- Action: (1) done, covered (see candidate 3); a future endpoint plugin that
  calls `SetTrafficDistribution` or `SetPriorityInfo` must return a nonzero
  contribution, which the editor test now states. (2) In the PR, either take the
  `forget` under the same ordering as the transform (drop the entry inside
  the transform when the input's delete is observed, or key retained
  entries by input generation) or document the two-proto window. (3) Record
  the upgrade-time EDS version move (RF-025 note) in the PR's release notes.
  (4) The `+noKrtEquals` marker wording should state the injectivity
  assumption (RF-008 note).
- Stack head update (2026-09-12): the review above was against 5da2acd8ec.
  The branch behind #14604 moved to cdd54cda9a (pushed) and ce4e553ab3
  (local, `~/k_14343-as-gh-stack`), and most of this entry is now closed
  there. Candidate 1: `forgetIfAbsent` (1c4a7002d4) drops a retained entry
  only when the backend is absent from the source collection when the delete
  is handled, which covers the late-delete order; `keepIfPresent`
  (ce4e553ab3, written today) re-checks presence after the pass stores its
  entry, which covers the mirror order where a second delete is handled
  before the re-add's pass stores and the entry would otherwise leak for a
  gone backend. Candidate 2: `cla_equal.go` compares CLAs with an
  identity-aware walk that short-circuits shared nested pointers and falls
  back to `proto.Equal` for fields it does not model. Action (4): the
  `+noKrtEquals` wording now states the collision assumption and what relies
  on it (1aab97a66e). The relayed annotation item: `objectContentEquals` now
  compares generation and labels only (3a091286c2). Still open: action (3),
  the PR body's release note is `NONE` while every EDS version string moves
  once on upgrade.
- Relayed, not verified here (same review, head 5da2acd8ec): the stack's
  `objectContentEquals` compares object annotations that nothing per-client
  reads, so an annotation-only change on a backend recomputes every client's
  row for it, a fan-out amplifier in the RF-025 family rather than a
  staleness. Recorded for the stack's review; no ledger action beyond this.
