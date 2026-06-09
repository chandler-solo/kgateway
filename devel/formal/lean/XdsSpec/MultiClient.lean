/-
XdsSpec.MultiClient: unboundedly many clients, with isolation.

The TLA+ models on this branch are all single-client; multi-client
convergence (the setting where issue 13868 actually bit: one Envoy
replica stranded while others are fine) is only covered by one Go test,
`TestSnapshotPerClientPartialUpdateForOneClientDoesNotPoisonAnotherClient`.

This module lifts the single-client spec to a function
`Client -> XdsState Name` over an *arbitrary* client type, with steps
that act on one client at a time (any interleaving). Two theorems:

  - `isolation`: a step by one client's per-client pipeline leaves every
    other client's state untouched. In the implementation this is the
    claim that `snapshotPerClient`'s KRT transform for one
    UniquelyConnectedClient writes only that client's snapshot-cache
    entry.
  - `multi_safety`: under any interleaving, every client's state
    satisfies all safety invariants — for any number of clients, any
    name universe, and unboundedly many configuration episodes.

Modeling note: in the implementation, clients share the upstream input
collections. Here each client's action parameters are arbitrary and
independent, which *subsumes* the shared-input schedule: any execution
in which clients observe the same inputs is one particular interleaving
with one particular choice of parameters.
-/
import XdsSpec.Spec
import XdsSpec.Proofs

namespace XdsSpec

variable {Client : Type} [DecidableEq Client]
variable {Name : Type} [DecidableEq Name]

/-- One per-client convergence state per uniquely-connected client. -/
def MultiState (Client Name : Type) := Client → XdsState Name

/-- Each client starts in its own steady state (clients of different
gateways serve different resource sets). -/
def multiInit (old : Client → List Name) : MultiState Client Name :=
  fun c => initState (old c)

/-- A step of client `c`'s per-client pipeline; other clients' states are
untouched by construction — `isolation` makes that an explicit theorem. -/
def applyClientAction (m : MultiState Client Name) (c : Client)
    (a : Action Name) : Option (MultiState Client Name) :=
  (applyAction (m c) a).map fun s' c' => if c' = c then s' else m c'

inductive MultiReachable (old : Client → List Name) :
    MultiState Client Name → Prop
  | init : MultiReachable old (multiInit old)
  | step {m m' : MultiState Client Name} (c : Client) (a : Action Name)
      (hsafe : a.isSafe = true)
      (hr : MultiReachable old m)
      (happ : applyClientAction m c a = some m') : MultiReachable old m'

/-- A step by one client never mutates another client's state. -/
theorem isolation {m m' : MultiState Client Name} {c : Client}
    {a : Action Name} (happ : applyClientAction m c a = some m') :
    ∀ c', c' ≠ c → m' c' = m c' := by
  intro c' hne
  unfold applyClientAction at happ
  cases h : applyAction (m c) a with
  | none => simp [h] at happ
  | some s' =>
    simp only [h, Option.map_some, Option.some.injEq] at happ
    subst happ
    simp [hne]

/-- Every client's component of a reachable multi-state is reachable in
the single-client spec. -/
theorem MultiReachable.proj {old : Client → List Name}
    {m : MultiState Client Name} (h : MultiReachable old m) (c : Client) :
    Reachable (old c) (m c) := by
  induction h with
  | init => exact .init
  | @step m₀ m₁ c' a hsafe _hr happ ih =>
    by_cases hc : c = c'
    · subst hc
      unfold applyClientAction at happ
      cases h' : applyAction (m₀ c) a with
      | none => rw [h'] at happ; simp at happ
      | some s' =>
        rw [h'] at happ
        simp only [Option.map_some, Option.some.injEq] at happ
        subst happ
        simpa using Reachable.step a hsafe ih h'
    · rw [isolation happ c hc]
      exact ih

/-- Headline multi-client theorem: under any interleaving of any number
of clients, every client's snapshot state satisfies every checked safety
invariant. -/
theorem multi_safety {old : Client → List Name}
    {m : MultiState Client Name} (h : MultiReachable old m) :
    ∀ c, safetyInvariants (m c) = true :=
  fun c => safety (h.proj c)

end XdsSpec
