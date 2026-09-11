/-
XdsSpec.TraceCheck: conformance-check implementation traces against the
spec.

`make formal-lean` runs the proxy_syncer Go tests with XDS_TRACE_OUT set,
which records every snapshotPerClient decision (defer or publish, with
the snapshot data it was made on) as JSONL. This module replays those
events against the verified spec, instantiated at `Name := String`:

  - Publish closure: every referenced cluster that is not exempt
    (errored or the blackhole sentinel) must be present in CDS — the
    trace-level counterpart of `candidateClosed`/`CacheSnapshotClosed`
    and of issue 13868's publication gate.
  - EDS presence: every referenced EDS cluster's ClusterLoadAssignment
    (by service_name or cluster name) must be present. Empty assignments
    are permitted for first cache publication and per-cluster resolution.
    Only the fully ready transform decision requires usable endpoints.
  - No orphan CLAs: every published CLA must be induced by an EDS
    cluster in the same snapshot — issue 14184's
    `NoOrphanEndpointResources`.

Version digest discipline (assumption IMPL-A1) is deliberately NOT
checked at trace level: unit fixtures fabricate `EndpointsHash` values,
so version strings from different tests sharing a client name collide
meaninglessly. The real hash function's digest properties are discharged
by `TestFilterEndpointResourcesForClusters_VersionDigestProperties`
instead.

Defer events always conform (whether a defer was *necessary* is a
liveness question the trace cannot settle); they are parsed and counted
so a malformed emitter still fails loudly.
-/
import Lean.Data.Json
import XdsSpec.Spec

namespace XdsSpec.Trace

open Lean (Json)

structure TraceCluster where
  name : String
  eds : Bool
  edsName : String
  deriving Repr

structure TraceEndpoint where
  name : String
  usable : Bool
  deriving Repr

structure TraceEvent where
  schema : Nat := 1
  scenario : String := "fixture"
  sequence : Nat := 1
  client : String
  decision : String
  referenced : List String
  exempt : List String
  clusters : List TraceCluster
  endpoints : List TraceEndpoint
  endpointsVersion : String
  deriving Repr

def getStrList (j : Json) (field : String) : Except String (List String) := do
  let arr ← (← j.getObjVal? field).getArr?
  arr.toList.mapM (·.getStr?)

def parseCluster (j : Json) : Except String TraceCluster := do
  let name ← (← j.getObjVal? "name").getStr?
  let eds ← (← j.getObjVal? "eds").getBool?
  let edsName ← (← j.getObjVal? "edsName").getStr?
  if name.isEmpty || (eds && edsName.isEmpty) then throw "empty cluster identity"
  return { name, eds, edsName }

def parseEndpoint (j : Json) : Except String TraceEndpoint := do
  let name ← (← j.getObjVal? "name").getStr?
  let usable ← (← j.getObjVal? "usable").getBool?
  if name.isEmpty then throw "empty endpoint identity"
  return { name, usable }

def parseEvent (line : String) : Except String TraceEvent := do
  let j ← Json.parse line
  let schema ← (← j.getObjVal? "schema").getNat?
  if schema != 1 then throw s!"unsupported schema: {schema}"
  let scenario ← (← j.getObjVal? "scenario").getStr?
  let sequence ← (← j.getObjVal? "sequence").getNat?
  let client ← (← j.getObjVal? "client").getStr?
  if scenario.isEmpty || client.isEmpty || sequence == 0 then throw "missing trace identity"
  let decision ← (← j.getObjVal? "decision").getStr?
  unless ["publish", "publish-first", "publish-resolved",
      "defer-missing-role-snapshot", "defer-endpoints-not-ready",
      "defer-flip", "defer-first-publish"].contains decision do
    throw s!"unknown trace decision: {decision}"
  let referenced ← getStrList j "referenced"
  let exempt ← getStrList j "exempt"
  let clusters ← (← (← j.getObjVal? "clusters").getArr?).toList.mapM parseCluster
  let endpoints ← (← (← j.getObjVal? "endpoints").getArr?).toList.mapM parseEndpoint
  let endpointsVersion ← (← j.getObjVal? "endpointsVersion").getStr?
  return { schema, scenario, sequence, client, decision, referenced, exempt,
           clusters, endpoints, endpointsVersion }

