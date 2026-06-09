/-
XdsSpec.Checker: a small explicit-state model checker.

This plays the role TLC plays for the TLA+ models: exhaustive
breadth-first exploration of a finite instantiation, checking safety
invariants on every reachable state and reporting a shortest
counterexample trace on violation. It also supports a reachability-style
liveness check: under the weak-fairness assumptions of the TLA+
`SafeSpec`, the property `phase = CoherentInput ~> phase = ActiveNew`
fails on a finite system exactly when some reachable state satisfying
the premise has no path to a state satisfying the goal, which is what
`checkLiveness` detects.
-/

import Std.Data.HashSet

namespace XdsSpec

/-- A finite transition system instance: an initial state and the finite
set of (named) actions to explore. `step` returns `none` when the action
is not enabled. -/
structure System (σ α : Type) where
  name : String
  init : σ
  actions : List α
  step : σ → α → Option σ
  describeAction : α → String

inductive CheckResult (σ α : Type)
  /-- All reachable states satisfy all invariants. -/
  | ok (statesExplored : Nat)
  /-- `trace` is the action-labeled path from init to the violating state. -/
  | violation (invariant : String) (trace : List (α × σ)) (bad : σ)
  | livenessViolation (stuck : σ) (statesExplored : Nat)
  deriving Repr

instance : Inhabited (CheckResult σ α) := ⟨.ok 0⟩

/-- Breadth-first exploration of the reachable state space, checking each
named invariant on every state. Counterexamples are minimal-length because
exploration is BFS. -/
partial def checkSafety [BEq σ] [Hashable σ]
    (sys : System σ α)
    (invariants : List (String × (σ → Bool))) : CheckResult σ α :=
  go [(sys.init, [])] ((∅ : Std.HashSet _).insert sys.init) 1
where
  firstViolation (s : σ) : Option String :=
    invariants.findSome? fun (n, p) => if p s then none else some n
  go (frontier : List (σ × List (α × σ))) (visited : Std.HashSet σ)
      (count : Nat) : CheckResult σ α :=
    match frontier with
    | [] => .ok count
    | (s, path) :: rest =>
      match firstViolation s with
      | some inv => .violation inv path.reverse s
      | none =>
        let (next, visited, count) :=
          sys.actions.foldl (init := ([], visited, count))
            fun (acc, visited, count) a =>
              match sys.step s a with
              | none => (acc, visited, count)
              | some s' =>
                if visited.contains s' then (acc, visited, count)
                else ((s', (a, s') :: path) :: acc, visited.insert s', count + 1)
        go (rest ++ next.reverse) visited count

/-- Collect the reachable state set. -/
partial def reachable [BEq σ] [Hashable σ]
    (sys : System σ α) : Std.HashSet σ :=
  go [sys.init] ((∅ : Std.HashSet _).insert sys.init)
where
  go (frontier : List σ) (visited : Std.HashSet σ) : Std.HashSet σ :=
    match frontier with
    | [] => visited
    | s :: rest =>
      let (next, visited) :=
        sys.actions.foldl (init := ([], visited)) fun (acc, visited) a =>
          match sys.step s a with
          | none => (acc, visited)
          | some s' =>
            if visited.contains s' then (acc, visited)
            else (s' :: acc, visited.insert s')
      go (rest ++ next) visited

/-- Can a state satisfying `goal` be reached from `s`? -/
partial def canReach [BEq σ] [Hashable σ]
    (sys : System σ α) (s : σ) (goal : σ → Bool) : Bool :=
  go [s] ((∅ : Std.HashSet _).insert s)
where
  go (frontier : List σ) (visited : Std.HashSet σ) : Bool :=
    match frontier with
    | [] => false
    | s :: rest =>
      if goal s then true
      else
        let (next, visited) :=
          sys.actions.foldl (init := ([], visited)) fun (acc, visited) a =>
            match sys.step s a with
            | none => (acc, visited)
            | some s' =>
              if visited.contains s' then (acc, visited)
              else (s' :: acc, visited.insert s')
        go (rest ++ next) visited

/-- Finite-state analogue of the TLA+ leads-to property under weak
fairness: every reachable state satisfying `premise` must have a path to
a state satisfying `goal`. Returns the first stuck state otherwise. -/
def checkLiveness [BEq σ] [Hashable σ]
    (sys : System σ α) (premise goal : σ → Bool) : CheckResult σ α :=
  let states := reachable sys
  let stuck := states.toList.find? fun s =>
    premise s && !canReach sys s goal
  match stuck with
  | some s => .livenessViolation s states.size
  | none => .ok states.size

end XdsSpec
