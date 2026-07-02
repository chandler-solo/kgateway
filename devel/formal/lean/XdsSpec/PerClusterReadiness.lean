/-
XdsSpec.PerClusterReadiness: the guard-#3 granularity question.

`snapshotPerClient` gates publication on per-cluster facts ("does this
referenced EDS cluster have a usable endpoint?") but applies the verdict
to the WHOLE per-client snapshot. Two production-motivated changes pull
that gate in opposite directions, and this model shows both are wrong at
snapshot granularity:

  - The strengthened whole-snapshot gate (defer unless every referenced
    cluster has a usable endpoint) livelocks on backends that sit at
    zero endpoints by design, and — worse — when a Service scales down
    to zero it can never publish the now-empty ClusterLoadAssignment,
    so Envoy keeps routing to the dead endpoints. That is the original
    "traffic to upstream endpoints which no longer exist" complaint,
    reintroduced by design. (`wholeSnapshotDeferBugSystem`, caught as a
    liveness violation whose stuck state shows Envoy holding stale
    usable endpoints while truth is empty.)
  - The demoted gate (publish regardless) lets Envoy finish warming a
    newly referenced cluster on an EMPTY ClusterLoadAssignment — an
    empty EDS response completes warming — and flip routes onto it,
    opening a 503 window. (`publishWhileWarmingBugSystem`, caught as a
    safety violation of `FlipWasGated`.)

The safe system encodes the synthesis — per-cluster make-before-break:

  C1  steady state: publish the cluster's current truth.
  C2  a PREVIOUSLY-ACTIVE cluster scales to zero / goes unhealthy:
      publish the empty CLA anyway. Truth wins; only that cluster's
      routes degrade, and dead endpoints are actually removed.
  C3  a NEWLY-REFERENCED cluster is still warming: hold the route flip
      (and only the flip) until its CLA has a usable endpoint. Other
      clusters' updates keep flowing — deferral is only ever justified
      for a transition, never for steady-state emptiness.

Cast: cluster A (previously active, routes target it from the start;
its endpoints may scale away) and cluster B (newly referenced when the
routes are retargeted; starts with no endpoints). This is the minimal
instance that distinguishes C2 from C3 — the conflation of the two is
exactly the bug in both guard-#3 variants.

This model is checked exhaustively by the model checker (Main.lean);
unlike the convergence spec it carries no unbounded proofs yet — the
state space is tiny and the two bug systems are the regression gates
that matter. Pruning of removed clusters (C4) and the carry-forward
pairing constraint (C5: a carried CLA always travels with its CDS
cluster) are covered by the convergence spec's `CacheSnapshotClosed`
and the trace checker's no-orphan-cla rule.

Connection to the implementation: every obligation here is mapped to
the Go test that discharges it in devel/testing/formal-model-map.yaml,
gated by TestFormalModelMap. The safe system is implemented: the
transform records per-cluster gaps on the wrapper instead of deferring
the snapshot, and syncXds resolves them against the currently-published
snapshot (resolveDeferredPerCluster in kube_gw_translator_syncer.go) —
truth publishes for previously-referenced clusters (C2), only the flip
onto a newly-referenced unready cluster is held (C3), and carried
clusters always travel with their CLAs (C5). The C2/C3 behaviors are
covered by pkg/kgateway/proxy_syncer/perclient_percluster_test.go and
fuzzed against the served cache by the randomized property test.
-/
import XdsSpec.Checker

namespace XdsSpec.PerCluster

/-- Endpoint truth for one cluster: at least one usable (healthy)
endpoint, or none. -/
inductive EP
  | usable | empty
  deriving DecidableEq, Repr, Hashable

structure PCState where
  /-- Cluster A's actual endpoint state (Kubernetes truth). -/
  aTruth : EP
  /-- Cluster B's actual endpoint state. -/
  bTruth : EP
  /-- Desired routes: false = old routes (target A only),
  true = new routes (target A and the newly referenced B). -/
  routesDesired : Bool
  /-- Published routes in the per-client snapshot. -/
  pubRoutes : Bool
  /-- Published CLA content per cluster; `none` = not in the snapshot. -/
  pubA : Option EP
  pubB : Option EP
  /-- Envoy's active state. -/
  actRoutes : Bool
  actA : Option EP
  actB : Option EP
  /-- Ghost: the last route flip was gated on B having a usable
  endpoint. The control plane owns this gate; Envoy will happily
  activate routes onto a cluster whose warming completed on an empty
  CLA. -/
  flipGated : Bool
  /-- Cluster A's translation is errored (e.g. its BackendTLSPolicy became
  invalid). An errored cluster must FAIL CLOSED: it leaves the published
  CDS so its routes 5xx, and is never resurrected from a previous
  snapshot — Gateway API conformance requires requests to a backend
  targeted by an invalid BackendTLSPolicy to receive a 5xx. -/
  aErrored : Bool
  deriving DecidableEq, Repr, Hashable

