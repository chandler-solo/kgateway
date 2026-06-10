/-
XdsSpec.Spec: the kgateway per-client xDS convergence state machine.

This is a Lean 4 port of devel/formal/tla/XdsPerClientConvergence.tla,
parameterized over an arbitrary resource-name type `Name` instead of the
TLA+ model's fixed `{"old", "new"}`. The same definitions serve two roles:

  1. Executable: `applyAction` is a computable step function, so the
     explicit-state model checker in `XdsSpec.Checker` can exhaustively
     explore concrete finite instantiations and reproduce the TLA+ bug
     counterexamples.
  2. Provable: the safety invariants are proven inductively over the
     reachable states for *any* `Name` type and *any* action parameters
     in `XdsSpec.Proofs` — an unbounded guarantee TLC cannot give.

Modeling notes (deviations from the TLA+ model, all deliberate):

  - Resource-name sets are `List Name` with membership semantics; set
    equality is mutual subset (`NameSet.eq`), never `=` on lists.
  - xDS version strings are modeled as content digests:
    `Version Name := Option (List Name)` where `some s` is "the version
    string derived from EDS content `s`" and `none` is the TLA+ "none".
    Version equality is content set-equality. This mirrors
    `filterEndpointResourcesForClusters` in
    pkg/kgateway/proxy_syncer/perclient.go, which versions EDS with a
    hash of the filtered ClusterLoadAssignments. The model therefore
    *assumes* the implementation's version digest is injective with
    respect to EDS content equality — see assumption IMPL-A1 in
    devel/formal/lean/ASSUMPTIONS.md.
  - The TLA+ model hardcodes the single old->new episode; here the
    "new" reference/cluster/endpoint sets are action parameters, so the
    proofs cover every episode shape, not just the two-name instance.
-/

namespace XdsSpec

/- Set-like operations on `List Name` under membership semantics. -/
namespace NameSet

def subset [DecidableEq Name] (a b : List Name) : Bool :=
  a.all (b.contains ·)

def eq [DecidableEq Name] (a b : List Name) : Bool :=
  subset a b && subset b a

end NameSet

/-- xDS version modeled as a digest of EDS content. `none` is the TLA+
"none" version (no snapshot computed yet). -/
abbrev Version (Name : Type) := Option (List Name)

/-- Version equality is content equality: two version strings derived
from the same EDS resource set are the same string. -/
def versionEq [DecidableEq Name] : Version Name → Version Name → Bool
  | none, none => true
  | some a, some b => NameSet.eq a b
  | _, _ => false

inductive Phase
  | stableOld | deferredPartial | coherentInput
  | publishedNew | warmingNew | edsResponded | activeNew
  deriving DecidableEq, Repr, Hashable

inductive ComputedState
  /-- TLA+ "nil" / "partial" / "coherent"; `partial` is a Lean keyword,
  hence `partialSnap`. -/
  | nil | partialSnap | coherent
  deriving DecidableEq, Repr, Hashable

inductive KrtEvent
  | none | delete | add | update
  deriving DecidableEq, Repr, Hashable

inductive WatchState
  | idle | opened | responded | suppressed
  deriving DecidableEq, Repr, Hashable

/-- One per-client snapshot's worth of data: the referenced clusters, the
CDS set, the EDS set, and the EDS version. Used for both the cache entry
in the go-control-plane snapshot cache and the retained last-good copy. -/
structure SnapshotData (Name : Type) where
  refs : List Name
  cds : List Name
  eds : List Name
  edsVersion : Version Name
  deriving DecidableEq, Repr, Hashable

/-- The full model state; field-for-field the TLA+ VARIABLES block, with
(cacheRefs, cacheCds, cacheEds, cacheEdsVersion) and the lastGood* group
each folded into a `SnapshotData`. -/
structure XdsState (Name : Type) where
  phase : Phase
  desiredRefs : List Name
  candidateCds : List Name
  candidateEds : List Name
  candidateEdsVersion : Version Name
  computedState : ComputedState
  krtEvent : KrtEvent
  cache : SnapshotData Name
  lastGood : SnapshotData Name
  envoyKnownRefs : List Name
  envoyKnownCds : List Name
  envoyRequestedEds : List Name
  clientEdsResources : List Name
  clientEdsVersion : Version Name
  envoyActiveRefs : List Name
  envoyActiveCds : List Name
  envoyActiveEds : List Name
  edsWatchState : WatchState
  deriving DecidableEq, Repr, Hashable

variable {Name : Type} [DecidableEq Name]

