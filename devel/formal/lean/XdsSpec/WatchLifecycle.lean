/- Bounded operation-level watch reference. Reuses pinned cache admission;
   explicit lock/timer phases do not establish Go refinement. -/
import XdsSpec.CacheOracle
namespace XdsSpec.WatchLifecycle
open Lean

structure Bounds where
  keys : Nat
  streams : Nat
  generations : Nat
  resources : Nat
  queueCapacity : Nat
  events : Nat
  deriving FromJson, ToJson
structure Initial where
  key : String
  typ : String
  generation : Nat
  phase : String
  deriving FromJson, ToJson
structure Arguments where
  version : String
  names : List String
  generation : Nat
  deriving FromJson, ToJson
structure Event where
  id : String
  actor : String
  kind : String
  arguments : Arguments
  happensAfter : List String
  deriving FromJson, ToJson
structure Expectation where
  property : String
  outcome : String
  witnessOperationIds : List String
  deriving FromJson, ToJson
structure Request where
  schema : Nat
  scenarioId : String
  subjectId : String
  cacheProfile : String
  adsMode : String
  bounds : Bounds
  initialState : Initial
  events : List Event
  expectations : List Expectation
  deriving FromJson, ToJson
structure Packet where
  version : String
  names : List String
  generation : Nat
  deriving ToJson, Repr
structure State where
  version : String := "1"
  names : List String := ["A"]
  phase : String := "initial-read"
  captured : String := ""
  lock : String := "none"
  publication : String := "idle"
  watch : Bool := false
  watchOwner : Nat := 1
  generation : Nat := 1
  generations : List Nat := [1]
  timer : String := "off"
  timerOwner : Nat := 1
  own : Option Packet := none
  queued : Option Packet := none
  sent : Option Packet := none
  accepted : String := ""
  rejected : String := "1"
  nonce : String := "first-response"
  applied : String := ""
  interested : Bool := true
  channel : CacheOracle.Channel := {stream := 1, client := "key", typ := "eds", names := ["A"], wildcard := false, legacy := false, returned := [⟨"A", "1"⟩]}
  deriving ToJson
structure Observation where
  id : String
  state : State
  deriving ToJson

def snapshot (s : State) : PublicationOracle.Snapshot :=
  {clusters := [], endpoints := s.names.map (fun n => ⟨n, 0, false, "0"⟩), config := ⟨[], [], false, false, false, "", "", ""⟩, cdsVersion := "", edsVersion := s.version}

-- Named RDS uses the same admission rules as EDS in this profile. Full-state
-- CDS/LDS, TTLs and glob subscriptions require separate profiles.
def admission (profile : String) (s : State) (requestVersion : String) : Bool × Option Packet := Id.run do
  let snap := snapshot s
  let snap := if profile == "per-type" then
    {snap with endpoints := snap.endpoints.filter (fun e => s.channel.wildcard || s.channel.names.contains e.name)}
    else snap
  let (channel, response) := CacheOracle.admit (some snap) {s.channel with requestVersion}
  return (channel.watch, response.map (fun r => ⟨r.version, r.names, r.stream⟩))

def policy (subject : String) : Except String String :=
  match subject with
  | "watch-early" => .ok "early"
  | "watch-retry" => .ok "retry"
  | "watch-parking" | "watch-stack" | "watch-claims" | "watch-pertype" => .ok "parking"
  | _ => .error "unknown immutable subject"

def cancelOwned (s : State) (owner : Nat) : State :=
  {s with watch := s.watch && s.watchOwner != owner, timer := if s.timerOwner == owner then "off" else s.timer}

theorem oldCancelPreservesNewWatch (s : State) (old : Nat) (h : s.watchOwner ≠ old) :
    (cancelOwned s old).watch = s.watch := by simp [cancelOwned, h]

theorem cancellationDoesNotRetractQueue (s : State) (owner : Nat) :
    (cancelOwned s owner).queued = s.queued := rfl

def require (condition : Bool) (message : String) : Except String Unit :=
  if condition then .ok () else .error message

