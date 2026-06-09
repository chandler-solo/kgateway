---------------------------- MODULE XdsNamedEdsWatch ----------------------------
EXTENDS FiniteSets, TLC

\* This model isolates the go-control-plane ADS/SotW named EDS watch behavior
\* behind kgateway-dev/kgateway#14184.
\*
\* SnapshotCache responds to a named ADS request only when the response
\* resources are all explicitly named by the request. It also relies on
\* per-type version changes, or newly subscribed known resources, to decide
\* whether a watch should answer immediately. The safe spec models a CDS shrink
\* that filters EDS at the same time and bumps the EDS version. The buggy specs
\* demonstrate two issue-relevant hazards:
\*
\*   1. the EDS snapshot retains a stale ClusterLoadAssignment outside the
\*      request names, so the ADS guard suppresses the response
\*   2. the EDS resource set changes but the EDS version is reused, so the
\*      changed resource set is not publishable as a new SotW state

Clusters == {"cluster-a", "cluster-b"}
ClusterA == {"cluster-a"}
Versions == {"v1", "v2"}
WatchStates == {"idle", "open", "responded", "suppressed"}
Phases == {"StableAB", "ShrunkFiltered", "ShrunkStaleExtra", "FilteredNoVersionBump", "Acked"}

VARIABLES
    phase,
    requestNames,
    snapshotEds,
    snapshotVersion,
    clientVersion,
    clientResources,
    watchState,
    responseSent,
    lastResponseResources,
    lastResponseVersion

vars ==
    << phase,
       requestNames,
       snapshotEds,
       snapshotVersion,
       clientVersion,
       clientResources,
       watchState,
       responseSent,
       lastResponseResources,
       lastResponseVersion >>

CanRespond(resources, names) ==
    resources \subseteq names

NewlySubscribedExistingResources ==
    (requestNames \ clientResources) \cap snapshotEds

ShouldRespond ==
    \/ snapshotVersion # clientVersion
    \/ NewlySubscribedExistingResources # {}

CanSendResponse ==
    /\ ShouldRespond
    /\ CanRespond(snapshotEds, requestNames)

Init ==
    /\ phase = "StableAB"
    /\ requestNames = Clusters
    /\ snapshotEds = Clusters
    /\ snapshotVersion = "v1"
    /\ clientVersion = "v1"
    /\ clientResources = Clusters
    /\ watchState = "idle"
    /\ responseSent = FALSE
    /\ lastResponseResources = {}
    /\ lastResponseVersion = "v1"

SafeShrinkAndFilter ==
    /\ phase = "StableAB"
    /\ phase' = "ShrunkFiltered"
    /\ requestNames' = ClusterA
    /\ snapshotEds' = ClusterA
    /\ snapshotVersion' = "v2"
    /\ watchState' = "idle"
    /\ responseSent' = FALSE
    /\ UNCHANGED << clientVersion, clientResources, lastResponseResources, lastResponseVersion >>

BuggyShrinkWithStaleExtraEds ==
    /\ phase = "StableAB"
    /\ phase' = "ShrunkStaleExtra"
    /\ requestNames' = ClusterA
    /\ snapshotEds' = Clusters
    /\ snapshotVersion' = "v2"
    /\ watchState' = "idle"
    /\ responseSent' = FALSE
    /\ UNCHANGED << clientVersion, clientResources, lastResponseResources, lastResponseVersion >>

BuggyFilterWithoutVersionBump ==
    /\ phase = "StableAB"
    /\ phase' = "FilteredNoVersionBump"
    /\ requestNames' = ClusterA
    /\ snapshotEds' = ClusterA
    /\ snapshotVersion' = "v1"
    /\ watchState' = "idle"
    /\ responseSent' = FALSE
    /\ UNCHANGED << clientVersion, clientResources, lastResponseResources, lastResponseVersion >>

ProcessEdsRequestRespond ==
    /\ CanSendResponse
    /\ watchState' = "responded"
    /\ responseSent' = TRUE
    /\ lastResponseResources' = snapshotEds
    /\ lastResponseVersion' = snapshotVersion
    /\ UNCHANGED << phase, requestNames, snapshotEds, snapshotVersion, clientVersion, clientResources >>

ProcessEdsRequestOpen ==
    /\ ~ShouldRespond
    /\ watchState' = "open"
    /\ responseSent' = FALSE
    /\ UNCHANGED << phase, requestNames, snapshotEds, snapshotVersion,
                    clientVersion, clientResources,
                    lastResponseResources, lastResponseVersion >>

ProcessEdsRequestSuppressed ==
    /\ ShouldRespond
    /\ ~CanRespond(snapshotEds, requestNames)
    /\ watchState' = "suppressed"
    /\ responseSent' = FALSE
    /\ UNCHANGED << phase, requestNames, snapshotEds, snapshotVersion,
                    clientVersion, clientResources,
                    lastResponseResources, lastResponseVersion >>

AckResponse ==
    /\ responseSent
    /\ phase' = "Acked"
    /\ clientVersion' = lastResponseVersion
    /\ clientResources' = lastResponseResources
    /\ responseSent' = FALSE
    /\ watchState' = "idle"
    /\ UNCHANGED << requestNames, snapshotEds, snapshotVersion,
                    lastResponseResources, lastResponseVersion >>

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ SafeShrinkAndFilter
    \/ ProcessEdsRequestRespond
    \/ ProcessEdsRequestOpen
    \/ AckResponse
    \/ NoOp

StaleExtraBugNext ==
    \/ BuggyShrinkWithStaleExtraEds
    \/ ProcessEdsRequestRespond
    \/ ProcessEdsRequestSuppressed
    \/ AckResponse
    \/ NoOp

VersionReuseBugNext ==
    \/ BuggyFilterWithoutVersionBump
    \/ ProcessEdsRequestRespond
    \/ ProcessEdsRequestOpen
    \/ AckResponse
    \/ NoOp

SafeSpec == Init /\ [][SafeNext]_vars

StaleExtraBugSpec == Init /\ [][StaleExtraBugNext]_vars

VersionReuseBugSpec == Init /\ [][VersionReuseBugNext]_vars

TypeOK ==
    /\ phase \in Phases
    /\ requestNames \subseteq Clusters
    /\ snapshotEds \subseteq Clusters
    /\ snapshotVersion \in Versions
    /\ clientVersion \in Versions
    /\ clientResources \subseteq Clusters
    /\ watchState \in WatchStates
    /\ responseSent \in BOOLEAN
    /\ lastResponseResources \subseteq Clusters
    /\ lastResponseVersion \in Versions

ResponseOnlyForRequestedNames ==
    responseSent => CanRespond(lastResponseResources, requestNames)

ResourceSetChangeRequiresVersionChange ==
    snapshotEds # clientResources => snapshotVersion # clientVersion

ChangedSnapshotRequestRespondable ==
    snapshotVersion # clientVersion => CanRespond(snapshotEds, requestNames)

ChangedRespondableSnapshotCanSend ==
    (snapshotVersion # clientVersion /\ CanRespond(snapshotEds, requestNames)) => ENABLED ProcessEdsRequestRespond

NoSuppressedChangedResponse ==
    ~(watchState = "suppressed" /\ snapshotVersion # clientVersion)

=============================================================================