/-- TLA+ `CanRespond`: go-control-plane answers a named EDS watch only
when every resource in the snapshot is named by the request. -/
def canRespond (resources requestNames : List Name) : Bool :=
  NameSet.subset resources requestNames

def snapshotDataEq (a b : SnapshotData Name) : Bool :=
  NameSet.eq a.refs b.refs
    && NameSet.eq a.cds b.cds
    && NameSet.eq a.eds b.eds
    && versionEq a.edsVersion b.edsVersion

/-- TLA+ `CacheMatchesLastGood`. -/
def cacheMatchesLastGood (s : XdsState Name) : Bool :=
  snapshotDataEq s.cache s.lastGood

/-- TLA+ `CacheMatchesCandidate`. -/
def cacheMatchesCandidate (s : XdsState Name) : Bool :=
  NameSet.eq s.cache.refs s.desiredRefs
    && NameSet.eq s.cache.cds s.candidateCds
    && NameSet.eq s.cache.eds s.candidateEds
    && versionEq s.cache.edsVersion s.candidateEdsVersion

/-- TLA+ `CandidateClosed`: the computed snapshot is dependency-closed. -/
def candidateClosed (s : XdsState Name) : Bool :=
  NameSet.subset s.desiredRefs s.candidateCds
    && NameSet.subset s.desiredRefs s.candidateEds
    && NameSet.subset s.candidateEds s.candidateCds

/-- TLA+ `CacheClosed`. -/
def cacheClosed (s : XdsState Name) : Bool :=
  NameSet.subset s.cache.refs s.cache.cds
    && NameSet.subset s.cache.refs s.cache.eds
    && NameSet.subset s.cache.eds s.cache.cds

/-- TLA+ `ActiveClosed`. -/
def activeClosed (s : XdsState Name) : Bool :=
  NameSet.subset s.envoyActiveRefs s.envoyActiveCds
    && NameSet.subset s.envoyActiveRefs s.envoyActiveEds

/-- TLA+ `Init`, generalized over the initially-served resource set. -/
def initState (old : List Name) : XdsState Name where
  phase := .stableOld
  desiredRefs := old
  candidateCds := old
  candidateEds := old
  candidateEdsVersion := some old
  computedState := .coherent
  krtEvent := .none
  cache := ⟨old, old, old, some old⟩
  lastGood := ⟨old, old, old, some old⟩
  envoyKnownRefs := old
  envoyKnownCds := old
  envoyRequestedEds := old
  clientEdsResources := old
  clientEdsVersion := some old
  envoyActiveRefs := old
  envoyActiveCds := old
  envoyActiveEds := old
  edsWatchState := .idle

/--
The actions of the convergence machine. The first six are the safe
transitions of the TLA+ model (parameterized where the TLA+ model used
the concrete `New` constant); the `buggy*` actions are the TLA+ bug
variants used to validate that the invariants actually catch regressions.
-/
inductive Action (Name : Type)
  /-- TLA+ `DeferPartialInput`: a new desired input arrives but per-client
  EDS has not been derived yet; `snapshotPerClient` returns nil, which KRT
  surfaces as a delete event. -/
  | deferPartialInput (newRefs newCds : List Name)
  /-- TLA+ `InputBecomesCoherent`: per-client EDS catches up. -/
  | inputBecomesCoherent (newEds : List Name)
  /-- TLA+ `PublishCoherent`: the coherent, dependency-closed snapshot is
  written to the cache and becomes the new last-good. -/
  | publishCoherent
  /-- TLA+ `EnvoyLearnsCds`: Envoy ACKs CDS and opens a named EDS watch
  for exactly the clusters it now knows. -/
  | envoyLearnsCds
  /-- TLA+ `EdsWatchResponds`: go-control-plane answers the named watch
  because the version changed and every EDS resource is named. -/
  | edsWatchResponds
  /-- TLA+ `ActivateNew`: Envoy finishes warming and activates. -/
  | activateNew
  /-- Not in the TLA+ model (which checks a single old->new episode):
  after activation the system returns to steady state and the next
  episode may begin. With this the spec covers unboundedly many
  consecutive configuration changes. -/
  | beginNextEpisode
  /-- Not in the TLA+ model: a coherent recomputation that exactly
  matches the cached snapshot is a no-op in `snapshotPerClient` (KRT
  surfaces no change event); the system is already converged and
  returns to steady state. -/
  | observeConverged
  /-- Not in the TLA+ model: a watchdog-forced re-derivation of the
  per-client inputs from current truth. `inputBecomesCoherent` models
  the KRT *event* arriving; if that fan-out event is dropped (the
  production failure behind assumption KRT-A1 in ASSUMPTIONS.md), the
  client is stuck at `deferredPartial` forever. The heartbeat closes
  that gap by recomputing both the cluster and endpoint inputs without
  waiting for an event. The parameters carry KRT-A1's content: the
  re-derivation sees the current (coherent) truth. -/
  | heartbeatRederive (newCds newEds : List Name)
  /-- Not in the TLA+ model: go-control-plane does not answer a watch
  whose version already matches the snapshot — Envoy already holds
  exactly these EDS resources, so warming completes from its current
  state. Without this, a published snapshot whose EDS content set-equals
  what the client already has could never leave `warmingNew`. -/
  | edsWatchNoChange
  /-- TLA+ `BuggyClearCacheOnDelete` (issue 13868 regression shape). -/
  | buggyClearCacheOnDelete
  /-- TLA+ `BuggyPublishPartial` (publishing a partial snapshot). -/
  | buggyPublishPartial
  /-- TLA+ `BuggyPublishStaleEds` (issue 14184 regression shape). -/
  | buggyPublishStaleEds (staleEds : List Name)
  /-- TLA+ `BuggyPublishWithoutEdsVersionChange`. -/
  | buggyPublishWithoutEdsVersionChange
  /-- TLA+ `BuggyActivateBeforeEds` (warming bypass). -/
  | buggyActivateBeforeEds
  deriving DecidableEq, Repr, Hashable

