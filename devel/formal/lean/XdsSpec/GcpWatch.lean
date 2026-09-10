import XdsSpec.Checker

/- Descriptive v0.14.0 named-watch seam (RF-007), not a full cache/server model.
   A is always requested/present; B varies. Snapshot and request versions are
   three-value revisions. Returned B and response ownership are explicit. Cache
   installation/cancellation interleavings and stream nonce handling are not
   represented here; see the source inventory for open obligations. -/
namespace XdsSpec.GcpWatch

structure State where
  snapshotB : Bool := false
  snapshotVersion : Nat := 0
  requestB : Bool := false
  requestVersion : Nat := 0
  returnedB : Bool := false
  pending : Bool := false
  watch : Bool := false
  response : Bool := false
  responseB : Bool := false
  responseVersion : Nat := 0
  deriving BEq, Hashable, Repr

inductive Action where
  | request | subscribeB | publishA | publishAB | ack | cancel
  deriving BEq, Repr

-- respond() uses the request's named superset guard. New unreturned B can
-- trigger CreateWatch at equal version, but cannot bypass this guard.
def compatible (s : State) : Bool := !s.snapshotB || s.requestB
def immediateEligible (s : State) : Bool :=
  s.snapshotVersion != s.requestVersion || (s.requestB && s.snapshotB && !s.returnedB)

def answer (s : State) : State :=
  { s with watch := false, response := true, responseB := s.snapshotB, responseVersion := s.snapshotVersion }

def request (retainDeclined : Bool) (s : State) : State :=
  let s := { s with pending := true, watch := false }
  if !immediateEligible s then { s with watch := true }
  else if compatible s then answer s
  else { s with watch := retainDeclined }

def publish (retainDeclined : Bool) (b : Bool) (s : State) : State :=
  let s := { s with snapshotB := b, snapshotVersion := (s.snapshotVersion + 1) % 3 }
  if s.watch && s.snapshotVersion != s.requestVersion then
    if compatible s then answer s else { s with watch := retainDeclined }
  else s

def step (retainDeclined : Bool) (s : State) (a : Action) : Option State :=
  match a with
  | .request => if !s.response then some (request retainDeclined s) else none
  | .subscribeB => if !s.response then some (request retainDeclined { s with requestB := true }) else none
  | .publishA => some (publish retainDeclined false s)
  | .publishAB => some (publish retainDeclined true s)
  | .ack => if s.response then some { s with
      pending := false, response := false, returnedB := s.responseB,
      requestVersion := s.responseVersion } else none
  | .cancel => some { s with pending := false, watch := false, response := false }

def system (retainDeclined : Bool) : System State Action where
  name := if retainDeclined then "GcpRetainedWatchPolicy" else "GcpV014LostWatch"
  init := {}
  actions := [.request, .subscribeB, .publishA, .publishAB, .ack, .cancel]
  step := step retainDeclined
  describeAction := reprStr

def requestAccountedFor (s : State) : Bool := !s.pending || s.watch || s.response

-- Immediate entry: request {A} against new {A,B} is discarded.
private def incompatible : State := { snapshotB := true, snapshotVersion := 1 }
#guard !(requestAccountedFor (request false incompatible))
#guard requestAccountedFor (request true incompatible)

-- Parked entry: equal-version request parks, then incompatible publish drops it.
#guard (request false {}).watch
#guard !(requestAccountedFor (publish false true (request false {})))
-- Realignment alone cannot wake a discarded watch; a new request can.
#guard !(publish false false (publish false true (request false {}))).response
#guard (request false (publish false false (publish false true (request false {})))).response

-- Retention preserves a waiter, but incompatible content is still not eligible.
#guard !(publish true true (request true {})).response
#guard (publish true true (request true {})).watch
-- Realignment at a distinct version permits delivery (request v0, snapshot v2).
#guard (publish true false (request true incompatible)).response

-- Same version, newly subscribed/unreturned B: an immediate response exists.
#guard (request false { snapshotB := true, requestB := true }).response
#guard !(request false { snapshotB := true, requestB := true, returnedB := true }).response

-- The characterized implementation violates waiter accounting; the proposed
-- retention policy preserves it over this entire finite transition graph.
#guard match checkSafety (system false) [("RequestAccountedFor", requestAccountedFor)] with
  | .violation _ _ _ => true | _ => false
#guard match checkSafety (system true) [("RequestAccountedFor", requestAccountedFor)] with
  | .ok _ => true | _ => false

end XdsSpec.GcpWatch
