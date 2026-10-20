/- Backend resources introduce cluster → listener → cluster/secret edges.
   These are structural obligations over arbitrary resource identities. They
   neither refine Go nor prove delivery, application, or temporal progress. -/
namespace XdsSpec.BackendResourceClosure

def Closed (edge : α → α → Prop) (kept : α → Prop) : Prop :=
  ∀ ⦃a b⦄, kept a → edge a b → kept b

inductive Reach (edge : α → α → Prop) (roots : α → Prop) : α → Prop where
  | root {a} : roots a → Reach edge roots a
  | step {a b} : Reach edge roots a → edge a b → Reach edge roots b

theorem rootsRetained (edge : α → α → Prop) (roots : α → Prop) {a}
    (h : roots a) : Reach edge roots a := .root h

theorem reachableClosed (edge : α → α → Prop) (roots : α → Prop) :
    Closed edge (Reach edge roots) := by
  intro a b reached dependency
  exact .step reached dependency

theorem leastClosedSet (edge : α → α → Prop) (roots kept : α → Prop)
    (includeRoots : ∀ a, roots a → kept a) (closed : Closed edge kept)
    {a} (reachable : Reach edge roots a) : kept a := by
  induction reachable with
  | root h => exact includeRoots _ h
  | step _ dependency ih => exact closed ih dependency

theorem retainedBackendNeedsListener (edge : α → α → Prop) (kept : α → Prop)
    (closed : Closed edge kept) {cluster listener}
    (carried : kept cluster) (owns : edge cluster listener) : kept listener :=
  closed carried owns

theorem retainedBackendNeedsProxy (edge : α → α → Prop) (kept : α → Prop)
    (closed : Closed edge kept) {cluster listener proxy}
    (carried : kept cluster) (owns : edge cluster listener)
    (calls : edge listener proxy) : kept proxy :=
  closed (closed carried owns) calls

theorem retainedBackendNeedsSecret (edge : α → α → Prop) (kept : α → Prop)
    (closed : Closed edge kept) {cluster listener secret}
    (carried : kept cluster) (owns : edge cluster listener)
    (reads : edge listener secret) : kept secret :=
  closed (closed carried owns) reads

inductive Resource where
  | cluster | listener | proxy | secret
  deriving DecidableEq

def exampleEdges (a b : Resource) : Prop :=
  (a = .cluster ∧ b = .listener) ∨
  (a = .listener ∧ b = .proxy) ∨
  (a = .listener ∧ b = .secret)

-- The old carry rule retains the root but not its owned resource. This is a
-- counterexample to closure even before considering downstream network timing.
theorem clusterOnlyCarryNotClosed :
    ¬Closed exampleEdges (fun r => r = .cluster) := by
  intro closed
  have impossible : Resource.listener = .cluster :=
    closed rfl (Or.inl ⟨rfl, rfl⟩)
  cases impossible

end XdsSpec.BackendResourceClosure
