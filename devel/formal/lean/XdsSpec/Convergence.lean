/-
XdsSpec.Convergence: concrete instantiations matching the TLA+ configs.

Each `System` below corresponds to a SPECIFICATION in
devel/formal/tla/XdsPerClientConvergence*.cfg, instantiated at the same
two-name universe (`old`, `new`). The safe system must satisfy every
invariant and the liveness property; each bug system must reproduce the
TLA+ counterexample. `Main.lean` runs all of them and fails loudly if a
bug config stops producing its counterexample — that is the regression
gate that keeps the invariants honest.
-/
import XdsSpec.Spec
import XdsSpec.Checker

namespace XdsSpec.Convergence

open XdsSpec

/-- The TLA+ model's `Names == {"old", "new"}`. -/
inductive CName
  | old | new
  deriving DecidableEq, Repr, Hashable

abbrev CState := XdsState CName
abbrev CAction := Action CName

def oldSet : List CName := [.old]
def newSet : List CName := [.new]

def describe : CAction → String
  | .deferPartialInput _ _ => "DeferPartialInput"
  | .inputBecomesCoherent _ => "InputBecomesCoherent"
  | .publishCoherent => "PublishCoherent"
  | .envoyLearnsCds => "EnvoyLearnsCds"
  | .edsWatchResponds => "EdsWatchResponds"
  | .activateNew => "ActivateNew"
  | .beginNextEpisode => "BeginNextEpisode"
  | .observeConverged => "ObserveConverged"
  | .buggyClearCacheOnDelete => "BuggyClearCacheOnDelete"
  | .buggyPublishPartial => "BuggyPublishPartial"
  | .buggyPublishStaleEds _ => "BuggyPublishStaleEds"
  | .buggyPublishWithoutEdsVersionChange => "BuggyPublishWithoutEdsVersionChange"
  | .buggyActivateBeforeEds => "BuggyActivateBeforeEds"

def mkSystem (name : String) (actions : List CAction) :
    System CState CAction where
  name := name
  init := initState oldSet
  actions := actions
  step := applyAction
  describeAction := describe

/-- TLA+ `SafeNext` (NoOp/stuttering is implicit in explicit-state
exploration). -/
def safeActions : List CAction :=
  [ .deferPartialInput newSet newSet,
    .inputBecomesCoherent newSet,
    .publishCoherent,
    .envoyLearnsCds,
    .edsWatchResponds,
    .activateNew,
    .beginNextEpisode,
    .observeConverged ]

def safeSystem : System CState CAction :=
  mkSystem "Safe" safeActions

/-- TLA+ `ClearOnDeleteBugSpec` (issue 13868 regression shape). -/
def clearOnDeleteBugSystem : System CState CAction :=
  mkSystem "ClearOnDeleteBug"
    [ .deferPartialInput newSet newSet, .buggyClearCacheOnDelete ]

/-- TLA+ `PartialOverwriteBugSpec`. -/
def partialOverwriteBugSystem : System CState CAction :=
  mkSystem "PartialOverwriteBug"
    [ .deferPartialInput newSet newSet, .buggyPublishPartial ]

/-- TLA+ `StaleEdsBugSpec` (issue 14184 regression shape). -/
def staleEdsBugSystem : System CState CAction :=
  mkSystem "StaleEdsBug"
    [ .deferPartialInput newSet newSet,
      .inputBecomesCoherent newSet,
      .buggyPublishStaleEds [.old, .new] ]

/-- TLA+ `VersionReuseBugSpec`. -/
def versionReuseBugSystem : System CState CAction :=
  mkSystem "VersionReuseBug"
    [ .deferPartialInput newSet newSet,
      .inputBecomesCoherent newSet,
      .buggyPublishWithoutEdsVersionChange ]

/-- TLA+ `ActivateBeforeEdsBugSpec`. -/
def activateBeforeEdsBugSystem : System CState CAction :=
  mkSystem "ActivateBeforeEdsBug"
    [ .deferPartialInput newSet newSet,
      .inputBecomesCoherent newSet,
      .publishCoherent,
      .envoyLearnsCds,
      .buggyActivateBeforeEds ]

/-- TLA+ `NoPublishBugSpec`: safety holds but liveness fails because the
coherent input is never published. -/
def noPublishBugSystem : System CState CAction :=
  mkSystem "NoPublishBug"
    [ .deferPartialInput newSet newSet, .inputBecomesCoherent newSet ]

def isCoherentInput (s : CState) : Bool := s.phase == .coherentInput

/-- The liveness goal: a coherent input must lead either to activation of
the new snapshot or back to steady state because the input was already
what the cache serves (a repeat episode that recomputes the same
snapshot is a no-op, not a stall). -/
def isConverged (s : CState) : Bool :=
  s.phase == .activeNew || s.phase == .stableOld

end XdsSpec.Convergence
