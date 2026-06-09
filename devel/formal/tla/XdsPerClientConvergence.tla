-------------------------- MODULE XdsPerClientConvergence --------------------------
EXTENDS FiniteSets, TLC

\* A focused convergence model for kgateway per-client xDS publication.
\*
\* This model connects the issue-13868, issue-14184, and startup/warming
\* concerns in one small phase graph:
\*
\*   - a last-good per-client cache snapshot exists
\*   - a new input arrives but is partial, so KRT surfaces a delete/defer event
\*   - the cache retains last-good state during the defer window
\*   - inputs later become coherent
\*   - the coherent snapshot publishes with filtered EDS and a changed EDS version
\*   - Envoy learns CDS, sends a named EDS request, receives EDS, and then
\*     activates the new route/cluster snapshot
\*
\* The model intentionally uses one old cluster and one new cluster. The point
\* is convergence ordering, not resource cardinality.

Names == {"old", "new"}
Old == {"old"}
New == {"new"}
None == {}
Versions == {"none", "v1", "v2"}
Phases == {
    "StableOld",
    "DeferredPartial",
    "CoherentInput",
    "PublishedNew",
    "WarmingNew",
    "EdsResponded",
    "ActiveNew"
}
ComputedStates == {"nil", "partial", "coherent"}
KrtEvents == {"none", "delete", "add", "update"}
WatchStates == {"idle", "open", "responded", "suppressed"}

VARIABLES
    phase,
    desiredRefs,
    candidateCds,
    candidateEds,
    candidateEdsVersion,
    computedState,
    krtEvent,
    cacheRefs,
    cacheCds,
    cacheEds,
    cacheEdsVersion,
    lastGoodRefs,
    lastGoodCds,
    lastGoodEds,
    lastGoodEdsVersion,
    envoyKnownRefs,
    envoyKnownCds,
    envoyRequestedEds,
    clientEdsResources,
    clientEdsVersion,
    envoyActiveRefs,
    envoyActiveCds,
    envoyActiveEds,
    edsWatchState

vars ==
    << phase,
       desiredRefs,
       candidateCds,
       candidateEds,
       candidateEdsVersion,
       computedState,
       krtEvent,
       cacheRefs,
       cacheCds,
       cacheEds,
       cacheEdsVersion,
       lastGoodRefs,
       lastGoodCds,
       lastGoodEds,
       lastGoodEdsVersion,
       envoyKnownRefs,
       envoyKnownCds,
       envoyRequestedEds,
       clientEdsResources,
       clientEdsVersion,
       envoyActiveRefs,
       envoyActiveCds,
       envoyActiveEds,
       edsWatchState >>

CanRespond(resources, requestNames) ==
    resources \subseteq requestNames

CacheMatchesLastGood ==
    /\ cacheRefs = lastGoodRefs
    /\ cacheCds = lastGoodCds
    /\ cacheEds = lastGoodEds
    /\ cacheEdsVersion = lastGoodEdsVersion

CacheMatchesCandidate ==
    /\ cacheRefs = desiredRefs
    /\ cacheCds = candidateCds
    /\ cacheEds = candidateEds
    /\ cacheEdsVersion = candidateEdsVersion

CandidateClosed ==
    /\ desiredRefs \subseteq candidateCds
    /\ desiredRefs \subseteq candidateEds
    /\ candidateEds \subseteq candidateCds

CacheClosed ==
    /\ cacheRefs \subseteq cacheCds
    /\ cacheRefs \subseteq cacheEds
    /\ cacheEds \subseteq cacheCds

ActiveClosed ==
    /\ envoyActiveRefs \subseteq envoyActiveCds
    /\ envoyActiveRefs \subseteq envoyActiveEds

Init ==
    /\ phase = "StableOld"
    /\ desiredRefs = Old
    /\ candidateCds = Old
    /\ candidateEds = Old
    /\ candidateEdsVersion = "v1"
    /\ computedState = "coherent"
    /\ krtEvent = "none"
    /\ cacheRefs = Old
    /\ cacheCds = Old
    /\ cacheEds = Old
    /\ cacheEdsVersion = "v1"
    /\ lastGoodRefs = Old
    /\ lastGoodCds = Old
    /\ lastGoodEds = Old
    /\ lastGoodEdsVersion = "v1"
    /\ envoyKnownRefs = Old
    /\ envoyKnownCds = Old
    /\ envoyRequestedEds = Old
    /\ clientEdsResources = Old
    /\ clientEdsVersion = "v1"
    /\ envoyActiveRefs = Old
    /\ envoyActiveCds = Old
    /\ envoyActiveEds = Old
    /\ edsWatchState = "idle"

