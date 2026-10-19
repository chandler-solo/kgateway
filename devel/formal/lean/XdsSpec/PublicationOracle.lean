/- Executable reference for the research publication policy, schema 1.
   Inputs are translated rows, NOT Go's computed readiness/selection flags.
   Lists deliberately replace KRT, caches and hash maps. A history controls
   candidate construction and publication separately. No delivery or Envoy
   application is inferred from an installation. -/
import Lean.Data.Json
import XdsSpec.PublicationSelection

namespace XdsSpec.PublicationOracle
open Lean

structure Cluster where
  name : String
  eds : Bool
  assignment : String
  payload : Nat
  hash : String
  deps : List String
  deriving FromJson, ToJson, BEq, Repr

structure Endpoint where
  name : String
  payload : Nat -- zero means a derived empty CLA; omission is absence
  unhealthy : Bool
  hash : String -- decimal UInt64; never a JSON floating-point number
  deriving FromJson, ToJson, BEq, Repr

structure Config where
  refs : List String
  ancillary : List String
  opaqueConfig : Bool
  dynamic : Bool
  weighted : Bool
  routeVersion : String
  listenerVersion : String
  secretVersion : String
  deriving FromJson, ToJson, BEq, Repr

structure Input where
  gateway : Bool
  clusters : List Cluster
  endpoints : List Endpoint
  bootstrap : List String
  errored : List String
  config : Config
  deriving FromJson, ToJson, BEq, Repr

structure Snapshot where
  clusters : List Cluster
  endpoints : List Endpoint
  config : Config
  cdsVersion : String
  edsVersion : String
  deriving FromJson, ToJson, BEq, Repr

structure Candidate where
  snapshot : Snapshot
  missingClusters : List String
  missingEndpoints : List String
  errored : List String
  deriving FromJson, ToJson, BEq, Repr

def unique (xs : List String) : List String := xs.eraseDups.mergeSort (· ≤ ·)
def findCluster (cs : List Cluster) (n : String) := cs.find? (·.name == n)
def findEndpoint (es : List Endpoint) (n : String) := es.find? (·.name == n)
def assignment (c : Cluster) := if c.assignment.isEmpty then c.name else c.assignment
def number (s : String) : UInt64 := s.toNat!.toUInt64
def foldHashes (hs : List String) : String := toString (hs.foldl (fun v h => v ^^^ number h) (0 : UInt64))

-- Independent implementation of the documented FNV-1a name contribution.
def nameHash (name : String) : String :=
  toString (name.toUTF8.data.foldl (fun (h : UInt64) b => (h ^^^ b.toUInt64) * 1099511628211) 14695981039346656037)

def synthesized (name : String) : Endpoint := ⟨name, 0, false, nameHash name⟩
def exempt (i : Input) (n : String) := i.errored.contains n || n == "blackhole-cluster"

def build (i : Input) : Option Candidate := do
  if !i.gateway then none else
  let missing := unique (i.config.refs.filter fun n => !exempt i n && (findCluster i.clusters n).isNone)
  let missingEndpoints := unique (i.config.refs.filter fun n =>
    !exempt i n && match findCluster i.clusters n with
      | none => false
      | some c => c.eds && (findEndpoint i.endpoints (assignment c)).isNone)
  let required := unique (i.bootstrap ++ (i.clusters.filter (·.eds)).map assignment)
  let endpoints := required.map fun n => (findEndpoint i.endpoints n).getD (synthesized n)
  some ⟨⟨i.clusters, endpoints, i.config, foldHashes (i.clusters.map (·.hash)),
      foldHashes (endpoints.map (·.hash))⟩, missing, missingEndpoints, unique i.errored⟩

def deferred (c : Candidate) := !c.missingClusters.isEmpty || !c.missingEndpoints.isEmpty