def step (p profile : String) (s : State) (e : Event) : Except String State := do
  let a := e.arguments
  let unlocked := if s.lock == "watch" then "none" else s.lock
  match e.kind with
  | "initial-read" =>
    require (s.phase == "initial-read") "wrong initial phase"
    if p == "parking" && s.lock != "none" then return {s with phase := "fallback"}
    require (s.publication != "fanout") "cache read blocked by publication"
    return {s with phase := if s.version == s.rejected then "initial-park" else "fallback", captured := s.version, lock := if p == "parking" then "watch" else s.lock}
  | "initial-park" =>
    require (s.phase == "initial-park" || s.phase == "fallback") "wrong admission phase"
    require (s.publication != "fanout") "cache admission blocked by publication"
    let (watch, response) := admission profile s (if s.phase == "fallback" then s.accepted else s.captured)
    require (response.isNone || s.queued.isNone) "blocked cache admission"
    return {s with phase := "waiting", watch, watchOwner := s.generation, timerOwner := s.generation, lock := unlocked, timer := if s.phase == "fallback" then "off" else "armed", queued := response.or s.queued}
  | "publish-begin" =>
    require (s.lock == "none" && s.publication == "idle") "publication lock unavailable"
    return {s with lock := "publish", publication := "install"}
  | "install" =>
    require (s.lock == "publish" && s.publication == "install" && !a.version.isEmpty) "invalid install"
    return {s with version := a.version, names := a.names, publication := "fanout"}
  | "fanout" | "publication-fail" =>
    require (s.lock == "publish" && s.publication == "fanout") "wrong fanout phase"
    let (_, response) := admission profile s s.rejected
    let deliver := s.watch && s.version != s.rejected && e.kind != "publication-fail"
    require (!deliver || response.isNone || s.queued.isNone) "blocked publication"
    return {s with lock := "none", publication := "idle", watch := if deliver && response.isSome then false else s.watch, queued := if deliver then response.or s.queued else s.queued}
  | "expire" =>
    require (s.timer == "armed") "timer not armed"
    return {s with timer := "due"}
  | "retry-begin" =>
    require (s.timer == "due" && s.phase == "waiting" && (p == "early" || s.lock == "none")) "retry cannot acquire locks"
    return {s with timer := "running", phase := "retry-read", lock := if p == "early" then s.lock else "watch"}
  | "retry-read" =>
    require (s.phase == "retry-read" && s.publication != "fanout") "wrong retry read phase"
    if s.version == s.rejected then
      return {s with captured := s.version, phase := if p == "early" then "retry-cancel" else "retry-replace"}
    return {s with captured := s.version, phase := "waiting", lock := unlocked, timer := if p == "early" then "off" else "armed"}
  | "retry-cancel" =>
    require (s.phase == "retry-cancel" && p == "early" && s.publication != "fanout") "cancel-before-replacement unavailable"
    return {s with watch := false, phase := "retry-replace"}
  | "retry-replace" =>
    require (s.phase == "retry-replace" && s.own.isNone && s.publication != "fanout") "wrong replacement phase"
    let (_, response) := admission profile s s.accepted
    return {s with own := response, phase := "waiting", lock := unlocked, watch := s.watch && response.isNone, timer := if response.isSome || p == "early" then "off" else "armed"}
  | "forward" =>
    let packet ← s.own.toExcept "no retry response"
    require s.queued.isNone "forwarding channel full"
    return {s with own := none, queued := some packet}
  | "send" =>
    let packet ← s.queued.toExcept "no queued response"
    require (packet.generation == s.generation && s.interested) "closed generation cannot send on successor"
    return {s with sent := some packet, queued := none, nonce := e.id}
  | "ack" =>
    let packet ← s.sent.toExcept "ACK without transport response"
    require (a.version == packet.version) "ACK version mismatch"
    return {s with accepted := packet.version}
  | "apply" =>
    let packet ← s.sent.toExcept "application without transport response"
    return {s with applied := packet.version}
  | "cancel" =>
    require (s.phase == "waiting" && s.publication != "fanout") "concurrent cancellation requires finer lock profile"
    return {cancelOwned s a.generation with interested := s.interested && a.generation != s.generation}
  | "reconnect" =>
    require (s.phase == "waiting" && s.lock == "none" && a.generation > 0 && !s.generations.contains a.generation) "invalid reconnect generation"
    return {cancelOwned s s.generation with generation := a.generation, generations := a.generation :: s.generations, channel := {s.channel with stream := a.generation}, phase := "initial-read", interested := true, sent := none, accepted := "", nonce := "first-response", applied := ""}
  | "drop-old-response" =>
    require ((s.queued.map (·.generation != s.generation)).getD false) "no retired response to discard"
    return {s with queued := none}
  | "observe" => return s
  | _ => throw "unknown watch operation"

