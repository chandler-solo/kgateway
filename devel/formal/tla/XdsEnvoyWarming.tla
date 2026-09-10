----------------------------- MODULE XdsEnvoyWarming -----------------------------
EXTENDS FiniteSets, TLC

\* A focused model of Envoy startup/warming semantics for LDS/RDS/CDS/EDS.
\*
\* Initialization and backend availability are distinct. Receiving an empty
\* CLA can initialize a cluster with zero hosts. Cold publication permits this
\* degraded state; only the warm route-flip policy requires usable endpoints.
\* activeClusters means initialized clusters, not successful backend traffic.
\* ACK alone still does not establish initialization or dependency closure.
\*
\* The safe spec covers cold empty/ready startup and warm make-before-break.
\* ColdEmptySpec checks progress with permanently empty endpoints under named
\* weak-fairness assumptions. No endpoint-recovery action exists in that spec.
\* These are abstract policy/initialization models, not a proof of Envoy.

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
    /\ activeClusters' = {"old", "new"}
    /\ UNCHANGED << cdsAcked, rdsAcked, ldsAcked, activeRouteCluster, activeListenerRoute >>

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

ColdInitializeOnEmptyCLA ==
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

BuggyFlipOnEmptyCLA ==
    /\ phase = "HotEmptyClaAcked"
    /\ phase' = "HotRouteActive"
    /\ rdsAcked' = {"old", "new"}
    /\ activeRouteCluster' = "new"
    /\ activeListenerRoute' = "new"
    /\ UNCHANGED << cdsAcked, edsAcked, ldsAcked, claState, activeClusters >>

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEmptyEDS
    \/ ColdReceiveReadyEDS
    \/ ColdInitializeOnEmptyCLA
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

EmptyCLAFlipBugNext ==
    \/ HotReceiveCDS
    \/ HotReceiveEmptyEDS
    \/ BuggyFlipOnEmptyCLA
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

ColdEmptyNext ==
    \/ ColdReceiveCDS
    \/ ColdReceiveEmptyEDS
    \/ ColdInitializeOnEmptyCLA
    \/ ColdReceiveRDS
    \/ ColdReceiveLDS
    \/ NoOp

ColdEmptySpec ==
    /\ ColdInit
    /\ [][ColdEmptyNext]_vars
    /\ WF_vars(ColdReceiveCDS)
    /\ WF_vars(ColdReceiveEmptyEDS)
    /\ WF_vars(ColdInitializeOnEmptyCLA)
    /\ WF_vars(ColdReceiveRDS)
    /\ WF_vars(ColdReceiveLDS)

ColdEmptyEventuallyActive == <> (phase = "ColdActive")

SafeSpec == Init /\ [][SafeNext]_vars

AckImpliesActiveBugSpec == ColdInit /\ [][AckImpliesActiveBugNext]_vars

EmptyCLAFlipBugSpec == HotInit /\ [][EmptyCLAFlipBugNext]_vars

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

ActiveClustersHaveCLA ==
    \A c \in activeClusters: claState[c] # "MissingCLA"

WarmRouteFlipHasReadyCLA ==
    phase \in {"HotRouteActive", "OldRemoved"} => claState["new"] = "ReadyCLA"

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
        /\ claState["new"] # "MissingCLA"
        /\ "new" \in rdsAcked
        /\ "new" \in ldsAcked
        /\ activeRouteCluster = "new"

NoBreakBeforeMake ==
    "old" \notin activeClusters =>
        activeRouteCluster # "old"

=============================================================================