-- Explicit dependency graph, with conservative opaque/request-time fallback.
-- The adapter encodes these edges in real Envoy protos; Go extracts its own graph.
-- The research policy deliberately matches strings against the CDS inventory:
-- an EDS service-name equal to a CDS name is therefore a conservative retention
-- edge, even though it is not a semantic CDS dependency. The native fuzz corpus
-- e5b872685be363d7 preserves the counterexample to omitting that edge.
def retained (old : Snapshot) (errors : List String) : List String := Id.run do
  let eligible := old.clusters.filter fun c => !errors.contains c.name
  if old.config.opaqueConfig || old.config.dynamic then return eligible.map (·.name)
  let mut names := unique (old.config.refs ++ old.config.ancillary)
  for _ in [:old.clusters.length] do
    names := unique (names ++ (eligible.filter (fun c => names.contains c.name)).flatMap
      (fun c => c.deps ++ if c.eds && !c.assignment.isEmpty then [c.assignment] else []))
  return (eligible.filter fun c => names.contains c.name).map (·.name)

def resolve (c : Candidate) (old : Snapshot) : Snapshot := Id.run do
  let held := c.missingClusters.any (fun n => (findCluster old.clusters n).isNone) ||
    c.missingEndpoints.any (fun n => !old.config.refs.contains n)
  let needed := if held then retained old c.errored else c.missingClusters
  let carried := old.clusters.filter fun x => needed.contains x.name &&
    !c.errored.contains x.name && x.name != "blackhole-cluster" &&
    (findCluster c.snapshot.clusters x.name).isNone
  let clusters := c.snapshot.clusters ++ carried
  let mut endpoints := c.snapshot.endpoints
  for x in carried.filter (·.eds) do
    let n := assignment x
    -- This uses the executable function whose precedence/pairing contracts
    -- are proved in PublicationSelection, rather than copying its algorithm.
    let entry (e : Endpoint) : PublicationSelection.Entry Endpoint String := ⟨e, e.hash⟩
    match PublicationSelection.select false
        ((findEndpoint endpoints n).map entry)
        ((findEndpoint old.endpoints n).map entry) none with
      | some (_, e) => if (findEndpoint endpoints n).isNone then endpoints := endpoints ++ [e.payload]
      | none => pure ()
  return { c.snapshot with
    clusters, endpoints
    config := if held then old.config else c.snapshot.config
    cdsVersion := if carried.isEmpty then c.snapshot.cdsVersion else
      c.snapshot.cdsVersion ++ "-carry-" ++ foldHashes (carried.map fun x => nameHash x.name)
    edsVersion := foldHashes (endpoints.map (·.hash)) }

-- none means no installation, not an instruction to clear the cache.
def publish (c : Candidate) (old : Option Snapshot) : Option Snapshot :=
  if !deferred c then some c.snapshot
  else match old with
    | none => if c.missingClusters.isEmpty then some c.snapshot else none
    | some s => some (resolve c s)

def equivalent (a b : Candidate) : Bool :=
  a.missingClusters == b.missingClusters && a.missingEndpoints == b.missingEndpoints &&
  a.errored == b.errored && a.snapshot.cdsVersion == b.snapshot.cdsVersion &&
  a.snapshot.edsVersion == b.snapshot.edsVersion &&
  a.snapshot.config.routeVersion == b.snapshot.config.routeVersion &&
  a.snapshot.config.listenerVersion == b.snapshot.config.listenerVersion &&
  a.snapshot.config.secretVersion == b.snapshot.config.secretVersion

structure Event where
  kind : String
  client : String
  id : Nat
  other : Nat
  input : Option Input
  deriving FromJson, ToJson, Repr

structure Request where
  schema : Nat
  profile : String
  events : List Event
  deriving FromJson, ToJson

structure Observation where
  candidate : Option Candidate
  installed : Option Snapshot
  wrote : Bool
  equal : Option Bool
  deriving ToJson

structure State where
  candidates : List (String × Nat × Option Candidate) := []
  installed : List (String × Snapshot) := []

def cache (s : State) (client : String) := (s.installed.find? (·.1 == client)).map (·.2)

