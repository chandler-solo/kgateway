-------------------------- MODULE XdsPerClientPublication --------------------------
EXTENDS FiniteSets, Naturals, TLC

\* Combined model for the per-client publication properties behind
\* kgateway-dev/kgateway#13868 and kgateway-dev/kgateway#14184.
\*
\* This is intentionally phase-based instead of a free-form two-cluster editor.
\* The phases cover the two failure traces we care about:
\*
\*   1. reconnect -> only cluster-a is ready -> old code publishes partial CDS
\*   2. cluster-b removed from CDS -> stale cluster-b EDS resource remains
\*
\* The safe spec keeps the last coherent xDS cache snapshot during incomplete
\* phases, publishes only once inputs are coherent, and filters EDS resources to
\* the EDS clusters in the same CDS snapshot.

Clusters == {"cluster-a", "cluster-b"}
Phases == {
    "Stable",
    "ReconnectEmpty",
    "ReconnectPartial",
    "ReconnectCoherent",
    "ReconnectErrored",
    "StaleEDS",
    "StalePruned"
}
StreamIds == 1..2

VARIABLES
    phase,
    stream,
    desiredRefs,
    readyClusters,
    erroredClusters,
    endpointInputs,
    cacheRefs,
    cacheCds,
    cacheEds,
    cacheErrored,
    envoyKnownRefs,
    envoyKnownCds,
    envoyKnownErrored,
    envoyActiveRefs,
    envoyActiveCds,
    envoyActiveEds,
    envoyActiveErrored,
    envoyWarming,
    lastEdsResponseSent,
    publishedSinceInput

vars ==
    << phase,
       stream,
       desiredRefs,
       readyClusters,
       erroredClusters,
       endpointInputs,
       cacheRefs,
       cacheCds,
       cacheEds,
       cacheErrored,
       envoyKnownRefs,
       envoyKnownCds,
       envoyKnownErrored,
       envoyActiveRefs,
       envoyActiveCds,
       envoyActiveEds,
       envoyActiveErrored,
       envoyWarming,
       lastEdsResponseSent,
       publishedSinceInput >>

MissingReferencedClusters(refs, cds, errors) ==
    refs \ (cds \cup errors)

MissingReferencedEndpoints(refs, cds, eds, errors) ==
    ((refs \ errors) \cap cds) \ eds

FilteredEndpoints(cds, endpoints) ==
    endpoints \cap cds

CanRespond(edsResources, requestNames) ==
    edsResources \subseteq requestNames

CandidateCds == readyClusters

CandidateEds == FilteredEndpoints(CandidateCds, endpointInputs)

ClusterGate ==
    MissingReferencedClusters(desiredRefs, CandidateCds, erroredClusters) = {}

EndpointGate ==
    MissingReferencedEndpoints(desiredRefs, CandidateCds, CandidateEds, erroredClusters) = {}

InputsCoherent ==
    /\ ClusterGate
    /\ EndpointGate

CacheMatchesCandidate ==
    /\ cacheRefs = desiredRefs
    /\ cacheCds = CandidateCds
    /\ cacheEds = CandidateEds
    /\ cacheErrored = erroredClusters

Init ==
    /\ phase = "Stable"
    /\ stream = 1
    /\ desiredRefs = Clusters
    /\ readyClusters = Clusters
    /\ erroredClusters = {}
    /\ endpointInputs = Clusters
    /\ cacheRefs = Clusters
    /\ cacheCds = Clusters
    /\ cacheEds = Clusters
    /\ cacheErrored = {}
    /\ envoyKnownRefs = Clusters
    /\ envoyKnownCds = Clusters
    /\ envoyKnownErrored = {}
    /\ envoyActiveRefs = Clusters
    /\ envoyActiveCds = Clusters
    /\ envoyActiveEds = Clusters
    /\ envoyActiveErrored = {}
    /\ envoyWarming = FALSE
    /\ lastEdsResponseSent = TRUE
    /\ publishedSinceInput = TRUE

StartReconnect ==
    /\ phase = "Stable"
    /\ phase' = "ReconnectEmpty"
    /\ stream' = 2
    /\ readyClusters' = {}
    /\ erroredClusters' = {}
    /\ endpointInputs' = {}
    /\ publishedSinceInput' = FALSE
    /\ UNCHANGED << desiredRefs,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent >>

