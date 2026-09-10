/- RF-004/RF-013: descriptive distinctions characterized by the direct Envoy
   v1.39.1 probe. One cluster, one priority, one reachable upstream, no active
   health check. This does not model all load balancers or protocol execution. -/
namespace XdsSpec.EnvoyAvailability

inductive Endpoints where
  | missing | empty | healthy | unhealthy
  deriving BEq, Repr

def initialized : Endpoints → Bool
  | .missing => false
  | _ => true

def selectable (panicEnabled : Bool) : Endpoints → Bool
  | .healthy => true
  | .unhealthy => panicEnabled
  | _ => false

-- Initialization cannot be used as a traffic-success predicate.
#guard initialized .empty && !selectable true .empty
-- Even an unhealthy EDS host can be selectable under default panic behavior.
#guard selectable true .unhealthy
#guard !selectable false .unhealthy

-- A candidate rewarming cluster needs an EDS response event, including when
-- content/version are unchanged. Prior active state can keep serving meanwhile.
structure Rewarming where
  oldActive : Bool
  edsReplayed : Bool

def candidateInitialized (s : Rewarming) : Bool := s.edsReplayed
#guard !(candidateInitialized ⟨true, false⟩)
#guard candidateInitialized ⟨true, true⟩

theorem selectable_requires_initialization (p : Bool) (e : Endpoints)
    (h : selectable p e = true) : initialized e = true := by
  cases e <;> simp_all [selectable, initialized]

end XdsSpec.EnvoyAvailability
