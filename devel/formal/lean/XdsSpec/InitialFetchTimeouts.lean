/- Per-subscription timeout guards and provenance over unbounded timestamps,
   provider generations and names. This does not prove Envoy refinement,
   dispatcher timing, temporal liveness or complete initialization safety. -/
namespace XdsSpec.InitialFetchTimeouts

inductive Config where
  | unset | disabled | finite (seconds : Nat)
  deriving DecidableEq, Repr

def duration : Config → Option Nat
  | .unset => some 15
  | .disabled => none
  | .finite 0 => none
  | .finite (n + 1) => some (n + 1)

inductive Cause where
  | none | fresh | rejected | timeoutEmpty | timeoutCached
  deriving DecidableEq, Repr

structure State (Name Payload : Type) where
  provider : Nat
  deadline : Option Nat
  initialized : Bool
  cause : Cause
  assignment : Name
  cached : Option (Name × Payload)
  received : Nat
  accepted : Nat

variable {Name Payload : Type} [DecidableEq Name]

def cacheFor (name : Name) (cached : Option (Name × Payload)) : Option Payload :=
  cached.bind fun pair => if pair.1 = name then some pair.2 else none

def finish (s : State Name Payload) : State Name Payload :=
  { s with
    deadline := none
    initialized := true
    cause := if (cacheFor s.assignment s.cached).isSome then .timeoutCached else .timeoutEmpty }

def expire (s : State Name Payload) (provider now : Nat) : Option (State Name Payload) :=
  if provider = s.provider then
    if s.initialized then none else
      match s.deadline with
      | none => none
      | some deadline => if deadline ≤ now then some (finish s) else none
  else none

theorem expiryMatchesProvider {s t : State Name Payload} {provider now : Nat}
    (h : expire s provider now = some t) : provider = s.provider := by
  by_cases hg : provider = s.provider
  · exact hg
  · simp [expire, hg] at h

theorem expiryNeedsDeadline {s t : State Name Payload} {provider now : Nat}
    (h : expire s provider now = some t) : ∃ d, s.deadline = some d ∧ d ≤ now := by
  cases hd : s.deadline with
  | none => simp [expire, hd] at h
  | some d =>
    refine ⟨d, rfl, ?_⟩
    by_cases due : d ≤ now
    · exact due
    · simp [expire, hd, due] at h

theorem expiryFinishes {s t : State Name Payload} {provider now : Nat}
    (h : expire s provider now = some t) : t = finish s := by
  have hg := expiryMatchesProvider h
  obtain ⟨d, hd, due⟩ := expiryNeedsDeadline h
  cases hi : s.initialized with
  | false => exact (Option.some.inj (by simpa [expire, hg, hi, hd, due] using h)).symm
  | true => simp [expire, hi] at h

theorem expiryPreservesWire {s t : State Name Payload} {provider now : Nat}
    (h : expire s provider now = some t) :
    t.received = s.received ∧ t.accepted = s.accepted := by
  rw [expiryFinishes h]
  exact ⟨rfl, rfl⟩

theorem disabledCannotExpire (s : State Name Payload) (provider now : Nat)
    (h : s.deadline = none) : expire s provider now = none := by
  simp [expire, h]

theorem initializedCannotExpire (s : State Name Payload) (provider now : Nat)
    (h : s.initialized = true) : expire s provider now = none := by
  simp [expire, h]

theorem cacheHasMatchingIdentity {name : Name} {cached : Option (Name × Payload)}
    {payload : Payload} (h : cacheFor name cached = some payload) :
    cached = some (name, payload) := by
  cases cached with
  | none => simp [cacheFor] at h
  | some pair =>
    obtain ⟨key, value⟩ := pair
    by_cases hk : key = name
    · subst key
      simp [cacheFor] at h
      subst value
      rfl
    · simp [cacheFor, hk] at h

#guard duration .unset = some 15
#guard duration .disabled = none
#guard duration (.finite 0) = none
#guard duration (.finite 1) = some 1
#guard duration (.finite 15) = duration .unset
-- Same effective duration does not identify the same ConfigSource/provider.
#guard Config.unset ≠ Config.finite 15

end XdsSpec.InitialFetchTimeouts