/-- Guarded step function. `none` means the action's guard is not enabled
in `s` (the TLA+ action is false); `some s'` is the post-state. -/
def applyAction (s : XdsState Name) : Action Name → Option (XdsState Name)
  | .deferPartialInput newRefs newCds =>
    if s.phase == .stableOld then
      some { s with
        phase := .deferredPartial
        desiredRefs := newRefs
        candidateCds := newCds
        candidateEds := []
        candidateEdsVersion := none
        computedState := .partialSnap
        krtEvent := .delete
        edsWatchState := .idle }
    else none
  | .inputBecomesCoherent newEds =>
    if s.phase == .deferredPartial then
      some { s with
        phase := .coherentInput
        candidateEds := newEds
        candidateEdsVersion := some newEds
        computedState := .coherent
        krtEvent := .update }
    else none
  | .publishCoherent =>
    if s.computedState == .coherent
        && candidateClosed s
        && !cacheMatchesCandidate s then
      let published : SnapshotData Name :=
        ⟨s.desiredRefs, s.candidateCds, s.candidateEds, s.candidateEdsVersion⟩
      some { s with
        phase := .publishedNew
        cache := published
        lastGood := published
        krtEvent := .add
        edsWatchState := .idle }
    else none
  | .envoyLearnsCds =>
    if s.phase == .publishedNew then
      some { s with
        phase := .warmingNew
        envoyKnownRefs := s.cache.refs
        envoyKnownCds := s.cache.cds
        envoyRequestedEds := s.cache.cds
        edsWatchState := .opened }
    else none
  | .edsWatchResponds =>
    if s.phase == .warmingNew
        && !versionEq s.cache.edsVersion s.clientEdsVersion
        && canRespond s.cache.eds s.envoyRequestedEds then
      some { s with
        phase := .edsResponded
        edsWatchState := .responded
        clientEdsResources := s.cache.eds
        clientEdsVersion := s.cache.edsVersion }
    else none
  | .activateNew =>
    if s.phase == .edsResponded
        && NameSet.subset s.envoyKnownRefs s.envoyKnownCds
        && NameSet.subset s.envoyKnownRefs s.clientEdsResources then
      some { s with
        phase := .activeNew
        envoyActiveRefs := s.envoyKnownRefs
        envoyActiveCds := s.envoyKnownCds
        envoyActiveEds := s.clientEdsResources
        edsWatchState := .idle }
    else none
  | .beginNextEpisode =>
    if s.phase == .activeNew then
      some { s with phase := .stableOld, krtEvent := .none }
    else none
  | .observeConverged =>
    if s.phase == .coherentInput && cacheMatchesCandidate s then
      some { s with phase := .stableOld, krtEvent := .none }
    else none
  | .heartbeatRederive newCds newEds =>
    if s.phase == .deferredPartial then
      some { s with
        phase := .coherentInput
        candidateCds := newCds
        candidateEds := newEds
        candidateEdsVersion := some newEds
        computedState := .coherent
        krtEvent := .update }
    else none
  | .edsWatchNoChange =>
    if s.phase == .warmingNew
        && versionEq s.cache.edsVersion s.clientEdsVersion
        && canRespond s.cache.eds s.envoyRequestedEds then
      some { s with
        phase := .edsResponded
        edsWatchState := .responded }
    else none
  | .buggyClearCacheOnDelete =>
    if s.phase == .deferredPartial && s.krtEvent == .delete then
      some { s with cache := ⟨[], [], [], none⟩ }
    else none
  | .buggyPublishPartial =>
    if s.phase == .deferredPartial && s.computedState == .partialSnap then
      some { s with
        cache := ⟨s.desiredRefs, s.candidateCds, s.candidateEds,
                  some s.candidateEds⟩
        krtEvent := .add }
    else none
  | .buggyPublishStaleEds staleEds =>
    if s.phase == .coherentInput then
      some { s with
        cache := ⟨s.desiredRefs, s.candidateCds, staleEds, some staleEds⟩ }
    else none
  | .buggyPublishWithoutEdsVersionChange =>
    if s.phase == .coherentInput then
      some { s with
        cache := ⟨s.desiredRefs, s.candidateCds, s.candidateEds,
                  s.clientEdsVersion⟩ }
    else none
  | .buggyActivateBeforeEds =>
    if s.phase == .warmingNew then
      some { s with
        phase := .activeNew
        envoyActiveRefs := s.envoyKnownRefs
        envoyActiveCds := s.envoyKnownCds
        edsWatchState := .idle }
    else none

