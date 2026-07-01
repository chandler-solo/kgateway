/-
XdsSpec.OrderedADS: the ADS wire-delivery ordering layer (WithOrderedADS).

The convergence spec (XdsSpec.Spec) models what per-client *snapshot* kgateway
publishes, and XdsEnvoyWarming (TLA) models Envoy's *internal* warming. Between
them sits a layer neither captures: the order in which go-control-plane hands a
snapshot's resource types to Envoy on the ADS stream.

Even a fully consistent snapshot (CDS carries cluster C, RDS carries route R
which references C) is delivered type-by-type. The default server buffers each
type on its own channel and drains them with reflect.Select — uniformly at
random — so Envoy can apply RDS (route R) before CDS (cluster C) and briefly
serve 503 NC (no cluster) for R until CDS arrives. server.WithOrderedADS()
routes all types through one FIFO so the cache's ordered sends reach the wire
in dependency order (CDS before RDS), making additions drop-free.

WithOrderedADS fixes additions but NOT removals: its fixed CDS-before-RDS order
is the *wrong* order for a removal, which must drop the route (RDS) before the
cluster (CDS). Safe removal instead needs a de-reference grace window — the same
defer/last-good window the convergence spec relies on for GCP-A2.

The single safety invariant across both is ActiveRouteHasCluster: a route Envoy
has applied must reference a cluster Envoy has applied (no 503 NC).

Maps to the code at pkg/kgateway/setup/controlplane.go (the xdsserver.NewServer
call); see assumption GCP-A3 in devel/formal/lean/ASSUMPTIONS.md.
-/
import XdsSpec.Checker

namespace XdsSpec.OrderedADS

open XdsSpec

/-- What Envoy has applied from the ADS stream. `routeActive` means the RDS
update carrying route R (which references the cluster) has been applied;
`clusterPresent` means the CDS update carrying that cluster has been applied. -/
structure OAState where
  clusterPresent : Bool
  routeActive : Bool
  deriving DecidableEq, Repr, Hashable

inductive OAAction
  /-- Envoy applies the CDS update (cluster arrives). Always safe on its own. -/
  | deliverCDS
  /-- Envoy applies the RDS update. WithOrderedADS: only after CDS, because the
  single FIFO delivers CDS first. -/
  | deliverRDSOrdered
  /-- Envoy applies the RDS update with no ordering — the default reflect.Select
  server can deliver RDS before CDS. -/
  | deliverRDSUnordered
  /-- Envoy drops the route (RDS update no longer carries R). Always safe: a
  route that references nothing cannot 503-NC. -/
  | removeRDS
  /-- Envoy drops the cluster only once no active route references it — the
  de-reference grace window. -/
  | removeCDSGuarded
  /-- Envoy drops the cluster with no grace window. This is what a fixed
  CDS-before-RDS delivery order (WithOrderedADS) does to a *removal*: it
  removes the cluster before the route. -/
  | removeCDSFirst
  deriving DecidableEq, Repr, Hashable

def step (s : OAState) : OAAction → Option OAState
  | .deliverCDS => some { s with clusterPresent := true }
  | .deliverRDSOrdered =>
    if s.clusterPresent then some { s with routeActive := true } else none
  | .deliverRDSUnordered => some { s with routeActive := true }
  | .removeRDS => some { s with routeActive := false }
  | .removeCDSGuarded =>
    if !s.routeActive then some { s with clusterPresent := false } else none
  | .removeCDSFirst => some { s with clusterPresent := false }

def describe : OAAction → String
  | .deliverCDS => "deliverCDS"
  | .deliverRDSOrdered => "deliverRDS(ordered)"
  | .deliverRDSUnordered => "deliverRDS(unordered)"
  | .removeRDS => "removeRDS"
  | .removeCDSGuarded => "removeCDS(grace-window)"
  | .removeCDSFirst => "removeCDS(first)"

/-- No 503 NC: a route Envoy has applied references a cluster Envoy has. -/
def activeRouteHasCluster (s : OAState) : Bool :=
  !s.routeActive || s.clusterPresent

def invariantList : List (String × (OAState → Bool)) :=
  [("ActiveRouteHasCluster", activeRouteHasCluster)]

/-- Adding route R + cluster C: Envoy has neither yet. -/
def addInit : OAState := { clusterPresent := false, routeActive := false }

/-- Removing route R + cluster C: Envoy has both. -/
def removeInit : OAState := { clusterPresent := true, routeActive := true }

def mkSystem (name : String) (init : OAState) (actions : List OAAction) :
    System OAState OAAction where
  name := name
  init := init
  actions := actions
  step := step
  describeAction := describe

/-- WithOrderedADS: an addition delivers CDS before RDS, so the route is never
applied before its cluster. Drop-free. -/
def orderedAdditionSystem : System OAState OAAction :=
  mkSystem "OrderedADS-Addition" addInit [.deliverCDS, .deliverRDSOrdered]

/-- The default reflect.Select server: RDS may beat CDS to the wire, so Envoy
applies the route before its cluster — a transient 503 NC. -/
def unorderedAdditionBugSystem : System OAState OAAction :=
  mkSystem "UnorderedADS-AdditionBug" addInit [.deliverCDS, .deliverRDSUnordered]

/-- A removal with a de-reference grace window: the route (RDS) is dropped
before the cluster (CDS), so no active route ever references a gone cluster. -/
def gracefulRemovalSystem : System OAState OAAction :=
  mkSystem "GraceWindow-Removal" removeInit [.removeRDS, .removeCDSGuarded]

/-- WithOrderedADS does not reverse for removals: its fixed CDS-before-RDS order
removes the cluster before the route, so the still-active route 503-NCs. Ordered
ADS is insufficient for removals; only the grace window above is safe. -/
def orderedRemovalStillBrokenBugSystem : System OAState OAAction :=
  mkSystem "OrderedADS-RemovalBug" removeInit [.removeCDSFirst, .removeRDS]

end XdsSpec.OrderedADS
