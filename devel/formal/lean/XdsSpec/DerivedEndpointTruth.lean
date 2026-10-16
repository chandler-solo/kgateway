/- Presence-based publication policy selected after RF-002. These are guard
proofs and finite recovery checks, not a Go refinement or temporal theorem.
The historical contents gate remains an explicit counterexample profile. -/
import XdsSpec.Checker

namespace XdsSpec.DerivedEndpointTruth

def mayPublish (cdsPresent rowDerived _nonempty : Bool) : Bool :=
  cdsPresent && rowDerived

theorem healthDoesNotGate (cds derived a b : Bool) :
    mayPublish cds derived a = mayPublish cds derived b := rfl

theorem emptyTruthPublishes : mayPublish true true false = true := rfl
theorem missingRowDefers (nonempty : Bool) :
    mayPublish true false nonempty = false := rfl

structure State where
  derived : Bool := true
  nonempty : Bool := false
  pending : Bool := false
  published : Bool := false
  deriving DecidableEq, Repr, Hashable

inductive Action
  | independentUpdate | publish | legacyContentsGate
  deriving DecidableEq, Repr, Hashable

def step (s : State) : Action → Option State
  | .independentUpdate => some { s with pending := true }
  | .publish =>
    if s.pending && mayPublish true s.derived s.nonempty then
      some { s with published := true }
    else none
  | .legacyContentsGate =>
    if s.pending && s.derived && s.nonempty then
      some { s with published := true }
    else none

def system (legacy : Bool) : System State Action where
  name := if legacy then "HistoricalWarmContentsGate" else "DerivedEmptyTruth"
  init := {}
  actions := [.independentUpdate, if legacy then .legacyContentsGate else .publish]
  step := step
  describeAction := reprStr

def pending (s : State) : Bool := s.pending && !s.published

end XdsSpec.DerivedEndpointTruth
