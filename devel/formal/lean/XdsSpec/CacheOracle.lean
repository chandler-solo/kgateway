/- Executable composition of publication and the pinned SotW SnapshotCache.
   Profile: 1cd1226616f5, ADS cache, one outstanding watch per stream/type,
   no TTLs or glob subscriptions. Response observations are cache-channel
   deliveries, not gRPC sends, ACKs or Envoy application. -/
import XdsSpec.PublicationOracle

namespace XdsSpec.CacheOracle
open Lean

structure Version where
  name : String
  version : String
  deriving FromJson, ToJson, BEq, Repr

structure Channel where
  stream : Nat
  client : String
  typ : String
  names : List String := []
  wildcard : Bool := true
  legacy : Bool := true
  returned : List Version := []
  requestVersion : String := ""
  watch : Bool := false
  deriving ToJson, Repr

structure Response where
  stream : Nat
  typ : String
  version : String
  names : List String
  returned : List Version
  snapshot : PublicationOracle.Snapshot
  deriving ToJson

structure HeldVersion where
  client : String
  typ : String
  version : String

structure State where
  publication : PublicationOracle.State := {}
  streams : List (Nat × String) := []
  generations : List Nat := []
  channels : List Channel := []
  held : List HeldVersion := []

structure Event where
  kind : String
  client : String
  stream : Nat
  typ : String
  names : List String
  version : String -- "held" refers to the last cache response for this client/type
  publication : Option PublicationOracle.Event
  deriving FromJson, ToJson

structure Request where
  schema : Nat
  profile : String
  events : List Event
  deriving FromJson, ToJson

structure Installed where
  client : String
  snapshot : PublicationOracle.Snapshot
  deriving ToJson

structure Observation where
  publication : Option PublicationOracle.Observation
  responses : List Response
  channels : List Channel
  installed : List Installed
  failed : Bool
  deriving ToJson

def types := ["cds", "eds", "lds", "rds", "sds"]
def fullState (typ : String) := typ == "cds" || typ == "lds"
def version (s : PublicationOracle.Snapshot) (typ : String) : String :=
  match typ with
  | "cds" => s.cdsVersion | "eds" => s.edsVersion
  | "lds" => s.config.listenerVersion | "rds" => s.config.routeVersion
  | "sds" => s.config.secretVersion | _ => ""

def names (s : PublicationOracle.Snapshot) (typ : String) : List String :=
  match typ with
  | "cds" => PublicationOracle.unique (s.clusters.map (·.name))
  | "eds" => PublicationOracle.unique (s.endpoints.map (·.name))
  | "lds" => ["oracle-listener"] | "rds" => ["oracle-routes"]
  | "sds" => ["oracle-secret"] | _ => []

def subscribe (c : Channel) (ns : List String) : Channel :=
  if c.legacy && ns.isEmpty then c else
  let wildcard := ns.contains "*"
  let named := PublicationOracle.unique (ns.filter (· != "*"))
  { c with
    names := named
    wildcard := wildcard
    legacy := false
    returned := if wildcard then c.returned else c.returned.filter (fun v => named.contains v.name) }

def compatible (s : PublicationOracle.Snapshot) (c : Channel) : Bool :=
  c.wildcard || (names s c.typ).all c.names.contains

def response (s : PublicationOracle.Snapshot) (c : Channel) : Option Response := do
  if !compatible s c then none else
  let vs := version s c.typ
  let all := (names s c.typ).filter fun n => c.wildcard || c.names.contains n
  let changed := all.filter fun n => !(c.returned.any (·.name == n))
  let deleted := c.returned.any fun v => !all.contains v.name
  if vs == c.requestVersion && changed.isEmpty && (!fullState c.typ || !deleted) then none else
  let sent := if vs != c.requestVersion || fullState c.typ then all else changed
  let returned := if vs != c.requestVersion || fullState c.typ then sent.map (⟨·, vs⟩)
    else c.returned ++ changed.map (⟨·, vs⟩)
  some ⟨c.stream, c.typ, vs, sent, returned, s⟩

-- Request admission and parked publication intentionally have different rules.
-- Immediate incompatibility loses the request on this pin; parked declines
-- retain the watch. Equal-version subscription changes are admission-only.
def admit (snap : Option PublicationOracle.Snapshot) (c : Channel) : Channel × Option Response :=
  match snap with
  | none => ({c with watch := true}, none)
  | some s =>
    if !compatible s c then ({c with watch := false}, none) else
    match response s c with
    | none => ({c with watch := true}, none)
    | some r => ({c with watch := false, returned := r.returned}, some r)