/-- A conformance violation found in a trace. -/
structure Violation where
  lineNumber : Nat
  client : String
  rule : String
  detail : String

/-- Check a publish event. `requireUsable` distinguishes the publish
kinds: a coherent transform publish ("publish") requires every referenced
EDS cluster to carry a usable endpoint; a per-cluster resolution
("publish-resolved", emitted by syncXds) only requires the CLA to exist —
a previously-referenced cluster legitimately publishes an empty CLA when
its endpoints scale to zero (spec case C2), and held-flip compositions gate
usability upstream in the unit tests. First cache publication
("publish-first") also requires presence, not usable endpoints: there is
no cached route flip to protect. All three decisions check CDS closure
and orphan CLAs. These event checks do not prove transition conformance. -/
def checkPublish (e : TraceEvent) (requireUsable : Bool) :
    List (String × String) := Id.run do
  let mut violations := []
  let cdsNames := e.clusters.map (·.name)
  let required := e.referenced.filter (fun r => !e.exempt.contains r)
  -- Publish closure (issue 13868 gate): referenced ⊆ CDS.
  unless NameSet.subset required cdsNames do
    violations := violations ++ [("publish-closure",
      s!"referenced clusters {required} not all present in CDS {cdsNames}")]
  -- EDS readiness: every referenced EDS cluster has a CLA (and, for coherent
  -- transform publishes, one with a usable endpoint).
  for c in e.clusters do
    if c.eds && required.contains c.name then
      match e.endpoints.find? (·.name == c.edsName) with
      | none =>
        violations := violations ++ [("eds-readiness",
          s!"EDS cluster {c.name} has no CLA named {c.edsName}")]
      | some ep =>
        unless ep.usable || !requireUsable do
          violations := violations ++ [("eds-readiness",
            s!"EDS cluster {c.name} CLA {c.edsName} has no usable endpoint")]
  -- No orphan CLAs (issue 14184): every CLA is induced by an EDS cluster.
  let edsNames := (e.clusters.filter (·.eds)).map (·.edsName)
  for ep in e.endpoints do
    unless edsNames.contains ep.name do
      violations := violations ++ [("no-orphan-cla",
        s!"CLA {ep.name} has no EDS cluster in the same snapshot")]
  return violations

structure TraceSummary where
  events : Nat := 0
  publishes : Nat := 0
  defers : Nat := 0
  violations : List Violation := []

def checkTrace (lines : List String) : Except String TraceSummary := Id.run do
  let mut summary : TraceSummary := {}
  let mut lineNumber := 0
  let mut sequences : List (String × Nat) := []
  let mut terminal := false
  for line in lines do
    lineNumber := lineNumber + 1
    if line.isEmpty then
      continue
    if terminal then return .error "event after terminal receipt"
    match Json.parse line with
    | .ok j =>
      if (j.getObjVal? "terminal").isOk then
        let receipt : Except String Unit := do
          unless (← (← j.getObjVal? "terminal").getBool?) do throw "terminal must be true"
          unless (← (← j.getObjVal? "schema").getNat?) == 1 do throw "unsupported terminal schema"
          let scenario ← (← j.getObjVal? "scenario").getStr?
          let count ← (← j.getObjVal? "events").getNat?
          unless sequences == [(scenario, count)] && count == summary.events do
            throw "terminal scenario/count mismatch"
        match receipt with
        | .error err => return .error err
        | .ok _ => terminal := true
        continue
    | .error _ => pure ()
    match parseEvent line with
    | .error err => return .error s!"line {lineNumber}: malformed trace event: {err}"
    | .ok e =>
      let previous := (sequences.find? (fun pair => pair.1 == e.scenario)).map (·.2) |>.getD 0
      if e.sequence != previous + 1 then
        return .error s!"line {lineNumber}: sequence gap/duplicate for {e.scenario}: {previous} -> {e.sequence}"
      sequences := (e.scenario, e.sequence) :: sequences.filter (fun pair => pair.1 != e.scenario)
      summary := { summary with events := summary.events + 1 }
      if ["publish", "publish-first", "publish-resolved"].contains e.decision then
        summary := { summary with publishes := summary.publishes + 1 }
        let found := checkPublish e (requireUsable := e.decision == "publish")
        summary := { summary with
          violations := summary.violations ++ found.map fun (rule, detail) =>
            { lineNumber, client := e.client, rule, detail } }
      else
        summary := { summary with defers := summary.defers + 1 }
  if summary.events == 0 || summary.publishes == 0 then return .error "trace must contain a publication"
  if !terminal then return .error "missing terminal receipt (possibly truncated trace)"
  return .ok summary

