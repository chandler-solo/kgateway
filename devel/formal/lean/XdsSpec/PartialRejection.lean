import XdsSpec.Checker

/- RF-026: SotW rejection semantics for a response carrying two resources of
   one type. `envoyprobe -scenario references` observed that Envoy v1.39.1
   applies the valid resource and NACKs the response for the invalid one, with
   the request reporting the previously accepted version. This finite model
   states the two candidate semantics and the property each one breaks:

   - atomic:  the whole response is discarded on any invalid resource. The
     accepted version then describes the applied state, but a valid sibling
     update is blocked by an unrelated invalid resource (no isolation).
   - partial: valid resources apply, the invalid one is dropped, and the
     accepted version stays behind. Isolation holds, but the accepted version
     no longer describes the applied state.

   The model is descriptive. It does not choose a policy, model retries, worker
   application, or per-type differences in reported dump versions. -/
namespace XdsSpec.PartialRejection

inductive Semantics where
  | atomicRejection | partialAcceptance
  deriving BEq, Repr

/-- Content revision of each resource as applied by the client, the client's
accepted (ACKed) version, and the control plane's view of that version. -/
structure State where
  appliedA : Nat := 0
  appliedB : Nat := 0
  acceptedVersion : Nat := 0
  serverAcceptedVersion : Nat := 0
  /-- Content of the latest response: version and whether B is invalid. -/
  responseVersion : Nat := 0
  responseBInvalid : Bool := false
  responded : Bool := false
  nacked : Bool := false
  deriving BEq, Hashable, Repr, Inhabited

inductive Action where
  /-- Publish version v+1 with A changed and B valid. -/
  | publishValid
  /-- Publish version v+1 with A changed and B invalid. -/
  | publishWithInvalidB
  /-- The client processes the outstanding response. -/
  | process
  deriving BEq, Repr

def step (sem : Semantics) (s : State) : Action → Option State
  | .publishValid =>
    if s.responded then none
    else some { s with responseVersion := s.acceptedVersion + 1, responseBInvalid := false, responded := true, nacked := false }
  | .publishWithInvalidB =>
    if s.responded then none
    else some { s with responseVersion := s.acceptedVersion + 1, responseBInvalid := true, responded := true, nacked := false }
  | .process =>
    if !s.responded then none
    else if !s.responseBInvalid then
      -- ACK: everything applies, both sides advance.
      some { s with appliedA := s.responseVersion, appliedB := s.responseVersion,
                    acceptedVersion := s.responseVersion, serverAcceptedVersion := s.responseVersion,
                    responded := false, nacked := false }
    else match sem with
      | .atomicRejection => some { s with responded := false, nacked := true }
      | .partialAcceptance =>
        -- Valid A applies; invalid B is dropped; the NACK carries the old version.
        some { s with appliedA := s.responseVersion, responded := false, nacked := true }

def system (sem : Semantics) : System State Action where
  name := match sem with
    | .atomicRejection => "SotwRejectionAtomicAbstraction"
    | .partialAcceptance => "SotwRejectionPartialEnvoy"
  init := {}
  actions := [.publishValid, .publishWithInvalidB, .process]
  step := step sem
  describeAction := reprStr

/-- The ACKed version describes what the client applied. -/
def acceptedVersionDescribesApplied (s : State) : Bool :=
  s.appliedA == s.acceptedVersion && s.appliedB == s.acceptedVersion

/-- After a NACK, a valid sibling's update was not blocked by the invalid one. -/
def validSiblingIsolated (s : State) : Bool :=
  !s.nacked || s.appliedA == s.responseVersion

/-- The control plane's accepted version equals the client's. This survives
under both semantics: the NACK request carries the old version honestly. -/
def serverViewMatchesClientVersion (s : State) : Bool :=
  s.serverAcceptedVersion == s.acceptedVersion

def invariants : List (String × (State → Bool)) :=
  [("AcceptedVersionDescribesApplied", acceptedVersionDescribesApplied),
   ("ValidSiblingIsolated", validSiblingIsolated),
   ("ServerViewMatchesClientVersion", serverViewMatchesClientVersion)]

-- Bounded exploration: versions grow without bound, so the checker is run on a
-- depth-limited copy via the version cap below.
def cappedStep (sem : Semantics) (s : State) (a : Action) : Option State :=
  match step sem s a with
  | some s' => if s'.responseVersion ≤ 3 then some s' else none
  | none => none

def capped (sem : Semantics) : System State Action where
  name := (system sem).name
  init := {}
  actions := [.publishValid, .publishWithInvalidB, .process]
  step := cappedStep sem
  describeAction := reprStr

-- Direct schedule checks (the observed Envoy trace).
private def observed : State :=
  (step .partialAcceptance ((step .partialAcceptance {} .publishWithInvalidB).get!) .process).get!
#guard observed.appliedA == 1 && observed.appliedB == 0 && observed.acceptedVersion == 0
#guard !acceptedVersionDescribesApplied observed
#guard validSiblingIsolated observed
#guard serverViewMatchesClientVersion observed

private def observedAtomic : State :=
  (step .atomicRejection ((step .atomicRejection {} .publishWithInvalidB).get!) .process).get!
#guard acceptedVersionDescribesApplied observedAtomic
#guard !validSiblingIsolated observedAtomic

-- Over the bounded graph each semantics violates exactly the property the
-- other satisfies, and the server's version view is honest in both.
#guard match checkSafety (capped .atomicRejection) [("ValidSiblingIsolated", validSiblingIsolated)] with
  | .violation _ _ _ => true | _ => false
#guard match checkSafety (capped .atomicRejection) [("AcceptedVersionDescribesApplied", acceptedVersionDescribesApplied), ("ServerViewMatchesClientVersion", serverViewMatchesClientVersion)] with
  | .ok _ => true | _ => false
#guard match checkSafety (capped .partialAcceptance) [("AcceptedVersionDescribesApplied", acceptedVersionDescribesApplied)] with
  | .violation _ _ _ => true | _ => false
#guard match checkSafety (capped .partialAcceptance) [("ValidSiblingIsolated", validSiblingIsolated), ("ServerViewMatchesClientVersion", serverViewMatchesClientVersion)] with
  | .ok _ => true | _ => false

end XdsSpec.PartialRejection