ReconnectClusterAReady ==
    /\ phase = "ReconnectEmpty"
    /\ phase' = "ReconnectPartial"
    /\ readyClusters' = {"cluster-a"}
    /\ endpointInputs' = {"cluster-a"}
    /\ UNCHANGED << stream, desiredRefs, erroredClusters,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent, publishedSinceInput >>

ReconnectClusterBReady ==
    /\ phase = "ReconnectPartial"
    /\ phase' = "ReconnectCoherent"
    /\ readyClusters' = Clusters
    /\ endpointInputs' = Clusters
    /\ UNCHANGED << stream, desiredRefs, erroredClusters,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent, publishedSinceInput >>

ReconnectClusterBErrored ==
    /\ phase = "ReconnectPartial"
    /\ phase' = "ReconnectErrored"
    /\ readyClusters' = {"cluster-a"}
    /\ erroredClusters' = {"cluster-b"}
    /\ UNCHANGED << stream, desiredRefs, endpointInputs,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent, publishedSinceInput >>

RemoveClusterBWithStaleEndpoint ==
    /\ phase = "Stable"
    /\ phase' = "StaleEDS"
    /\ desiredRefs' = {"cluster-a"}
    /\ readyClusters' = {"cluster-a"}
    /\ erroredClusters' = {}
    /\ endpointInputs' = Clusters
    /\ publishedSinceInput' = FALSE
    /\ UNCHANGED << stream,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent >>

PruneStaleEndpoint ==
    /\ phase = "StaleEDS"
    /\ phase' = "StalePruned"
    /\ endpointInputs' = {"cluster-a"}
    /\ UNCHANGED << stream, desiredRefs, readyClusters, erroredClusters,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent, publishedSinceInput >>

InputNext ==
    \/ StartReconnect
    \/ ReconnectClusterAReady
    \/ ReconnectClusterBReady
    \/ ReconnectClusterBErrored
    \/ RemoveClusterBWithStaleEndpoint
    \/ PruneStaleEndpoint

SafePublish ==
    /\ InputsCoherent
    /\ \/ ~CacheMatchesCandidate
       \/ publishedSinceInput = FALSE
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = CandidateCds
    /\ cacheEds' = CandidateEds
    /\ cacheErrored' = erroredClusters
    /\ publishedSinceInput' = TRUE
    /\ UNCHANGED << phase, stream, desiredRefs, readyClusters, erroredClusters, endpointInputs,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent >>

BuggyMissingClusterPublish ==
    /\ phase = "ReconnectPartial"
    /\ readyClusters \cup erroredClusters # {}
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = readyClusters
    /\ cacheEds' = FilteredEndpoints(readyClusters, endpointInputs)
    /\ cacheErrored' = erroredClusters
    /\ publishedSinceInput' = TRUE
    /\ UNCHANGED << phase, stream, desiredRefs, readyClusters, erroredClusters, endpointInputs,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent >>

BuggyStaleEdsPublish ==
    /\ phase = "StaleEDS"
    /\ InputsCoherent
    /\ cacheRefs' = desiredRefs
    /\ cacheCds' = CandidateCds
    /\ cacheEds' = endpointInputs
    /\ cacheErrored' = erroredClusters
    /\ publishedSinceInput' = TRUE
    /\ UNCHANGED << phase, stream, desiredRefs, readyClusters, erroredClusters, endpointInputs,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    envoyWarming, lastEdsResponseSent >>

DeliverControlPlaneSnapshot ==
    /\ \/ envoyKnownRefs # cacheRefs
       \/ envoyKnownCds # cacheCds
       \/ envoyKnownErrored # cacheErrored
       \/ envoyWarming = FALSE
    /\ envoyKnownRefs' = cacheRefs
    /\ envoyKnownCds' = cacheCds
    /\ envoyKnownErrored' = cacheErrored
    /\ envoyWarming' = TRUE
    /\ UNCHANGED << phase, stream, desiredRefs, readyClusters, erroredClusters, endpointInputs,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored,
                    lastEdsResponseSent, publishedSinceInput >>