def init : PCState where
  aTruth := .usable
  bTruth := .empty
  routesDesired := false
  pubRoutes := false
  pubA := some .usable
  pubB := none
  actRoutes := false
  actA := some .usable
  actB := none
  flipGated := true
  aErrored := false

inductive PCAction
  /-- Cluster A's Service scales to zero (or all endpoints go
  unhealthy). There is deliberately no scale-up action: the customer's
  probe backends sit at zero endpoints by design, so the model must
  converge without assuming recovery. -/
  | scaleDownA
  /-- Cluster B's Deployment comes up. -/
  | deployB
  /-- The routes are retargeted to additionally reference B. -/
  | retargetRoutes
  /-- Cluster A's translation goes errored (its BackendTLSPolicy becomes
  invalid, a plugin fails, ...). There is deliberately no un-error action:
  the model must fail closed without assuming the operator fixes the
  policy. -/
  | errorA
  /-- C1/C2: publish A's current truth — unconditionally, empty included;
  when A's translation is errored, its truth is ABSENCE (fail closed). -/
  | publishA
  /-- The rejected PR #13976 design: keep serving an errored cluster from
  its last-good config instead of dropping it. -/
  | buggyServeErroredA
  /-- B's cluster and CLA enter the snapshot together (C5) once B is
  referenced; CDS/EDS may advance ahead of the route flip. -/
  | publishB
  /-- C3: the route flip is gated on the newly referenced cluster
  having a usable endpoint. -/
  | flipRoutes
  /-- Envoy ingests published state. -/
  | envoySyncA
  | envoySyncB
  /-- Envoy activates the new routes once it knows cluster B. Warming
  completes even on an empty CLA, so this carries no usable-endpoint
  protection — that is the control plane's job. -/
  | envoyFlipRoutes
  /-- The strengthened whole-snapshot gate: A's truth may publish only
  when EVERY referenced cluster has a usable endpoint. -/
  | buggyPublishAWholeSnapshotGate
  /-- The demoted gate: flip the routes without checking B. -/
  | buggyFlipRoutesUngated
  deriving DecidableEq, Repr, Hashable

def step (s : PCState) : PCAction → Option PCState
  | .scaleDownA =>
    if s.aTruth == .usable then some { s with aTruth := .empty } else none
  | .deployB =>
    if s.bTruth == .empty then some { s with bTruth := .usable } else none
  | .retargetRoutes =>
    if !s.routesDesired then some { s with routesDesired := true } else none
  | .errorA =>
    if !s.aErrored then some { s with aErrored := true } else none
  | .publishA =>
    some { s with pubA := if s.aErrored then none else some s.aTruth }
  | .buggyServeErroredA =>
    if s.aErrored then some { s with pubA := some s.aTruth } else none
  | .publishB =>
    if s.routesDesired then some { s with pubB := some s.bTruth } else none
  | .flipRoutes =>
    if s.routesDesired && !s.pubRoutes && s.pubB == some .usable then
      some { s with pubRoutes := true, flipGated := true }
    else none
  | .envoySyncA =>
    some { s with actA := s.pubA }
  | .envoySyncB =>
    if s.pubB.isSome then some { s with actB := s.pubB } else none
  | .envoyFlipRoutes =>
    if s.pubRoutes && !s.actRoutes && s.actB.isSome then
      some { s with actRoutes := true }
    else none
  | .buggyPublishAWholeSnapshotGate =>
    if s.aTruth == .usable && (!s.routesDesired || s.bTruth == .usable) then
      some { s with pubA := some s.aTruth }
    else none
  | .buggyFlipRoutesUngated =>
    if s.routesDesired && !s.pubRoutes then
      some { s with pubRoutes := true,
                    flipGated := s.pubB == some .usable }
    else none