-- MARK: Invariants

/-- TLA+ `DeleteRetainsLastGood`: while KRT surfaces a delete (the defer
window), the served cache snapshot must still be the last-good one. -/
def deleteRetainsLastGood (s : XdsState Name) : Bool :=
  s.krtEvent != .delete || cacheMatchesLastGood s

/-- TLA+ `PartialDoesNotOverwriteCache`. -/
def partialDoesNotOverwriteCache (s : XdsState Name) : Bool :=
  s.computedState != .partialSnap || cacheMatchesLastGood s

/-- TLA+ `CacheSnapshotClosed`. -/
def cacheSnapshotClosed (s : XdsState Name) : Bool :=
  cacheClosed s

/-- TLA+ `AlignedEDSRequestRespondable`: once Envoy's CDS knowledge
matches the cache, the cache's EDS set must be answerable against the
named EDS request (issue 14184's failure mode). -/
def alignedEdsRequestRespondable (s : XdsState Name) : Bool :=
  !NameSet.eq s.envoyKnownCds s.cache.cds
    || canRespond s.cache.eds s.envoyRequestedEds

/-- TLA+ `EDSResourceSetChangeChangesVersion`. -/
def edsResourceSetChangeChangesVersion (s : XdsState Name) : Bool :=
  NameSet.eq s.cache.eds s.clientEdsResources
    || !versionEq s.cache.edsVersion s.clientEdsVersion

/-- TLA+ `ActiveSnapshotClosed`: Envoy never activates a route whose
cluster or endpoints it does not have. -/
def activeSnapshotClosed (s : XdsState Name) : Bool :=
  activeClosed s

/-- TLA+ `CoherentInputCanPublish` (the ENABLED check): a coherent,
closed, cache-differing candidate must satisfy `publishCoherent`'s guard. -/
def coherentInputCanPublish (s : XdsState Name) : Bool :=
  !(s.computedState == .coherent
      && candidateClosed s
      && !cacheMatchesCandidate s)
    || (applyAction s .publishCoherent).isSome

/-- The conjunction checked by the model checker and proven inductively.
TLA+ `TypeOK` is enforced by Lean's types and omitted. -/
def safetyInvariants (s : XdsState Name) : Bool :=
  deleteRetainsLastGood s
    && partialDoesNotOverwriteCache s
    && cacheSnapshotClosed s
    && alignedEdsRequestRespondable s
    && edsResourceSetChangeChangesVersion s
    && activeSnapshotClosed s
    && coherentInputCanPublish s

/-- Names and predicates of the individual invariants, for counterexample
reporting. -/
def invariantList :
    List (String × (XdsState Name → Bool)) :=
  [ ("DeleteRetainsLastGood", deleteRetainsLastGood),
    ("PartialDoesNotOverwriteCache", partialDoesNotOverwriteCache),
    ("CacheSnapshotClosed", cacheSnapshotClosed),
    ("AlignedEDSRequestRespondable", alignedEdsRequestRespondable),
    ("EDSResourceSetChangeChangesVersion", edsResourceSetChangeChangesVersion),
    ("ActiveSnapshotClosed", activeSnapshotClosed),
    ("CoherentInputCanPublish", coherentInputCanPublish) ]

end XdsSpec
