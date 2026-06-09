----------------------------- MODULE XdsEnvoyWarming -----------------------------
EXTENDS FiniteSets, TLC

\* A focused model of Envoy startup/warming semantics for LDS/RDS/CDS/EDS.
\*
\* Envoy ACKs individual xDS resources after validating them in isolation, but
\* ACK does not mean the configuration is active. For this kgateway safety
\* model, a cluster is route-usable only after EDS supplies a ready
\* ClusterLoadAssignment. A missing CLA means no EDS response has arrived for
\* the cluster; an empty CLA means EDS was ACKed but does not contain usable
\* endpoints; a ready CLA can activate the cluster in the model. Listeners that
\* refer to RDS warm until the corresponding RouteConfiguration is supplied.
\* Routes are not warmed, so the management server must make sure route cluster
\* references are already usable before activating route updates.
\*
\* The safe spec checks two paths:
\*   1. cold startup from no active listener to a fully active listener
\*   2. make-before-break update from "old" to "new"
\*
\* The buggy specs demonstrate four common mistakes:
\*   - treating CDS ACK as cluster active before EDS arrives
\*   - treating an empty CLA as enough to activate a cluster
\*   - activating an RDS route before the referenced cluster is active
\*   - activating an LDS listener before the referenced RDS config exists

Names == {"old", "new"}
MaybeName == Names \cup {"none"}
CLAStates == {"MissingCLA", "EmptyCLA", "ReadyCLA"}

