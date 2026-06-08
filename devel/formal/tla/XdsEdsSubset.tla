----------------------------- MODULE XdsEdsSubset -----------------------------
EXTENDS FiniteSets, TLC

\* This model isolates the issue-14184 failure shape: a per-client ADS/SotW
\* snapshot whose EDS resource set retains an endpoint resource that no current
\* EDS cluster can request. In go-control-plane ADS mode, named EDS responses
\* are suppressed when the snapshot contains resources outside the request name
\* set. The safe spec filters EDS together with CDS. The buggy spec keeps stale
\* EDS resources after CDS shrinks and TLC can produce a counterexample.

Clusters == {"cluster-a", "cluster-b"}

VARIABLES
    cds,
    eds,
    requestedEds,
    responseSent

vars == << cds, eds, requestedEds, responseSent >>

CanRespond(edsResources, requestNames) ==
    edsResources \subseteq requestNames

Init ==
    /\ cds = Clusters
    /\ eds = Clusters
    /\ requestedEds = Clusters
    /\ responseSent = TRUE

SafeRemoveCluster ==
    /\ "cluster-b" \in cds
    /\ cds' = cds \ {"cluster-b"}
    /\ eds' = eds \ {"cluster-b"}
    /\ requestedEds' = cds'
    /\ responseSent' = CanRespond(eds', requestedEds')

BuggyRemoveCluster ==
    /\ "cluster-b" \in cds
    /\ cds' = cds \ {"cluster-b"}
    /\ eds' = eds
    /\ requestedEds' = cds'
    /\ responseSent' = CanRespond(eds', requestedEds')

RefreshEdsRequest ==
    /\ requestedEds' = cds
    /\ responseSent' = CanRespond(eds, requestedEds')
    /\ UNCHANGED << cds, eds >>

FilterOrphanEndpoints ==
    /\ eds' = eds \ (eds \ cds)
    /\ requestedEds' = cds
    /\ responseSent' = CanRespond(eds', requestedEds')
    /\ UNCHANGED cds

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ SafeRemoveCluster
    \/ RefreshEdsRequest
    \/ FilterOrphanEndpoints
    \/ NoOp

BuggyNext ==
    \/ BuggyRemoveCluster
    \/ RefreshEdsRequest
    \/ NoOp

SafeSpec == Init /\ [][SafeNext]_vars

BuggySpec == Init /\ [][BuggyNext]_vars

TypeOK ==
    /\ cds \subseteq Clusters
    /\ eds \subseteq Clusters
    /\ requestedEds \subseteq Clusters
    /\ responseSent \in BOOLEAN

NoOrphanEndpointResources ==
    eds \subseteq cds

EDSRequestRespondable ==
    CanRespond(eds, requestedEds)

ResponseMatchesADSGuard ==
    responseSent = CanRespond(eds, requestedEds)
=============================================================================