def property (name : String) (s : State) : Except String Bool := do
  let response := s.own.isSome || s.queued.isSome || s.sent.isSome
  match name with
  | "request-accounting" => return !s.interested || s.watch || response || s.timer != "off" || s.phase != "waiting"
  | "parked-watch-retained" => return s.watch || response
  | "retry-persistence" => return !s.interested || !s.watch || response || s.phase != "waiting" || s.timer != "off"
  | "generation-ownership" => return !s.watch || s.watchOwner == s.generation
  | "installation-means-send" => return s.sent.isSome
  | "ack-means-application" => return s.accepted.isEmpty || !s.applied.isEmpty
  | _ => throw "property outside executable profile"

def run (r : Request) : Except String (List Observation × List Expectation) := do
  require (r.schema == 1 && !r.scenarioId.isEmpty) "unsupported watch schema"
  let p ← policy r.subjectId
  require (r.cacheProfile == (if r.subjectId == "watch-pertype" then "per-type" else "pinned-ads")) "cache profile does not match subject"
  require (["default", "ordered"].contains r.adsMode) "unknown ADS mode"
  require (r.bounds.keys == 1 && r.bounds.streams == 1 && r.bounds.generations > 0 && r.bounds.generations ≤ 2 && r.bounds.events > 0 && r.bounds.events ≤ 128 && !r.events.isEmpty &&
    r.bounds.resources == 2 && r.bounds.queueCapacity == 1 && r.events.length ≤ r.bounds.events) "unsupported bounds"
  require (["eds", "rds"].contains r.initialState.typ) "unsupported type"
  require (!r.initialState.key.isEmpty && r.initialState.generation > 0 &&
    ["initial-read", "waiting"].contains r.initialState.phase) "invalid initial state"
  let channel : CacheOracle.Channel := {stream := r.initialState.generation, client := r.initialState.key, typ := "eds", names := ["A"], wildcard := false, legacy := false, returned := [⟨"A", "1"⟩]}
  let waiting := r.initialState.phase == "waiting"
  let mut s : State := {channel, phase := r.initialState.phase, generation := r.initialState.generation, generations := [r.initialState.generation], watchOwner := r.initialState.generation, timerOwner := r.initialState.generation, watch := waiting, timer := if waiting then "armed" else "off"}
  let mut ids : List String := []
  let mut observations : List Observation := []
  for e in r.events do
    require (!e.id.isEmpty && !ids.contains e.id && e.happensAfter.all ids.contains) "invalid identity or happens-before edge"
    let actor := if ["initial-read", "initial-park", "cancel", "reconnect", "ack"].contains e.kind then "client"
      else if ["publish-begin", "install", "fanout", "publication-fail"].contains e.kind then "publication"
      else if ["expire", "retry-begin", "retry-read", "retry-cancel", "retry-replace", "forward"].contains e.kind then "retry"
      else if ["send", "drop-old-response"].contains e.kind then "transport" else "observer"
    require (e.actor == actor && e.happensAfter.eraseDups == e.happensAfter) "wrong actor or duplicate causal edge"
    require (e.arguments.names.length ≤ r.bounds.resources && e.arguments.names.eraseDups == e.arguments.names) "invalid resource names"
    require (e.kind == "install" || e.arguments.names.isEmpty) "names on non-install operation"
    require (["install", "ack"].contains e.kind || e.arguments.version.isEmpty) "version on wrong operation"
    require (["cancel", "reconnect"].contains e.kind || e.arguments.generation == 0) "generation on wrong operation"
    s ← step p r.cacheProfile s e
    require (s.generations.length ≤ r.bounds.generations) "generation bound exceeded"
    ids := ids ++ [e.id]
    observations := observations ++ [⟨e.id, s⟩]
  require (!r.expectations.isEmpty) "no required property"
  let mut results := []
  for expectation in r.expectations do
    require (["holds", "violated"].contains expectation.outcome && !expectation.witnessOperationIds.isEmpty) "unsupported or vacuous expectation"
    let mut holds := true
    for id in expectation.witnessOperationIds do
      let observation ← (observations.find? (·.id == id)).toExcept "unknown property witness"
      let value ← property expectation.property observation.state
      holds := holds && value
    results := results ++ [{expectation with outcome := if holds then "holds" else "violated"}]
  return (observations, results)
end XdsSpec.WatchLifecycle