EnvoyRequestEds ==
    LET canRespond == CanRespond(cacheEds, envoyKnownCds) IN
    LET canActivate ==
            /\ canRespond
            /\ envoyKnownRefs = cacheRefs
            /\ envoyKnownCds = cacheCds
            /\ envoyKnownErrored = cacheErrored
    IN
    /\ \/ lastEdsResponseSent # canRespond
       \/ canActivate
       \/ envoyWarming = TRUE
    /\ lastEdsResponseSent' = canRespond
    /\ IF canActivate
       THEN /\ envoyActiveRefs' = envoyKnownRefs
            /\ envoyActiveCds' = envoyKnownCds
            /\ envoyActiveEds' = cacheEds
            /\ envoyActiveErrored' = envoyKnownErrored
            /\ envoyWarming' = FALSE
       ELSE /\ UNCHANGED << envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored >>
            /\ envoyWarming' = TRUE
    /\ UNCHANGED << phase, stream, desiredRefs, readyClusters, erroredClusters, endpointInputs,
                    cacheRefs, cacheCds, cacheEds, cacheErrored,
                    envoyKnownRefs, envoyKnownCds, envoyKnownErrored,
                    publishedSinceInput >>

SafeNext ==
    \/ InputNext
    \/ SafePublish
    \/ DeliverControlPlaneSnapshot
    \/ EnvoyRequestEds

MissingClusterBugNext ==
    \/ InputNext
    \/ BuggyMissingClusterPublish
    \/ DeliverControlPlaneSnapshot
    \/ EnvoyRequestEds

StaleEdsBugNext ==
    \/ InputNext
    \/ BuggyStaleEdsPublish
    \/ DeliverControlPlaneSnapshot
    \/ EnvoyRequestEds

SafeSpec == Init /\ [][SafeNext]_vars

MissingClusterBugSpec == Init /\ [][MissingClusterBugNext]_vars

StaleEdsBugSpec == Init /\ [][StaleEdsBugNext]_vars

TypeOK ==
    /\ phase \in Phases
    /\ stream \in StreamIds
    /\ desiredRefs \subseteq Clusters
    /\ readyClusters \subseteq Clusters
    /\ erroredClusters \subseteq Clusters
    /\ readyClusters \cap erroredClusters = {}
    /\ endpointInputs \subseteq Clusters
    /\ cacheRefs \subseteq Clusters
    /\ cacheCds \subseteq Clusters
    /\ cacheEds \subseteq Clusters
    /\ cacheErrored \subseteq Clusters
    /\ cacheCds \cap cacheErrored = {}
    /\ envoyKnownRefs \subseteq Clusters
    /\ envoyKnownCds \subseteq Clusters
    /\ envoyKnownErrored \subseteq Clusters
    /\ envoyKnownCds \cap envoyKnownErrored = {}
    /\ envoyActiveRefs \subseteq Clusters
    /\ envoyActiveCds \subseteq Clusters
    /\ envoyActiveEds \subseteq Clusters
    /\ envoyActiveErrored \subseteq Clusters
    /\ envoyActiveCds \cap envoyActiveErrored = {}
    /\ envoyWarming \in BOOLEAN
    /\ lastEdsResponseSent \in BOOLEAN
    /\ publishedSinceInput \in BOOLEAN

CacheReferencesResolved ==
    MissingReferencedClusters(cacheRefs, cacheCds, cacheErrored) = {}

CacheReferencedEndpointsPresent ==
    MissingReferencedEndpoints(cacheRefs, cacheCds, cacheEds, cacheErrored) = {}

CacheHasNoOrphanEndpointResources ==
    cacheEds \subseteq cacheCds

CacheSnapshotCoherent ==
    /\ CacheReferencesResolved
    /\ CacheReferencedEndpointsPresent
    /\ CacheHasNoOrphanEndpointResources

EnvoyActiveReferencesResolved ==
    MissingReferencedClusters(envoyActiveRefs, envoyActiveCds, envoyActiveErrored) = {}

EnvoyActiveReferencedEndpointsPresent ==
    MissingReferencedEndpoints(envoyActiveRefs, envoyActiveCds, envoyActiveEds, envoyActiveErrored) = {}

EnvoyActiveHasNoOrphanEndpointResources ==
    envoyActiveEds \subseteq envoyActiveCds

EnvoyActiveSnapshotCoherent ==
    /\ EnvoyActiveReferencesResolved
    /\ EnvoyActiveReferencedEndpointsPresent
    /\ EnvoyActiveHasNoOrphanEndpointResources

AlignedEDSRequestRespondable ==
    envoyKnownCds = cacheCds => CanRespond(cacheEds, envoyKnownCds)

CoherentInputsCanPublish ==
    /\ InputsCoherent
    /\ \/ ~CacheMatchesCandidate
       \/ publishedSinceInput = FALSE
    =>
    ENABLED SafePublish

=============================================================================
