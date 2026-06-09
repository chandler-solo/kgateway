-------------------------- MODULE XdsReconnectRace13868 --------------------------
EXTENDS FiniteSets, Naturals, TLC

\* This model isolates the reconnect-time race fixed by
\* https://github.com/kgateway-dev/kgateway/pull/13868.
\*
\* A reconnect creates a fresh per-client computation while the xDS cache keeps
\* the last coherent snapshot. Routes/listeners may already reference both
\* clusters, but per-client cluster translation can become visible one cluster
\* at a time. The 13868 readiness gate should prevent overwriting the retained
\* cache snapshot until every referenced cluster is either present in CDS or
\* explicitly known to be errored.
\*
\* The safe spec models the 13868 gate. The buggy spec models the old partial
\* publish shape: once any per-client cluster result exists, the server may
\* publish a snapshot even if another referenced cluster is still missing.

Clusters == {"cluster-a", "cluster-b"}
StreamIds == 1..2

VARIABLES
    stream,
    refs,
    ready,
    errored,
    cacheClusters,
    cacheErrored,
    publishedThisStream

vars == << stream, refs, ready, errored, cacheClusters, cacheErrored, publishedThisStream >>

MissingReferencedClusters(rs, clusters, errors) ==
    rs \ (clusters \cup errors)

ReadyGate(rs, clusters, errors) ==
    MissingReferencedClusters(rs, clusters, errors) = {}

OldNonNilGuard(clusters, errors) ==
    clusters \cup errors # {}

Init ==
    /\ stream = 1
    /\ refs = Clusters
    /\ ready = {}
    /\ errored = {}
    \* The cache starts with the last coherent snapshot retained across reconnect.
    /\ cacheClusters = Clusters
    /\ cacheErrored = {}
    /\ publishedThisStream = FALSE

ClusterReady(c) ==
    /\ c \in Clusters
    /\ c \notin ready
    /\ c \notin errored
    /\ ready' = ready \cup {c}
    /\ UNCHANGED << stream, refs, errored, cacheClusters, cacheErrored, publishedThisStream >>

ClusterErrored(c) ==
    /\ c \in Clusters
    /\ c \notin ready
    /\ c \notin errored
    /\ errored' = errored \cup {c}
    /\ UNCHANGED << stream, refs, ready, cacheClusters, cacheErrored, publishedThisStream >>

Reconnect ==
    /\ stream = 1
    /\ stream' = 2
    /\ ready' = {}
    /\ errored' = {}
    /\ publishedThisStream' = FALSE
    /\ UNCHANGED << refs, cacheClusters, cacheErrored >>

SafePublish ==
    /\ ReadyGate(refs, ready, errored)
    /\ cacheClusters' = ready
    /\ cacheErrored' = errored
    /\ publishedThisStream' = TRUE
    /\ UNCHANGED << stream, refs, ready, errored >>

BuggyPublish ==
    /\ OldNonNilGuard(ready, errored)
    /\ cacheClusters' = ready
    /\ cacheErrored' = errored
    /\ publishedThisStream' = TRUE
    /\ UNCHANGED << stream, refs, ready, errored >>

SafeNext ==
    \/ \E c \in Clusters: ClusterReady(c)
    \/ \E c \in Clusters: ClusterErrored(c)
    \/ Reconnect
    \/ SafePublish

BuggyNext ==
    \/ \E c \in Clusters: ClusterReady(c)
    \/ \E c \in Clusters: ClusterErrored(c)
    \/ Reconnect
    \/ BuggyPublish

SafeSpec == Init /\ [][SafeNext]_vars

BuggySpec == Init /\ [][BuggyNext]_vars

TypeOK ==
    /\ stream \in StreamIds
    /\ refs \subseteq Clusters
    /\ ready \subseteq Clusters
    /\ errored \subseteq Clusters
    /\ ready \cap errored = {}
    /\ cacheClusters \subseteq Clusters
    /\ cacheErrored \subseteq Clusters
    /\ cacheClusters \cap cacheErrored = {}
    /\ publishedThisStream \in BOOLEAN

ServedSnapshotReferencesResolved ==
    MissingReferencedClusters(refs, cacheClusters, cacheErrored) = {}

PublishedSnapshotIsCurrentWhenPublished ==
    publishedThisStream =>
        /\ cacheClusters = ready
        /\ cacheErrored = errored

=============================================================================