def describe : PCAction → String
  | .scaleDownA => "ScaleDownA"
  | .deployB => "DeployB"
  | .retargetRoutes => "RetargetRoutes"
  | .errorA => "ErrorA"
  | .publishA => "PublishA"
  | .buggyServeErroredA => "BuggyServeErroredA"
  | .publishB => "PublishB"
  | .flipRoutes => "FlipRoutes"
  | .envoySyncA => "EnvoySyncA"
  | .envoySyncB => "EnvoySyncB"
  | .envoyFlipRoutes => "EnvoyFlipRoutes"
  | .buggyPublishAWholeSnapshotGate => "BuggyPublishAWholeSnapshotGate"
  | .buggyFlipRoutesUngated => "BuggyFlipRoutesUngated"

def mkSystem (name : String) (actions : List PCAction) :
    System PCState PCAction where
  name := name
  init := init
  actions := actions
  step := step
  describeAction := describe

-- MARK: invariants

/-- Envoy never activates routes onto a newly referenced cluster unless
the control plane gated the flip on a usable endpoint (the 503-window
protection the warming e2e suite pins). -/
def flipWasGated (s : PCState) : Bool :=
  !s.actRoutes || s.flipGated

/-- Active new routes imply Envoy knows cluster B. -/
def activeRoutesHaveCluster (s : PCState) : Bool :=
  !s.actRoutes || s.actB.isSome

/-- B's CLA is in the snapshot only while B is referenced (the subset /
no-orphan obligation, C5). -/
def noOrphanB (s : PCState) : Bool :=
  !s.pubB.isSome || s.routesDesired

def invariantList : List (String × (PCState → Bool)) :=
  [ ("FlipWasGated", flipWasGated),
    ("ActiveRoutesHaveCluster", activeRoutesHaveCluster),
    ("NoOrphanB", noOrphanB) ]

-- MARK: liveness predicates

/-- A's published state reflects A's truth: its current endpoints while the
translation is healthy, and ABSENCE when it is errored (fail closed — the
Gateway API BackendTLSPolicy conformance semantics). -/
def truthPublishedA (s : PCState) : Bool :=
  if s.aErrored then s.pubA.isNone else s.pubA == some s.aTruth

/-- A's published state lags its truth (Envoy still holds endpoints that no
longer exist, or still holds a cluster whose translation is errored). -/
def truthLagsA (s : PCState) : Bool :=
  !truthPublishedA s

/-- The retargeted routes are ready to go live: B deployed. -/
def flipPending (s : PCState) : Bool :=
  s.routesDesired && s.bTruth == .usable && !s.actRoutes

def flipDone (s : PCState) : Bool :=
  s.actRoutes

-- MARK: systems

/-- The per-cluster synthesis: every action with both gates in their
per-cluster form. -/
def safeSystem : System PCState PCAction :=
  mkSystem "PerClusterSafe"
    [ .scaleDownA, .errorA, .deployB, .retargetRoutes,
      .publishA, .publishB, .flipRoutes,
      .envoySyncA, .envoySyncB, .envoyFlipRoutes ]

/-- The strengthened whole-snapshot gate (defer unless every referenced
cluster is usable). `deployB` is deliberately absent: probe backends sit
at zero endpoints by design, so the model may not assume recovery. The
expected stuck state is the dead-endpoints livelock: `aTruth = empty`
while Envoy still holds `actA = some usable`. -/
def wholeSnapshotDeferBugSystem : System PCState PCAction :=
  mkSystem "WholeSnapshotDeferBug"
    [ .scaleDownA, .retargetRoutes,
      .buggyPublishAWholeSnapshotGate,
      .envoySyncA ]

/-- The demoted gate: routes flip without the usable-endpoint check and
Envoy activates them onto a cluster that warmed on an empty CLA. -/
def publishWhileWarmingBugSystem : System PCState PCAction :=
  mkSystem "PublishWhileWarmingBug"
    [ .retargetRoutes, .publishB, .buggyFlipRoutesUngated,
      .envoySyncB, .envoyFlipRoutes ]

/-- The rejected fail-open design (PR #13976): an errored cluster keeps
serving from its last-good config, so the served state never reflects the
error. The same liveness failure shape as the whole-snapshot defer — truth
(here: absence) never publishes — with a security consequence: the policy
whose failure errored the cluster is silently bypassed. Gateway API
conformance caught it (BackendTLSPolicyInvalidCACertificateRef requires a
5xx). Caught here as a liveness violation of TruthLagsA ~> TruthPublishedA. -/
def erroredRestoreBugSystem : System PCState PCAction :=
  mkSystem "ErroredRestoreBug"
    [ .errorA, .buggyServeErroredA, .envoySyncA ]

end XdsSpec.PerCluster