DeferPartialInput ==
    /\ phase = "StableOld"
    /\ phase' = "DeferredPartial"
    /\ desiredRefs' = New
    /\ candidateCds' = New
    /\ candidateEds' = None
    /\ candidateEdsVersion' = "none"
    /\ computedState' = "partial"
    /\ krtEvent' = "delete"
    /\ edsWatchState' = "idle"
    /\ UNCHANGED << cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds >>

InputBecomesCoherent ==
    /\ phase = "DeferredPartial"
    /\ phase' = "CoherentInput"
    /\ candidateEds' = New
    /\ candidateEdsVersion' = "v2"
    /\ computedState' = "coherent"
    /\ krtEvent' = "update"
    /\ UNCHANGED << desiredRefs, candidateCds,
                    cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, edsWatchState >>

PublishCoherent ==
    /\ computedState = "coherent"
    /\ CandidateClosed
    /\ ~CacheMatchesCandidate
    /\ phase' = "PublishedNew"
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = candidateCds
    /\ cacheEds' = candidateEds
    /\ cacheEdsVersion' = candidateEdsVersion
    /\ lastGoodRefs' = desiredRefs
    /\ lastGoodCds' = candidateCds
    /\ lastGoodEds' = candidateEds
    /\ lastGoodEdsVersion' = candidateEdsVersion
    /\ krtEvent' = "add"
    /\ edsWatchState' = "idle"
    /\ UNCHANGED << desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds >>

EnvoyLearnsCds ==
    /\ phase = "PublishedNew"
    /\ phase' = "WarmingNew"
    /\ envoyKnownRefs' = cacheRefs
    /\ envoyKnownCds' = cacheCds
    /\ envoyRequestedEds' = cacheCds
    /\ edsWatchState' = "open"
    /\ UNCHANGED << desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds >>

EdsWatchResponds ==
    /\ phase = "WarmingNew"
    /\ cacheEdsVersion # clientEdsVersion
    /\ CanRespond(cacheEds, envoyRequestedEds)
    /\ phase' = "EdsResponded"
    /\ edsWatchState' = "responded"
    /\ clientEdsResources' = cacheEds
    /\ clientEdsVersion' = cacheEdsVersion
    /\ UNCHANGED << desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds >>

ActivateNew ==
    /\ phase = "EdsResponded"
    /\ envoyKnownRefs \subseteq envoyKnownCds
    /\ envoyKnownRefs \subseteq clientEdsResources
    /\ phase' = "ActiveNew"
    /\ envoyActiveRefs' = envoyKnownRefs
    /\ envoyActiveCds' = envoyKnownCds
    /\ envoyActiveEds' = clientEdsResources
    /\ edsWatchState' = "idle"
    /\ UNCHANGED << desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion >>

BuggyClearCacheOnDelete ==
    /\ phase = "DeferredPartial"
    /\ krtEvent = "delete"
    /\ cacheRefs' = None
    /\ cacheCds' = None
    /\ cacheEds' = None
    /\ cacheEdsVersion' = "none"
    /\ UNCHANGED << phase, desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, edsWatchState >>

BuggyPublishPartial ==
    /\ phase = "DeferredPartial"
    /\ computedState = "partial"
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = candidateCds
    /\ cacheEds' = candidateEds
    /\ cacheEdsVersion' = "v2"
    /\ krtEvent' = "add"
    /\ UNCHANGED << phase, desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, edsWatchState >>

BuggyPublishStaleEds ==
    /\ phase = "CoherentInput"
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = candidateCds
    /\ cacheEds' = Old \cup New
    /\ cacheEdsVersion' = "v2"
    /\ UNCHANGED << phase, desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, edsWatchState >>

BuggyPublishWithoutEdsVersionChange ==
    /\ phase = "CoherentInput"
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = candidateCds
    /\ cacheEds' = candidateEds
    /\ cacheEdsVersion' = clientEdsVersion
    /\ UNCHANGED << phase, desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, edsWatchState >>

BuggyActivateBeforeEds ==
    /\ phase = "WarmingNew"
    /\ phase' = "ActiveNew"
    /\ envoyActiveRefs' = envoyKnownRefs
    /\ envoyActiveCds' = envoyKnownCds
    /\ edsWatchState' = "idle"
    /\ UNCHANGED << desiredRefs, candidateCds, candidateEds, candidateEdsVersion,
                    computedState, krtEvent,
                    cacheRefs, cacheCds, cacheEds, cacheEdsVersion,
                    lastGoodRefs, lastGoodCds, lastGoodEds, lastGoodEdsVersion,
                    envoyKnownRefs, envoyKnownCds, envoyRequestedEds,
                    clientEdsResources, clientEdsVersion,
                    envoyActiveEds >>

