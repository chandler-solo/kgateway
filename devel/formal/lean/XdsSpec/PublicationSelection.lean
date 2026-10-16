/- Selection contracts for the October 16 research policy. Payloads and row
contributions travel together; a synthesized empty is not a derived row.
These are abstract structural proofs, not a refinement proof of Go or hashing. -/
namespace XdsSpec.PublicationSelection

structure Entry (Payload Contribution : Type) where
  payload : Payload
  contribution : Contribution
  deriving DecidableEq, Repr

inductive Origin where
  | current | retained | synthesized
  deriving DecidableEq, Repr

-- `derived = some e` includes a legitimately empty assignment. No health or
-- nonempty predicate participates. Omission is represented by `none`.
-- `errored` excludes THIS resource key; it is not the error flag of an arbitrary
-- cluster sharing the assignment. Computing eligibility/dependency closure is
-- a separate obligation: one invalid consumer cannot exclude a valid sibling's CLA.
def select (errored : Bool) (derived retained synthesized : Option (Entry P H)) :
    Option (Origin × Entry P H) :=
  if errored then none
  else match derived with
    | some e => some (.current, e)
    | none => match retained with
      | some e => some (.retained, e)
      | none => synthesized.map fun e => (.synthesized, e)

theorem erroredExcluded (d r s : Option (Entry P H)) :
    select true d r s = none := rfl

theorem currentWins (e : Entry P H) (r s : Option (Entry P H)) :
    select false (some e) r s = some (.current, e) := rfl

-- In particular, carrying another cluster that shares this CLA key cannot
-- replace either the current bytes OR their remembered contribution.
theorem sharedAssignmentKeepsCurrentPair (e old synthesized : Entry P H) :
    select false (some e) (some old) (some synthesized) = some (.current, e) := rfl

theorem carryKeepsPair (e : Entry P H) (s : Option (Entry P H)) :
    select false none (some e) s = some (.retained, e) := rfl

theorem omissionIsNotEmptyTruth :
    select (P := P) (H := H) false none none none = none := rfl

-- An equality projection is sufficient only if it determines publication.
-- This obligation includes error/derivation metadata even when CDS/EDS versions
-- are unchanged. The new stack probe demonstrates a Go projection violating it.
theorem policyChangeMustInvalidate {State Projection Result : Type}
    (projection : State → Projection) (decision : State → Result)
    (adequate : ∀ a b, projection a = projection b → decision a = decision b)
    (a b : State) (changed : decision a ≠ decision b) :
    projection a ≠ projection b := fun same => changed (adequate a b same)

end XdsSpec.PublicationSelection
