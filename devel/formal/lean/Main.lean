/-
xdsspec CLI: run the convergence model configurations.

Exit code 0 means every expectation held:
  - the safe system satisfies all safety invariants and the liveness
    property (CoherentInput leads to ActiveNew), and
  - every bug system reproduces its expected counterexample (a bug
    config that silently passes means an invariant lost its teeth).
-/
import XdsSpec
import XdsSpec.TraceCheck

open XdsSpec XdsSpec.Convergence

def renderTrace (sys : System CState CAction)
    (trace : List (CAction × CState)) : String :=
  String.intercalate "\n" <| trace.map fun (a, s) =>
    s!"  -> {sys.describeAction a}\n     {reprStr s.phase} cacheEds={reprStr s.cache.eds} cacheCds={reprStr s.cache.cds}"

structure Expectation where
  system : System CState CAction
  /-- `none`: all invariants must hold. `some inv`: invariant `inv` must
  be violated. -/
  expectViolation : Option String

def safetyExpectations : List Expectation :=
  [ ⟨safeSystem, none⟩,
    ⟨clearOnDeleteBugSystem, some "DeleteRetainsLastGood"⟩,
    ⟨partialOverwriteBugSystem, some "PartialDoesNotOverwriteCache"⟩,
    ⟨staleEdsBugSystem, some "CacheSnapshotClosed"⟩,
    ⟨versionReuseBugSystem, some "EDSResourceSetChangeChangesVersion"⟩,
    ⟨activateBeforeEdsBugSystem, some "ActiveSnapshotClosed"⟩ ]

def runSafetyExpectation (e : Expectation) : IO Bool := do
  let result := checkSafety e.system invariantList
  match e.expectViolation, result with
  | none, .ok n =>
    IO.println s!"PASS  {e.system.name}: all invariants hold ({n} states)"
    return true
  | none, .violation inv trace bad =>
    IO.println s!"FAIL  {e.system.name}: invariant {inv} violated"
    IO.println (renderTrace e.system trace)
    IO.println s!"  bad state: {reprStr bad}"
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

def runLiveness (sys : System CState CAction) (expectStuck : Bool) : IO Bool := do
  match checkLiveness sys isCoherentInput isConverged with
  | .ok n =>
    if expectStuck then
      IO.println s!"FAIL  {sys.name}: expected liveness violation but CoherentInput ~> Converged holds ({n} states)"
      return false
    else
      IO.println s!"PASS  {sys.name}: CoherentInput ~> Converged holds ({n} states)"
      return true
  | .livenessViolation stuck n =>
    if expectStuck then
      IO.println s!"PASS  {sys.name}: reproduced expected liveness violation ({n} states)"
      return true
    else
      IO.println s!"FAIL  {sys.name}: liveness violated; stuck state: {reprStr stuck}"
      return false
  | .violation .. =>
    IO.println s!"FAIL  {sys.name}: unexpected safety result from liveness check"
    return false

def runModelCheck : IO UInt32 := do
  IO.println "xdsspec: model-checking the per-client xDS convergence spec"
  IO.println "(Lean port of devel/formal/tla/XdsPerClientConvergence.tla)"
  IO.println ""
  let mut ok := true
  for e in safetyExpectations do
    ok := (← runSafetyExpectation e) && ok
  ok := (← runLiveness safeSystem (expectStuck := false)) && ok
  ok := (← runLiveness noPublishBugSystem (expectStuck := true)) && ok
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