/-- A first publication may be empty but must still be structurally closed. -/
private def firstEmpty : TraceEvent :=
  { client := "cold", decision := "publish-first", referenced := ["c"],
    exempt := [], clusters := [⟨"c", true, "service"⟩],
    endpoints := [⟨"service", false⟩], endpointsVersion := "v1" }

#guard (checkPublish firstEmpty false).isEmpty
#guard !(checkPublish firstEmpty true).isEmpty
#guard !(checkPublish { firstEmpty with clusters := [] } false).isEmpty
#guard !(checkPublish { firstEmpty with endpoints := [] } false).isEmpty
#guard !(checkPublish { firstEmpty with endpoints := [⟨"orphan", false⟩] } false).isEmpty
private def eventJSON (decision : String := "publish-first") (sequence : Nat := 1) : String :=
  (r#"{"schema":1,"scenario":"fixture","sequence":SEQ,"client":"cold","decision":"DECISION","referenced":["c"],"exempt":[],"clusters":[{"name":"c","eds":true,"edsName":"service"}],"endpoints":[{"name":"service","usable":false}],"endpointsVersion":"v1"}"#).replace "SEQ" (toString sequence) |>.replace "DECISION" decision

private def fails (result : Except String α) : Bool :=
  match result with | .error _ => true | .ok _ => false

#guard fails (parseEvent "{}")
#guard fails (parseEvent (eventJSON "publsih"))
#guard fails (checkTrace [])
#guard fails (checkTrace [eventJSON "defer-flip"])
#guard fails (checkTrace [eventJSON "publish-first" 2])
#guard fails (checkTrace [eventJSON, eventJSON])
private def terminalJSON (count : Nat) : String :=
  r#"{"schema":1,"scenario":"fixture","terminal":true,"events":COUNT}"#.replace "COUNT" (toString count)

#guard fails (checkTrace [eventJSON])
#guard fails (checkTrace [eventJSON, terminalJSON 2])
#guard fails (checkTrace [eventJSON, terminalJSON 1, eventJSON])
#guard fails (checkTrace [eventJSON, terminalJSON 1, terminalJSON 1])
#guard match checkTrace [eventJSON, eventJSON "publish-first" 2, terminalJSON 2] with
  | .ok summary => summary.publishes == 2 && summary.violations.isEmpty
  | .error _ => false

def runTraceCheck (paths : List String) : IO UInt32 := do
  let mut ok := true
  for path in paths do
    let contents ← IO.FS.readFile path
    match checkTrace (contents.splitOn "\n") with
    | .error err =>
      IO.println s!"FAIL  {path}: {err}"
      ok := false
    | .ok summary =>
      if summary.events == 0 then
        IO.println s!"FAIL  {path}: trace contains no events — emitter not wired?"
        ok := false
      else if summary.violations.isEmpty then
        IO.println s!"PASS  {path}: {summary.events} events ({summary.publishes} publishes, {summary.defers} defers) conform to the spec"
      else
        ok := false
        IO.println s!"FAIL  {path}: {summary.violations.length} violation(s) in {summary.events} events"
        for v in summary.violations do
          IO.println s!"  line {v.lineNumber} client {v.client} [{v.rule}]: {v.detail}"
  return if ok then 0 else 1

end XdsSpec.Trace
