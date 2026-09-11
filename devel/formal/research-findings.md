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

- Status: open implementation limitation.
- Evidence: `syncXds` still defers with no cache and nonempty `missingReferenced`.
- Action: reproduce permanent derivation failure separately from transient lag
  and select an explicit fail-closed/degraded outcome. Do not synthesize a
  security-sensitive replacement cluster or assume a future ready event.

## RF-004 Envoy activation assumption is unproven

- Status: partly characterized directly; ENV-A1 remains open beyond tested profiles.
- Evidence: warming e2e tests run through the kgateway gate; the initial-route
  test adds a host to a running gateway. They do not establish an independent
  Envoy usable-endpoint activation guarantee.
- Evidence added: `envoy-characterization.md` records direct missing/empty/ready,
  same-version rewarming, unhealthy and endpoint-loss probes in both panic modes.
- Action: extend the pinned direct harness to missing CDS, multi-resource NACK,
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
- Evidence added: successful test completion emits a terminal scenario/event
  count under the emitter lock. Lean rejects missing/mismatched receipts,
  duplicate terminals, and subsequent events. Negative guards cover suffix
  loss; the required runner still enumerates scenarios independently.
- Action: add subtest/stream generations and stateful cache/watch/wire/
  application replay. Terminal counts cover emitted events, not transitions
  omitted by instrumentation; audit transition coverage separately.

## RF-007 go-control-plane response model omits real behavior

- Status: dependency defects characterized at root module v0.14.0; model refinement open.
- Evidence: `CreateWatch` responds at equal version for new unreturned names;
  declined immediate requests are not registered; `respondSOTWWatches` deletes
  declined parked watches. `SetSnapshot` installs before sends can fail.
- Evidence added: `gcpprobe` reproduces both paths, equal-version subscription,
  and partial installation. `GcpWatch.lean` checks discard and retention policies.
- Action: compose the named-watch model with installation and stream lifecycle;
  distinguish installation from response success. A passing defect probe is
  not a fixed cache. Track repaired watch retention and eligibility separately.

## RF-008 Finite hash evidence overstated as injectivity

- Status: assumption reopened; payload/version refinement remains open.
- Evidence: finite-width XOR digests cannot be injective over unbounded content;
  the current Lean version abstraction omits same-name payload changes.
- Action: add payload revisions and a collision assumption or revision allocator;
  retain finite-corpus tests as characterization only.

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
- Action: enforce subscription-aware eligibility/filtering at cache and send
  boundaries, and test empty responses, full-state types, wildcard history,
  and queued supersession. GCP PR #1498 adds a send-time subscription filter
  in newer source; validate its version ancestry and this real-cache schedule
  before calling the branch dependency fixed. KGW deployment reachability and
  the complete stream/queue refinement remain open.

## RF-016 Historical inventory is not completed bug coverage

- Status: all seven title/body inventories captured; detailed classification and linked-source audit open.
- Evidence: the exporter records every retrieved issue/PR page and cursor,
  cutoff, repository identity, payload checksum, keyword hits, and unreviewed
  dispositions. GitHub rejected page-number pagination at Envoy page 100;
  following server-supplied cursors is necessary for large repositories.
- Evidence added: `corpus-inventory.json` records completed enumeration;
  `bug-corpus.json` records ten discovery dispositions, with their detail pages
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
