import XdsSpec.Checker

/- RF-018: empty raw names have different meanings before and after an explicit
   subscription. The v0.14.0 snapshot response path ignores this history. This
   model covers one resource and a changed snapshot; nonce/queues remain open. -/
namespace XdsSpec.GcpSubscription

inductive Mode where
  | legacyWildcard | named | unsubscribed
  deriving BEq, Hashable, Repr
structure State where
  mode : Mode := .legacyWildcard
  sentUnrequested : Bool := false
  deriving BEq, Hashable, Repr
inductive Action where
  | subscribe | unsubscribe | changedSnapshot
  deriving Repr

def system (honorSubscription : Bool) : System State Action where
  name := if honorSubscription then "SubscriptionAwareResponsePolicy" else "GcpV014UnsubscribeLeak"
  init := {}
  actions := [.subscribe, .unsubscribe, .changedSnapshot]
  step := fun s a => match a with
    | .subscribe => some { s with mode := .named }
    | .unsubscribe => if s.mode != .legacyWildcard then some { s with mode := .unsubscribed } else some s
    | .changedSnapshot => some { s with
        sentUnrequested := s.sentUnrequested || (s.mode == .unsubscribed && !honorSubscription) }
  describeAction := reprStr

def onlyRequested (s : State) : Bool := !s.sentUnrequested
#guard match checkSafety (system false) [("OnlyRequested", onlyRequested)] with
  | .violation _ trace _ => trace.length == 3 | _ => false
#guard match checkSafety (system true) [("OnlyRequested", onlyRequested)] with
  | .ok 3 => true | _ => false

end XdsSpec.GcpSubscription