Phases == {
    "ColdEmpty",
    "ColdCdsAcked",
    "ColdEmptyClaAcked",
    "ColdClusterActive",
    "ColdRdsAcked",
    "ColdActive",
    "StableOld",
    "HotCdsAcked",
    "HotEmptyClaAcked",
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
    claState,
    activeClusters,
    activeRouteCluster,
    activeListenerRoute

vars ==
    << phase,
       cdsAcked,
       edsAcked,
       rdsAcked,
       ldsAcked,
       claState,
       activeClusters,
       activeRouteCluster,
       activeListenerRoute >>

ColdInit ==
    /\ phase = "ColdEmpty"
    /\ cdsAcked = {}
    /\ edsAcked = {}
    /\ rdsAcked = {}
    /\ ldsAcked = {}
    /\ claState = [n \in Names |-> "MissingCLA"]
    /\ activeClusters = {}
    /\ activeRouteCluster = "none"
    /\ activeListenerRoute = "none"

HotInit ==
    /\ phase = "StableOld"
    /\ cdsAcked = {"old"}
    /\ edsAcked = {"old"}
    /\ rdsAcked = {"old"}
    /\ ldsAcked = {"old"}
    /\ claState = [n \in Names |-> IF n = "old" THEN "ReadyCLA" ELSE "MissingCLA"]
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
    /\ UNCHANGED << edsAcked, rdsAcked, ldsAcked, claState, activeClusters, activeRouteCluster, activeListenerRoute >>

ColdReceiveEmptyEDS ==
    /\ phase = "ColdCdsAcked"
    /\ "new" \in cdsAcked
    /\ phase' = "ColdEmptyClaAcked"
    /\ edsAcked' = {"new"}
    /\ claState' = [claState EXCEPT !["new"] = "EmptyCLA"]
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeClusters, activeRouteCluster, activeListenerRoute >>

ColdReceiveReadyEDS ==
    /\ phase \in {"ColdCdsAcked", "ColdEmptyClaAcked"}
    /\ "new" \in cdsAcked
    /\ phase' = "ColdClusterActive"
    /\ edsAcked' = {"new"}
    /\ claState' = [claState EXCEPT !["new"] = "ReadyCLA"]
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

ColdReceiveRDS ==
    /\ phase = "ColdClusterActive"
    /\ "new" \in activeClusters
    /\ phase' = "ColdRdsAcked"
    /\ rdsAcked' = {"new"}
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, claState, activeClusters, activeRouteCluster, activeListenerRoute >>

ColdReceiveLDS ==
    /\ phase = "ColdRdsAcked"
    /\ "new" \in rdsAcked
    /\ phase' = "ColdActive"
    /\ ldsAcked' = {"new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, claState, activeClusters >>

HotReceiveCDS ==
    /\ phase = "StableOld"
    /\ phase' = "HotCdsAcked"
    /\ cdsAcked' = {"old", "new"}
    /\ UNCHANGED << edsAcked, rdsAcked, ldsAcked, claState, activeClusters, activeRouteCluster, activeListenerRoute >>

HotReceiveEmptyEDS ==
    /\ phase = "HotCdsAcked"
    /\ "new" \in cdsAcked
    /\ phase' = "HotEmptyClaAcked"
    /\ edsAcked' = {"old", "new"}
    /\ claState' = [claState EXCEPT !["new"] = "EmptyCLA"]
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeClusters, activeRouteCluster, activeListenerRoute >>

HotReceiveReadyEDS ==
    /\ phase \in {"HotCdsAcked", "HotEmptyClaAcked"}
    /\ "new" \in cdsAcked
    /\ phase' = "HotClusterActive"
    /\ edsAcked' = {"old", "new"}
    /\ claState' = [claState EXCEPT !["new"] = "ReadyCLA"]
    /\ activeClusters' = {"old", "new"}
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

HotReceiveRDS ==
    /\ phase = "HotClusterActive"
    /\ "new" \in activeClusters
    /\ phase' = "HotRouteActive"
    /\ rdsAcked' = {"old", "new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, claState, activeClusters >>

HotRemoveOld ==
    /\ phase = "HotRouteActive"
    /\ activeRouteCluster = "new"
    /\ phase' = "OldRemoved"
    /\ cdsAcked' = {"new"}
    /\ edsAcked' = {"new"}
    /\ rdsAcked' = {"new"}
    /\ ldsAcked' = {"new"}
    /\ claState' = [claState EXCEPT !["old"] = "MissingCLA"]
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << activeRouteCluster, activeListenerRoute >>

BuggyActivateClusterOnCDSAck ==
    /\ phase = "ColdCdsAcked"
    /\ phase' = "ColdClusterActive"
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, ldsAcked, claState, activeRouteCluster, activeListenerRoute >>

BuggyActivateClusterOnEmptyCLA ==
    /\ phase = "ColdEmptyClaAcked"
    /\ phase' = "ColdClusterActive"
    /\ activeClusters' = {"new"}
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, ldsAcked, claState, activeRouteCluster, activeListenerRoute >>

BuggyRouteBeforeClusterActive ==
    /\ phase = "HotCdsAcked"
    /\ phase' = "HotRouteActive"
    /\ rdsAcked' = {"old", "new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, claState, activeClusters >>

BuggyListenerBeforeRouteConfig ==
    /\ phase = "ColdClusterActive"
    /\ phase' = "ColdActive"
    /\ ldsAcked' = {"new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, rdsAcked, claState, activeClusters >>

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEmptyEDS
    \/ ColdReceiveReadyEDS
    \/ ColdReceiveRDS
    \/ ColdReceiveLDS
    \/ HotReceiveCDS
    \/ HotReceiveEmptyEDS
    \/ HotReceiveReadyEDS
    \/ HotReceiveRDS
    \/ HotRemoveOld
    \/ NoOp

AckImpliesActiveBugNext ==
    \/ ColdReceiveCDS
    \/ BuggyActivateClusterOnCDSAck
    \/ NoOp

EmptyCLAImpliesActiveBugNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEmptyEDS
    \/ BuggyActivateClusterOnEmptyCLA
    \/ NoOp

RouteBeforeClusterBugNext ==
    \/ HotReceiveCDS
    \/ BuggyRouteBeforeClusterActive
    \/ NoOp

ListenerBeforeRouteBugNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveReadyEDS
    \/ BuggyListenerBeforeRouteConfig
    \/ NoOp

SafeSpec == Init /\ [][SafeNext]_vars

AckImpliesActiveBugSpec == ColdInit /\ [][AckImpliesActiveBugNext]_vars

EmptyCLAImpliesActiveBugSpec == ColdInit /\ [][EmptyCLAImpliesActiveBugNext]_vars

RouteBeforeClusterBugSpec == HotInit /\ [][RouteBeforeClusterBugNext]_vars

ListenerBeforeRouteBugSpec == ColdInit /\ [][ListenerBeforeRouteBugNext]_vars

TypeOK ==
    /\ phase \in Phases
    /\ cdsAcked \subseteq Names
    /\ edsAcked \subseteq Names
    /\ rdsAcked \subseteq Names
    /\ ldsAcked \subseteq Names
    /\ claState \in [Names -> CLAStates]
    /\ activeClusters \subseteq Names
    /\ activeRouteCluster \in MaybeName
    /\ activeListenerRoute \in MaybeName

ActiveClustersHaveCDSAndEDS ==
    activeClusters \subseteq (cdsAcked \cap edsAcked)

ActiveClustersHaveReadyCLA ==
    \A c \in activeClusters: claState[c] = "ReadyCLA"

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
        /\ claState["new"] = "ReadyCLA"
        /\ "new" \in rdsAcked
        /\ "new" \in ldsAcked
        /\ activeRouteCluster = "new"

NoBreakBeforeMake ==
    "old" \notin activeClusters =>
        activeRouteCluster # "old"

=============================================================================