NoOp ==
    UNCHANGED vars

SafeNext ==
    \/ DeferPartialInput
    \/ InputBecomesCoherent
    \/ PublishCoherent
    \/ EnvoyLearnsCds
    \/ EdsWatchResponds
    \/ ActivateNew
    \/ NoOp

ClearOnDeleteBugNext ==
    \/ DeferPartialInput
    \/ BuggyClearCacheOnDelete
    \/ NoOp

PartialOverwriteBugNext ==
    \/ DeferPartialInput
    \/ BuggyPublishPartial
    \/ NoOp

StaleEdsBugNext ==
    \/ DeferPartialInput
    \/ InputBecomesCoherent
    \/ BuggyPublishStaleEds
    \/ NoOp

VersionReuseBugNext ==
    \/ DeferPartialInput
    \/ InputBecomesCoherent
    \/ BuggyPublishWithoutEdsVersionChange
    \/ NoOp

ActivateBeforeEdsBugNext ==
    \/ DeferPartialInput
    \/ InputBecomesCoherent
    \/ PublishCoherent
    \/ EnvoyLearnsCds
    \/ BuggyActivateBeforeEds
    \/ NoOp

NoPublishBugNext ==
    \/ DeferPartialInput
    \/ InputBecomesCoherent
    \/ NoOp

SafeSpec ==
    /\ Init
    /\ [][SafeNext]_vars
    /\ WF_vars(PublishCoherent)
    /\ WF_vars(EnvoyLearnsCds)
    /\ WF_vars(EdsWatchResponds)
    /\ WF_vars(ActivateNew)

ClearOnDeleteBugSpec == Init /\ [][ClearOnDeleteBugNext]_vars

PartialOverwriteBugSpec == Init /\ [][PartialOverwriteBugNext]_vars

StaleEdsBugSpec == Init /\ [][StaleEdsBugNext]_vars

VersionReuseBugSpec == Init /\ [][VersionReuseBugNext]_vars

ActivateBeforeEdsBugSpec == Init /\ [][ActivateBeforeEdsBugNext]_vars

NoPublishBugSpec == Init /\ [][NoPublishBugNext]_vars

TypeOK ==
    /\ phase \in Phases
    /\ desiredRefs \subseteq Names
    /\ candidateCds \subseteq Names
    /\ candidateEds \subseteq Names
    /\ candidateEdsVersion \in Versions
    /\ computedState \in ComputedStates
    /\ krtEvent \in KrtEvents
    /\ cacheRefs \subseteq Names
    /\ cacheCds \subseteq Names
    /\ cacheEds \subseteq Names
    /\ cacheEdsVersion \in Versions
    /\ lastGoodRefs \subseteq Names
    /\ lastGoodCds \subseteq Names
    /\ lastGoodEds \subseteq Names
    /\ lastGoodEdsVersion \in Versions
    /\ envoyKnownRefs \subseteq Names
    /\ envoyKnownCds \subseteq Names
    /\ envoyRequestedEds \subseteq Names
    /\ clientEdsResources \subseteq Names
    /\ clientEdsVersion \in Versions
    /\ envoyActiveRefs \subseteq Names
    /\ envoyActiveCds \subseteq Names
    /\ envoyActiveEds \subseteq Names
    /\ edsWatchState \in WatchStates

DeleteRetainsLastGood ==
    krtEvent = "delete" => CacheMatchesLastGood

PartialDoesNotOverwriteCache ==
    computedState = "partial" => CacheMatchesLastGood

CacheSnapshotClosed ==
    CacheClosed

AlignedEDSRequestRespondable ==
    envoyKnownCds = cacheCds => CanRespond(cacheEds, envoyRequestedEds)

EDSResourceSetChangeChangesVersion ==
    cacheEds # clientEdsResources => cacheEdsVersion # clientEdsVersion

ActiveSnapshotClosed ==
    ActiveClosed

CoherentInputCanPublish ==
    (computedState = "coherent" /\ CandidateClosed /\ ~CacheMatchesCandidate) => ENABLED PublishCoherent

CoherentNewEventuallyActive ==
    [](phase = "CoherentInput" => <> (phase = "ActiveNew"))

=============================================================================
