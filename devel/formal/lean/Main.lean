/-
xdsspec CLI: run the model configurations and the trace checker.

Exit code 0 means every expectation held:
  - the safe systems satisfy all safety invariants and their liveness
    properties, and
  - every bug system reproduces its expected counterexample (a bug
    config that silently passes means an invariant lost its teeth).
-/
import XdsSpec
import XdsSpec.TraceCheck

open XdsSpec XdsSpec.Convergence

def renderTrace [Repr σ] (sys : System σ α)
    (trace : List (α × σ)) (bad : σ) : String :=
  let steps := trace.map fun (a, _) => s!"  -> {sys.describeAction a}"
  String.intercalate "\n" steps ++ s!"\n  bad state: {reprStr bad}"

structure Expectation (σ α : Type) where
  system : System σ α
  invariants : List (String × (σ → Bool))
  /-- `none`: all invariants must hold. `some inv`: invariant `inv` must
  be violated. -/
  expectViolation : Option String

def runSafetyExpectation [BEq σ] [Hashable σ] [Repr σ]
    (e : Expectation σ α) : IO Bool := do
  let result := checkSafety e.system e.invariants
  match e.expectViolation, result with
  | none, .ok n =>
    IO.println s!"PASS  {e.system.name}: all invariants hold ({n} states)"
    return true
  | none, .violation inv trace bad =>
    IO.println s!"FAIL  {e.system.name}: invariant {inv} violated"
    IO.println (renderTrace e.system trace bad)
    return false
  | some inv, .violation inv' trace _ =>
    if inv = inv' then
      IO.println s!"PASS  {e.system.name}: reproduced expected violation of {inv} ({trace.length} steps)"
      return true
    else
      IO.println s!"FAIL  {e.system.name}: expected violation of {inv} but got {inv'}"
      return false
  | some inv, .ok n =>
    IO.println s!"FAIL  {e.system.name}: expected violation of {inv} but all invariants held ({n} states) — an invariant lost its teeth"
    return false
  | _, .livenessViolation .. =>
    IO.println s!"FAIL  {e.system.name}: unexpected liveness result from safety check"
    return false

def runLiveness [BEq σ] [Hashable σ] [Repr σ]
    (sys : System σ α) (expectStuck : Bool)
    (premise goal : σ → Bool) (property : String) : IO Bool := do
  match checkLiveness sys premise goal with
  | .ok n =>
    if expectStuck then
      IO.println s!"FAIL  {sys.name}: expected liveness violation but {property} holds ({n} states)"
      return false
    else
      IO.println s!"PASS  {sys.name}: {property} holds ({n} states)"
      return true
  | .livenessViolation stuck n =>
    if expectStuck then
      IO.println s!"PASS  {sys.name}: reproduced expected liveness violation of {property} ({n} states)"
      return true
    else
      IO.println s!"FAIL  {sys.name}: {property} violated; stuck state: {reprStr stuck}"
      return false
  | .violation .. =>
    IO.println s!"FAIL  {sys.name}: unexpected safety result from liveness check"
    return false

def convergenceExpectations : List (Expectation CState CAction) :=
  [ ⟨safeSystem, invariantList, none⟩,
    ⟨clearOnDeleteBugSystem, invariantList, some "DeleteRetainsLastGood"⟩,
    ⟨partialOverwriteBugSystem, invariantList, some "PartialDoesNotOverwriteCache"⟩,
    ⟨staleEdsBugSystem, invariantList, some "CacheSnapshotClosed"⟩,
    ⟨versionReuseBugSystem, invariantList, some "EDSResourceSetChangeChangesVersion"⟩,
    ⟨activateBeforeEdsBugSystem, invariantList, some "ActiveSnapshotClosed"⟩ ]

def perClusterExpectations :
    List (Expectation PerCluster.PCState PerCluster.PCAction) :=
  [ ⟨PerCluster.safeSystem, PerCluster.invariantList, none⟩,
    ⟨PerCluster.publishWhileWarmingBugSystem, PerCluster.invariantList,
      some "FlipWasGated"⟩ ]

def runModelCheck : IO UInt32 := do
  IO.println "xdsspec: model-checking the per-client xDS convergence spec"
  IO.println "(Lean port of devel/formal/tla/XdsPerClientConvergence.tla)"
  IO.println ""
  let mut ok := true
  for e in convergenceExpectations do
    ok := (← runSafetyExpectation e) && ok
  ok := (← runLiveness safeSystem (expectStuck := false)
    isCoherentInput isConverged "CoherentInput ~> Converged") && ok
  ok := (← runLiveness noPublishBugSystem (expectStuck := true)
    isCoherentInput isConverged "CoherentInput ~> Converged") && ok
  -- KRT-A1 (see ASSUMPTIONS.md): a dropped fan-out event strands the
  -- client at DeferredPartial; the watchdog heartbeat restores progress.
  -- Finite-instance counterparts of XdsSpec.stuck_client_converges.
  ok := (← runLiveness droppedFanoutBugSystem (expectStuck := true)
    isDeferredPartial isConverged "DeferredPartial ~> Converged") && ok
  ok := (← runLiveness droppedFanoutWithHeartbeatSystem (expectStuck := false)
    isDeferredPartial isConverged "DeferredPartial ~> Converged") && ok
  IO.println ""
  IO.println "per-cluster readiness model (guard #3 granularity)"
  IO.println ""
  for e in perClusterExpectations do
    ok := (← runSafetyExpectation e) && ok
  -- C2: a previously-active cluster's published CLA always catches up
  -- with its truth (empty included) — for every cluster independently
  -- of the others' readiness (publication isolation).
  ok := (← runLiveness PerCluster.safeSystem (expectStuck := false)
    PerCluster.truthLagsA PerCluster.truthPublishedA
    "TruthLagsA ~> TruthPublishedA") && ok
  -- C3: once the newly referenced cluster is deployed, the held route
  -- flip goes through.
  ok := (← runLiveness PerCluster.safeSystem (expectStuck := false)
    PerCluster.flipPending PerCluster.flipDone
    "FlipPending ~> FlipDone") && ok
  -- The strengthened whole-snapshot gate livelocks: scale-to-zero can
  -- never publish its empty CLA, so Envoy keeps the dead endpoints.
  ok := (← runLiveness PerCluster.wholeSnapshotDeferBugSystem
    (expectStuck := true)
    PerCluster.truthLagsA PerCluster.truthPublishedA
    "TruthLagsA ~> TruthPublishedA") && ok
  IO.println ""
  if ok then
    IO.println "all model-check expectations held"
    return 0
  else
    IO.println "model-check expectations FAILED"
    return 1

def main (args : List String) : IO UInt32 := do
  match args with
  | [] | ["check"] => runModelCheck
  | "trace" :: paths =>
    if paths.isEmpty then
      IO.println "usage: xdsspec trace <trace.jsonl> [...]"
      return 2
    XdsSpec.Trace.runTraceCheck paths
  | _ =>
    IO.println "usage: xdsspec [check | trace <trace.jsonl>...]"
    return 2
