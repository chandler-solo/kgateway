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
- Action: choose and implement an explicit outcome for a reference that stays
  unresolved: classify as errored after a bound derived from the startup
  probe budget, or surface a status condition and keep waiting. Do not
  synthesize a security-sensitive replacement cluster or assume a future
  ready event. Verify that the chosen policy refines the model's
  `Classify` step, and reproduce the warm variant: a permanently missing
  newly referenced cluster holds RDS/LDS/SDS the same way RF-002's empty
  backend does.

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
- Action: extend the pinned direct harness to SDS absence/rotation,
  multi-resource RDS/EDS rejection, initial-fetch timeouts, restart, and
  worker application observations.

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
  decisions counted. One transform-only test now also installs and asserts
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

- Status: dependency defects characterized at root module v0.14.0; model refinement open.
- Lineage: replaying the probes against unreleased upstream `bf9b60b56`
  (#1356) and `1cd122661` (#1498) shows the parked declined watch is retained
  there (GCP-A5 repaired expectation) while the request-entry decline still
  registers no watch. No root-module release contains either change as of
  the 2026-09-04 checkout. See `gcpprobe/README.md` profile diff and
  `gcpprobe/replay-profile.sh`.
- Evidence: `CreateWatch` responds at equal version for new unreturned names;
  declined immediate requests are not registered; `respondSOTWWatches` deletes
  declined parked watches. `SetSnapshot` installs before sends can fail.
- Evidence added: `gcpprobe` reproduces both paths, equal-version subscription,
  and partial installation. `GcpWatch.lean` checks discard and retention policies.
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

## RF-009 Required evidence execution and CI coverage

- Status: required unit receipt validation and broader CI implemented; full
  lifecycle/e2e and extended-bound coverage remain open. Required Lean runner
  fails on missing tools, runs full
  relevant unit packages, isolates snapshot scenarios, rejects skips, and
  retains logs/JSON receipts. Declaration mapping now requires explicit status.
- Evidence added: `checkreceipts` rejects absent, skipped, failed, malformed,
  or truncated required test outcomes. Workflow runs on all PR changes, uploads
  receipts, and includes bounded TLC and pinned direct Envoy.
- Action: add lifecycle trace receipts and live KGW e2e execution. Existence of
  a named Go function is not execution; RF-015 tracks original TLC bounds.

## RF-010 Ordered delivery does not prove remote application

- Status: open protocol/composition limitation.
- Evidence: ordering probes exhibit ACK-skew and removal windows in both modes;
  Lean graceful removal requires observed deactivation. Setup now supports
  `EnableOrderedAds`, so earlier documentation of non-adoption was stale.
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
- Evidence: `TestRepeatedNackResendsSameVersionAndCorrectionRecovers` drives
  32 repeated NACKs through the actual cache/server and receives the same
  version with fresh nonces, then receives a corrected snapshot.
- Evidence added: `envoyprobe -scenario references -snapshot-cache` measures
  the recurrence with a real Envoy v1.39.1: about 6,800 CDS NACK round trips
  and about 3,900 LDS NACK round trips in two seconds, in both ADS modes,
  each re-applying the valid siblings (RF-026) and re-rejecting the invalid
  resource; a corrected snapshot ends it. One machine, concurrency one.
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
  v1.39.1; mitigation remains open.
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

## RF-018 Unsubscribe-all is treated as wildcard by snapshot responses

- Status: reproduced cache and wire defect in root v0.14.0, both ADS modes.
- Evidence: `TestUnsubscribeAllStillReceivesResourcesFromCache` covers immediate
  and parked response paths. `TestUnsubscribeAllResourceLeaksOnWire` uses the
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
  and decide whether kgateway adopts a pseudo-version or waits for a release.
  KGW deployment reachability and the complete stream/queue refinement remain
  open.

## RF-016 Historical inventory is not completed bug coverage

- Status: all seven title/body inventories captured; detailed classification and linked-source audit open.
- Evidence: the exporter records every retrieved issue/PR page and cursor,
  cutoff, repository identity, payload checksum, keyword hits, and unreviewed
  dispositions. GitHub rejected page-number pagination at Envoy page 100;
  following server-supplied cursors is necessary for large repositories.
- Evidence added: `corpus-inventory.json` records completed enumeration;
  `bug-corpus.json` records eleven discovery dispositions, with their detail pages
  captured privately. Gloo/kgateway repository IDs prevent lineage loss.
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
- Action: audit chart/environment overrides and network reachability; decide
  whether to enforce loopback or authenticate supported remote clients. Extend
  characterization to StreamSecrets and DeltaSecrets. Model the configured
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
- Mechanism 2, branch-dependent version function: `filterEndpointResourcesForClusters`
  returns the endpoint collection's own version (`EndpointsHash`, derived
  from translation inputs in `cla.go`) when nothing is dropped or
  synthesized, and an XOR of per-CLA proto digests otherwise. The same
  content therefore has two version strings depending on whether an
  unrelated CLA was filtered or synthesized in that revision. Unit fixtures
  fabricate `EndpointsHash`, so the randomized trace exaggerates this; the
  branch flip itself is reachable in production whenever a CDS removal
  precedes its CLA removal, or an EDS cluster's CLA arrives after the cluster.
- Consequence: each occurrence is one spurious EDS push per client. Envoy
  re-applies identical endpoints; go-control-plane answers because the
  version differs. This is a cost and observability issue, not a safety
  violation, and it is the same class that PR #14516 fixed for label hashing.
  It also shows that `EndpointsHash` and the proto digest are not the same
  version function, so IMPL-A1 must be stated for both.
- Action: version the EDS resource set from the published CLA protos on every
  branch, including the carried composition, so equal content yields equal
  version; or document the churn as accepted. Emit traces from a run with the
  real `translateEndpoints` (envtest or e2e) and apply the version relation to
  check that `EndpointsHash` moves exactly when CLA content moves; the unit
  fixtures cannot establish that. Decide whether `version-churn` becomes a
  failure once fixtures stop fabricating versions.

## RF-026 SotW rejection applies the valid resources of a NACKed response

- Status: directly characterized on Envoy v1.39.1 for CDS and LDS; model
  fidelity gap open; RDS/EDS/SDS and Delta untested.
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
- Consequence: a NACK does not protect unrelated resources from a response
  that also carries an invalid one; it protects only the invalid resource.
  Isolation is better than the atomic-rollback model predicts, but the
  control plane's accepted-version accounting, and any recovery logic that
  resends "the last accepted version", describe state Envoy is not in. A
  resend of the rejected version re-applies the valid parts idempotently and
  repeats the NACK (RF-012).
- Action: add a partial-acceptance transition to the ADS and convergence
  models and re-derive which invariants survive; audit kgateway status and
  readiness reporting that infers applied configuration from ACKed versions;
  extend the probe to RDS with several route configurations, EDS with several
  CLAs, SDS, and the SnapshotCache path where the rejected version is resent
  on every NACK; and record whether Delta xDS behaves the same.
