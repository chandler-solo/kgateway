----------------------------- MODULE XdsEnvoyWarming -----------------------------
EXTENDS FiniteSets, TLC

\* A focused model of Envoy startup/warming semantics for LDS/RDS/CDS/EDS.
\*
\* Envoy ACKs individual xDS resources after validating them in isolation, but
\* ACK does not mean the configuration is active. Clusters warm until EDS
\* supplies a ClusterLoadAssignment. Listeners that refer to RDS warm until the
\* RouteConfiguration is supplied. Routes are not warmed, so the management
\* server must make sure route cluster references are already usable before
\* activating route updates.
\*
\* The safe spec checks two paths:
\*   1. cold startup from no active listener to a fully active listener
\*   2. make-before-break update from "old" to "new"
\*
\* The buggy specs demonstrate three common mistakes:
\*   - treating CDS ACK as cluster active before EDS arrives
\*   - activating an RDS route before the referenced cluster is active
\*   - activating an LDS listener before the referenced RDS config exists

Names == {"old", "new"}
MaybeName == Names \cup {"none"}

Phases == {
    "ColdEmpty",
    "ColdCdsAcked",
    "ColdClusterActive",
    "ColdRdsAcked",
    "ColdActive",
    "StableOld",
    "HotCdsAcked",
    "HotClusterActive",
    "HotRouteActive",
    "OldRemoved"
}

VARIABLES
    phase,
    cdsAcked,
    edsAcked,
    rdsAcked,
    ldsAcked,
    activeClusters,
    activeRouteCluster,
    activeListenerRoute

vars ==
    << phase,
       cdsAcked,
       edsAcked,
       rdsAcked,
       ldsAcked,
       activeClusters,
       activeRouteCluster,
       activeListenerRoute >>

ColdInit ==
    /\ phase = "ColdEmpty"
    /\ cdsAcked = {}
    /\ edsAcked = {}
    /\ rdsAcked = {}
    /\ ldsAcked = {}
    /\ activeClusters = {}
    /\ activeRouteCluster = "none"
    /\ activeListenerRoute = "none"

HotInit ==
    /\ phase = "StableOld"
    /\ cdsAcked = {"old"}
    /\ edsAcked = {"old"}
    /\ rdsAcked = {"old"}
    /\ ldsAcked = {"old"}
    /\ activeClusters = {"old"}
    /\ activeRouteCluster = "old"
    /\ activeListenerRoute = "old"

Init ==
    \/ ColdInit
    \/ HotInit

ColdReceiveCDS ==
    /\ phase = "ColdEmpty"
    /\ phase' = "ColdCdsAcked"
    /\ cdsAcked' = {"new"}
    /\ UNCHANGED << edsAcked, rdsAcked, ldsAcked, activeClusters, activeRouteCluster, activeListenerRoute >>

ColdReceiveEDS ==
    /\ phase = "ColdCdsAcked"
    /\ "new" \in cdsAcked
    /\ phase' = "ColdClusterActive"
    /\ edsAcked' = {"new"}
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

ColdReceiveRDS ==
    /\ phase = "ColdClusterActive"
    /\ "new" \in activeClusters
    /\ phase' = "ColdRdsAcked"
    /\ rdsAcked' = {"new"}
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, activeClusters, activeRouteCluster, activeListenerRoute >>

ColdReceiveLDS ==
    /\ phase = "ColdRdsAcked"
    /\ "new" \in rdsAcked
    /\ phase' = "ColdActive"
    /\ ldsAcked' = {"new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, activeClusters >>

HotReceiveCDS ==
    /\ phase = "StableOld"
    /\ phase' = "HotCdsAcked"
    /\ cdsAcked' = {"old", "new"}
    /\ UNCHANGED << edsAcked, rdsAcked, ldsAcked, activeClusters, activeRouteCluster, activeListenerRoute >>

HotReceiveEDS ==
    /\ phase = "HotCdsAcked"
    /\ "new" \in cdsAcked
    /\ phase' = "HotClusterActive"
    /\ edsAcked' = {"old", "new"}
    /\ activeClusters' = {"old", "new"}
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

HotReceiveRDS ==
    /\ phase = "HotClusterActive"
    /\ "new" \in activeClusters
    /\ phase' = "HotRouteActive"
    /\ rdsAcked' = {"old", "new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, activeClusters >>

HotRemoveOld ==
    /\ phase = "HotRouteActive"
    /\ activeRouteCluster = "new"
    /\ phase' = "OldRemoved"
    /\ cdsAcked' = {"new"}
    /\ edsAcked' = {"new"}
    /\ rdsAcked' = {"new"}
    /\ ldsAcked' = {"new"}
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << activeRouteCluster, activeListenerRoute >>

BuggyActivateClusterOnCDSAck ==
    /\ phase = "ColdCdsAcked"
    /\ phase' = "ColdClusterActive"
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

BuggyRouteBeforeClusterActive ==
    /\ phase = "HotCdsAcked"
    /\ phase' = "HotRouteActive"
    /\ rdsAcked' = {"old", "new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, activeClusters >>

BuggyListenerBeforeRouteConfig ==
    /\ phase = "ColdClusterActive"
    /\ phase' = "ColdActive"
    /\ ldsAcked' = {"new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, activeClusters >>

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEDS
    \/ ColdReceiveRDS
    \/ ColdReceiveLDS
    \/ HotReceiveCDS
    \/ HotReceiveEDS
    \/ HotReceiveRDS
    \/ HotRemoveOld
    \/ NoOp

AckImpliesActiveBugNext ==
    \/ ColdReceiveCDS
    \/ BuggyActivateClusterOnCDSAck
    \/ NoOp

RouteBeforeClusterBugNext ==
    \/ HotReceiveCDS
    \/ BuggyRouteBeforeClusterActive
    \/ NoOp

ListenerBeforeRouteBugNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEDS
    \/ BuggyListenerBeforeRouteConfig
    \/ NoOp

SafeSpec == Init /\ [][SafeNext]_vars

AckImpliesActiveBugSpec == ColdInit /\ [][AckImpliesActiveBugNext]_vars

RouteBeforeClusterBugSpec == HotInit /\ [][RouteBeforeClusterBugNext]_vars

ListenerBeforeRouteBugSpec == ColdInit /\ [][ListenerBeforeRouteBugNext]_vars

TypeOK ==
    /\ phase \in Phases
    /\ cdsAcked \subseteq Names
    /\ edsAcked \subseteq Names
    /\ rdsAcked \subseteq Names
    /\ ldsAcked \subseteq Names
    /\ activeClusters \subseteq Names
    /\ activeRouteCluster \in MaybeName
    /\ activeListenerRoute \in MaybeName

ActiveClustersHaveCDSAndEDS ==
    activeClusters \subseteq (cdsAcked \cap edsAcked)

ActiveRouteReferencesActiveCluster ==
    \/ activeRouteCluster = "none"
    \/ activeRouteCluster \in activeClusters

ActiveListenerHasRouteConfig ==
    \/ activeListenerRoute = "none"
    \/ activeListenerRoute \in rdsAcked

ActiveListenerAndRouteAgree ==
    activeListenerRoute = activeRouteCluster

StartupActiveOnlyAfterClosure ==
    phase = "ColdActive" =>
        /\ "new" \in activeClusters
        /\ "new" \in rdsAcked
        /\ "new" \in ldsAcked
        /\ activeRouteCluster = "new"

NoBreakBeforeMake ==
    "old" \notin activeClusters =>
        activeRouteCluster # "old"

=============================================================================
