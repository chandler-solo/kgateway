import XdsSpec.GcpWatch
import XdsSpec.EnvoyAvailability

/- RF-017: one stable same-name CDS update, with EDS already returned at the
   current version. This composes the characterized response guard and warming
   response-event requirement. A later EDS revision is a recovery witness, not
   a production fix or a fairness assumption about permanently stable input. -/
namespace XdsSpec.RewarmingComposition

structure State where
  cache : GcpWatch.State := {}
  warming : Bool := true
  deriving BEq, Hashable, Repr

inductive Action where
  | request | republishSame | changeEDS | receive
  deriving Repr

def system (allowExternalRevision : Bool) : System State Action where
  name := if allowExternalRevision then "RewarmingWithExternalEDSRevision" else "StableEDSRewarmingStuck"
  init := {}
  actions := if allowExternalRevision then [.request, .republishSame, .changeEDS, .receive]
             else [.request, .republishSame, .receive]
  step := fun s a => match a with
    | .request => if !s.cache.response then some { s with cache := GcpWatch.request false s.cache } else none
    | .republishSame => some s
    | .changeEDS => some { s with cache := GcpWatch.publish false false s.cache }
    | .receive => if s.cache.response then some { s with warming := false } else none
  describeAction := reprStr

#guard match checkRecoverability (system false) (·.warming) (! ·.warming) with
  | .unreachableGoal _ _ => true | _ => false
#guard match checkRecoverability (system true) (·.warming) (! ·.warming) with
  | .ok _ => true | _ => false

end XdsSpec.RewarmingComposition