def _root_.Option.toExcept (o : Option α) (error : String) : Except String α :=
  match o with | some a => .ok a | none => .error error

def step (s : State) (e : Event) : Except String (State × Observation) := do
  let old := cache s e.client
  match e.kind with
  | "build" =>
    let i ← e.input.toExcept "build requires input"
    if s.candidates.any (fun x => x.1 == e.client && x.2.1 == e.id) then throw "duplicate candidate id"
    let c := build i
    return (⟨(e.client, e.id, c) :: s.candidates, s.installed⟩, ⟨c, old, false, none⟩)
  | "publish" =>
    let row ← (s.candidates.find? (fun x => x.1 == e.client && x.2.1 == e.id)).toExcept "unknown candidate"
    let chosen := row.2.2.bind fun c => publish c old
    let installed := match chosen with
      | none => s.installed
      | some snap => (e.client, snap) :: s.installed.filter (·.1 != e.client)
    return (⟨s.candidates, installed⟩, ⟨row.2.2, chosen.or old, chosen.isSome, none⟩)
  | "delete" => -- the research KRT delete callback retains served cache
    return (s, ⟨none, old, false, none⟩)
  | "equal" =>
    let a ← (s.candidates.find? (fun x => x.1 == e.client && x.2.1 == e.id)).toExcept "unknown left candidate"
    let b ← (s.candidates.find? (fun x => x.1 == e.client && x.2.1 == e.other)).toExcept "unknown right candidate"
    let equal := match a.2.2, b.2.2 with
      | some x, some y => equivalent x y
      | none, none => true
      | _, _ => false
    return (s, ⟨none, old, false, some equal⟩)
  | _ => throw s!"unknown event {e.kind}"

def validHash (s : String) : Bool := match s.toNat? with
  | some n => n < 2^64 && toString n == s
  | none => false

def validate (i : Input) : Except String Unit := do
  if (i.clusters.map (·.name)).eraseDups.length != i.clusters.length then throw "duplicate cluster"
  if (i.endpoints.map (·.name)).eraseDups.length != i.endpoints.length then throw "duplicate endpoint"
  if i.clusters.any (fun c => c.name.isEmpty || !validHash c.hash || i.errored.contains c.name) then throw "invalid cluster"
  if i.endpoints.any (fun e => e.name.isEmpty || !validHash e.hash || (e.payload == 0 && e.unhealthy)) then throw "invalid endpoint"
  if i.bootstrap.any (fun n => (findEndpoint i.endpoints n).isNone) then throw "bootstrap row absent"

def run (r : Request) : Except String (List Observation) := do
  if r.schema != 1 || r.profile != "research-publication-v1" then throw "unsupported schema/profile"
  let mut s : State := {}
  let mut out := []
  for e in r.events do
    if e.client.isEmpty then throw "empty client"
    if e.kind != "build" && e.input.isSome then throw "only build accepts input"
    if let some i := e.input then validate i
    let (next, o) ← step s e
    s := next
    out := o :: out
  return out.reverse

-- These guard/provenance statements are about the actual executable oracle.
-- They intentionally make no claim about Go, hashing injectivity or liveness.
theorem missingGatewayProducesNoCandidate (i : Input) (h : i.gateway = false) : build i = none := by
  simp [build, h]

theorem nondeferredPublishes (c : Candidate) (old : Option Snapshot) (h : deferred c = false) :
    publish c old = some c.snapshot := by simp [publish, h]

theorem coldMissingCDSHolds (c : Candidate) (h : c.missingClusters.isEmpty = false) :
    publish c none = none := by simp [publish, deferred, h]

theorem deleteRetainsInstallation (s : State) (e : Event) (h : e.kind = "delete") :
    step s e = .ok (s, ⟨none, cache s e.client, false, none⟩) := by simp [step, h]; rfl

#guard nameHash "" == "14695981039346656037"
#guard nameHash "a" == "12638187200555641996"
#guard foldHashes ["18", "20", "22"] == "16" -- preserve the known XOR hazard

end XdsSpec.PublicationOracle