def remember (s : State) (client typ vs : String) : State :=
  {s with held := ⟨client, typ, vs⟩ :: s.held.filter (fun v => v.client != client || v.typ != typ)}

def observe (s : State) (p : Option PublicationOracle.Observation) (rs : List Response) (failed : Bool := false) : Observation :=
  ⟨p, rs, s.channels, s.publication.installed.map (fun (k, v) => ⟨k, v⟩), failed⟩

def step (s : State) (e : Event) : Except String (State × Observation) := do
  match e.kind with
  | "open" =>
    if s.generations.contains e.stream then throw "stream generation reused"
    let next := {s with streams := (e.stream, e.client) :: s.streams, generations := e.stream :: s.generations}
    return (next, observe next none [])
  | "close" =>
    if !(s.streams.any (·.1 == e.stream)) then throw "unknown stream"
    let next := {s with streams := s.streams.filter (·.1 != e.stream), channels := s.channels.filter (·.stream != e.stream)}
    return (next, observe next none [])
  | "restart" =>
    -- Controller/cache replacement closes watches; it is not ClearSnapshot.
    -- Candidate inputs belong to the harness and may be resubmitted later.
    let next := {s with publication := {s.publication with installed := []}, streams := [], channels := []}
    return (next, observe next none [])
  | "cancel" | "request" | "blocked-request" =>
    let client ← ((s.streams.find? (·.1 == e.stream)).map (·.2)).toExcept "unknown stream"
    if client != e.client || !types.contains e.typ then throw "wrong stream key or type"
    if e.names.any (fun n => n.endsWith "/*") then throw "glob subscriptions outside profile"
    let old := (s.channels.find? (fun c => c.stream == e.stream && c.typ == e.typ)).getD
      {stream := e.stream, client, typ := e.typ}
    let others := s.channels.filter fun c => c.stream != e.stream || c.typ != e.typ
    if e.kind == "cancel" then
      let next := {s with channels := {old with watch := false} :: others}
      return (next, observe next none [])
    else
      let vs := if e.version == "held" then
        ((s.held.find? (fun v => v.client == client && v.typ == e.typ)).map (·.version)).getD ""
        else e.version
      let c := {subscribe old e.names with requestVersion := vs, watch := false}
      if e.kind == "blocked-request" && (PublicationOracle.cache s.publication client).isSome then
        throw "blocked request requires empty cache"
      let (c, r) := admit (PublicationOracle.cache s.publication client) c
      let next := {s with channels := c :: others}
      let next := match r with | none => next | some v => remember next client e.typ v.version
      return (next, observe next none r.toList)
  | "publication" | "publication-cancel" =>
    let event ← e.publication.toExcept "publication event missing"
    if event.client != e.client then throw "publication key mismatch"
    if let some i := event.input then PublicationOracle.validate i
    let (pub, po) ← PublicationOracle.step s.publication event
    let mut next := {s with publication := pub}
    let mut responses := []
    let mut failed := false
    if po.wrote then
      let snap ← po.installed.toExcept "installation missing"
      -- CDS before EDS is the only ordering used by the cancellation schedule.
      for typ in types do
        for c in s.channels.filter (fun c => c.client == e.client && c.typ == typ && c.watch) do
          if !failed && c.requestVersion != version snap typ then
            if let some r := response snap c then
              if e.kind == "publication-cancel" && typ == "eds" then
                failed := true
              else
                responses := responses ++ [r]
                next := {next with channels := next.channels.map fun x =>
                  if x.stream == c.stream && x.typ == typ then {x with watch := false, returned := r.returned} else x}
                next := remember next e.client typ r.version
    if e.kind == "publication-cancel" && (!failed || !(responses.any (·.typ == "cds"))) then
      throw "partial-cancellation precondition absent"
    return (next, observe next (some po) responses failed)
  | _ => throw "unknown cache event"

def run (r : Request) : Except String (List Observation) := do
  if r.schema != 1 || r.profile != "research-cache-1cd122661-v1" then throw "unsupported cache schema/profile"
  let mut state : State := {}
  let mut out := []
  for e in r.events do
    if e.client.isEmpty then throw "empty client"
    let (next, observation) ← step state e
    state := next
    out := observation :: out
  return out.reverse

theorem incompatibleAdmissionLost (s : PublicationOracle.Snapshot) (c : Channel) (h : compatible s c = false) :
    (admit (some s) c).1.watch = false := by simp [admit, h]

theorem emptyCacheRetainsWatch (c : Channel) : (admit none c).1.watch = true := rfl

end XdsSpec.CacheOracle
