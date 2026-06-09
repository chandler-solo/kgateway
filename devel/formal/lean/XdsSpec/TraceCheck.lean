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
  - EDS readiness: every referenced EDS cluster's ClusterLoadAssignment
    (by service_name or cluster name) must be present and have a usable
    endpoint — the `findMissingReferencedEndpointResources` gate that
    `activateNew`'s guard models.
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
  client : String
  decision : String
  referenced : List String
  exempt : List String
  clusters : List TraceCluster
  endpoints : List TraceEndpoint
  endpointsVersion : String
  deriving Repr

def getStrList (j : Json) (field : String) : Except String (List String) :=
  match j.getObjVal? field with
  | .error _ => .ok []
  | .ok v => do
    let arr ← v.getArr?
    arr.toList.mapM (·.getStr?)

def parseCluster (j : Json) : Except String TraceCluster := do
  let name ← (← j.getObjVal? "name").getStr?
  let eds ← match j.getObjVal? "eds" with
    | .error _ => pure false
    | .ok v => v.getBool?
  let edsName ← match j.getObjVal? "edsName" with
    | .error _ => pure ""
    | .ok v => v.getStr?
  return { name, eds, edsName }

def parseEndpoint (j : Json) : Except String TraceEndpoint := do
  let name ← (← j.getObjVal? "name").getStr?
  let usable ← match j.getObjVal? "usable" with
    | .error _ => pure false
    | .ok v => v.getBool?
  return { name, usable }

def parseEvent (line : String) : Except String TraceEvent := do
  let j ← Json.parse line
  let client ← (← j.getObjVal? "client").getStr?
  let decision ← (← j.getObjVal? "decision").getStr?
  let referenced ← getStrList j "referenced"
  let exempt ← getStrList j "exempt"
  let clusters ← match j.getObjVal? "clusters" with
    | .error _ => pure []
    | .ok v => do (← v.getArr?).toList.mapM parseCluster
  let endpoints ← match j.getObjVal? "endpoints" with
    | .error _ => pure []
    | .ok v => do (← v.getArr?).toList.mapM parseEndpoint
  let endpointsVersion ← match j.getObjVal? "endpointsVersion" with
    | .error _ => pure ""
    | .ok v => v.getStr?
  return { client, decision, referenced, exempt, clusters, endpoints, endpointsVersion }

/-- A conformance violation found in a trace. -/
structure Violation where
  lineNumber : Nat
  client : String
  rule : String
  detail : String

def checkPublish (e : TraceEvent) : List (String × String) := Id.run do
  let mut violations := []
  let cdsNames := e.clusters.map (·.name)
  let required := e.referenced.filter (fun r => !e.exempt.contains r)
  -- Publish closure (issue 13868 gate): referenced ⊆ CDS.
  unless NameSet.subset required cdsNames do
    violations := violations ++ [("publish-closure",
      s!"referenced clusters {required} not all present in CDS {cdsNames}")]
  -- EDS readiness: every referenced EDS cluster has a usable CLA.
  for c in e.clusters do
    if c.eds && required.contains c.name then
      match e.endpoints.find? (·.name == c.edsName) with
      | none =>
        violations := violations ++ [("eds-readiness",
          s!"EDS cluster {c.name} has no CLA named {c.edsName}")]
      | some ep =>
        unless ep.usable do
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
  for line in lines do
    lineNumber := lineNumber + 1
    if line.isEmpty then
      continue
    match parseEvent line with
    | .error err => return .error s!"line {lineNumber}: malformed trace event: {err}"
    | .ok e =>
      summary := { summary with events := summary.events + 1 }
      if e.decision == "publish" then
        summary := { summary with publishes := summary.publishes + 1 }
        let found := checkPublish e
        summary := { summary with
          violations := summary.violations ++ found.map fun (rule, detail) =>
            { lineNumber, client := e.client, rule, detail } }
      else
        summary := { summary with defers := summary.defers + 1 }
  return .ok summary

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
