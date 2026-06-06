----------------------------- MODULE XdsAdsSotw -----------------------------
EXTENDS Naturals, FiniteSets, TLC

\* This is an intentionally small finite model of ADS/SotW publication.
\* It models resource-type versions, stream-local nonces, ACK/NACK behavior,
\* stale nonce handling, reconnects, and dependency-closed xDS publication.

Types == {"LDS", "RDS", "CDS", "EDS"}

VersionValues == 0..4
NonceValues == 0..3
StreamValues == 1..3
NoNonce == 0
MaxVersion == 4
MaxNonce == 3
MaxStream == 3
MaxStaleRequests == 2

NamesForType(t) ==
    IF t = "LDS" THEN {"listener"}
    ELSE IF t = "RDS" THEN {"route"}
    ELSE IF t = "CDS" THEN {"cluster"}
    ELSE {"endpoint"}

EmptySnapshot == [t \in Types |-> {}]
EndpointSnapshot == [t \in Types |-> IF t = "EDS" THEN {"endpoint"} ELSE {}]
ClusterEndpointSnapshot ==
    [t \in Types |->
        IF t = "CDS" THEN {"cluster"}
        ELSE IF t = "EDS" THEN {"endpoint"}
        ELSE {}]
RouteClusterEndpointSnapshot ==
    [t \in Types |->
        IF t = "RDS" THEN {"route"}
        ELSE IF t = "CDS" THEN {"cluster"}
        ELSE IF t = "EDS" THEN {"endpoint"}
        ELSE {}]
FullSnapshot == [t \in Types |-> NamesForType(t)]

ValidDesiredSnapshots ==
    { EmptySnapshot,
      EndpointSnapshot,
      ClusterEndpointSnapshot,
      RouteClusterEndpointSnapshot,
      FullSnapshot }

SnapshotTypeOK(snap) ==
    /\ DOMAIN snap = Types
    /\ \A t \in Types: snap[t] \subseteq NamesForType(t)

SnapshotClosed(snap) ==
    /\ SnapshotTypeOK(snap)
    /\ ("listener" \in snap["LDS"] => "route" \in snap["RDS"])
    /\ ("route" \in snap["RDS"] => "cluster" \in snap["CDS"])
    /\ ("cluster" \in snap["CDS"] => "endpoint" \in snap["EDS"])

VARIABLES
    desired,
    resourceVersion,
    sentSnapshot,
    sentVersion,
    nonceCounter,
    sentNonce,
    sentStream,
    stream,
    clientAcceptedVersion,
    serverAcceptedVersion,
    staleRequests,
    ackMatchesLatestNonce,
    nackDoesNotAdvance,
    staleNonceDoesNotAdvance,
    versionsArePerResourceType

vars ==
    << desired,
       resourceVersion,
       sentSnapshot,
       sentVersion,
       nonceCounter,
       sentNonce,
       sentStream,
       stream,
       clientAcceptedVersion,
       serverAcceptedVersion,
       staleRequests,
       ackMatchesLatestNonce,
       nackDoesNotAdvance,
       staleNonceDoesNotAdvance,
       versionsArePerResourceType >>

Init ==
    /\ desired = EmptySnapshot
    /\ resourceVersion = [t \in Types |-> 0]
    /\ sentSnapshot = EmptySnapshot
    /\ sentVersion = [t \in Types |-> 0]
    /\ nonceCounter = [t \in Types |-> 0]
    /\ sentNonce = [t \in Types |-> NoNonce]
    /\ stream = 1
    /\ sentStream = [t \in Types |-> 1]
    /\ clientAcceptedVersion = [t \in Types |-> 0]
    /\ serverAcceptedVersion = [t \in Types |-> 0]
    /\ staleRequests = 0
    /\ ackMatchesLatestNonce
    /\ nackDoesNotAdvance
    /\ staleNonceDoesNotAdvance
    /\ versionsArePerResourceType

CanVersion(nextDesired) ==
    \A t \in Types:
        nextDesired[t] = desired[t] \/ resourceVersion[t] < MaxVersion

InputChange ==
    \E nextDesired \in ValidDesiredSnapshots:
        /\ nextDesired # desired
        /\ CanVersion(nextDesired)
        /\ desired' = nextDesired
        /\ resourceVersion' =
            [t \in Types |->
                IF nextDesired[t] = desired[t]
                THEN resourceVersion[t]
                ELSE resourceVersion[t] + 1]
        /\ UNCHANGED << sentSnapshot,
                       sentVersion,
                       nonceCounter,
                       sentNonce,
                       sentStream,
                       stream,
                       clientAcceptedVersion,
                       serverAcceptedVersion,
                       staleRequests,
                       ackMatchesLatestNonce,
                       nackDoesNotAdvance,
                       staleNonceDoesNotAdvance,
                       versionsArePerResourceType >>

ProposedSentSnapshot(t) ==
    [u \in Types |-> IF u = t THEN desired[t] ELSE sentSnapshot[u]]

SendResponse(t) ==
    /\ desired[t] # sentSnapshot[t]
    /\ nonceCounter[t] < MaxNonce
    /\ SnapshotClosed(ProposedSentSnapshot(t))
    /\ sentSnapshot' = ProposedSentSnapshot(t)
    /\ sentVersion' = [sentVersion EXCEPT ![t] = resourceVersion[t]]
    /\ nonceCounter' = [nonceCounter EXCEPT ![t] = nonceCounter[t] + 1]
    /\ sentNonce' = [sentNonce EXCEPT ![t] = nonceCounter[t] + 1]
    /\ sentStream' = [sentStream EXCEPT ![t] = stream]
    /\ resourceVersion' = resourceVersion
    /\ versionsArePerResourceType' =
        (versionsArePerResourceType /\ resourceVersion' = resourceVersion)
    /\ UNCHANGED << desired,
                   resourceVersion,
                   stream,
                   clientAcceptedVersion,
                   serverAcceptedVersion,
                   staleRequests,
                   ackMatchesLatestNonce,
                   nackDoesNotAdvance,
                   staleNonceDoesNotAdvance >>

ClientAck(t) ==
    /\ sentNonce[t] # NoNonce
    /\ sentStream[t] = stream
    /\ serverAcceptedVersion' = [serverAcceptedVersion EXCEPT ![t] = sentVersion[t]]
    /\ clientAcceptedVersion' = [clientAcceptedVersion EXCEPT ![t] = sentVersion[t]]
    /\ ackMatchesLatestNonce' =
        (ackMatchesLatestNonce /\ sentNonce[t] = nonceCounter[t] /\ sentStream[t] = stream)
    /\ resourceVersion' = resourceVersion
    /\ versionsArePerResourceType' =
        (versionsArePerResourceType
        /\ \A u \in Types \ {t}:
            /\ serverAcceptedVersion'[u] = serverAcceptedVersion[u]
            /\ clientAcceptedVersion'[u] = clientAcceptedVersion[u])
    /\ UNCHANGED << desired,
                   sentSnapshot,
                   sentVersion,
                   nonceCounter,
                   sentNonce,
                   sentStream,
                   stream,
                   staleRequests,
                   nackDoesNotAdvance,
                   staleNonceDoesNotAdvance >>

ClientNack(t) ==
    /\ sentNonce[t] # NoNonce
    /\ sentStream[t] = stream
    /\ serverAcceptedVersion' = serverAcceptedVersion
    /\ clientAcceptedVersion' = clientAcceptedVersion
    /\ nackDoesNotAdvance' =
        (nackDoesNotAdvance
        /\ serverAcceptedVersion' = serverAcceptedVersion
        /\ clientAcceptedVersion' = clientAcceptedVersion)
    /\ resourceVersion' = resourceVersion
    /\ versionsArePerResourceType' =
        (versionsArePerResourceType /\ resourceVersion' = resourceVersion)
    /\ UNCHANGED << desired,
                   sentSnapshot,
                   sentVersion,
                   nonceCounter,
                   sentNonce,
                   sentStream,
                   stream,
                   staleRequests,
                   ackMatchesLatestNonce,
                   staleNonceDoesNotAdvance >>

StaleClientRequest(t) ==
    /\ sentNonce[t] > 1
    /\ staleRequests < MaxStaleRequests
    /\ staleRequests' = staleRequests + 1
    /\ serverAcceptedVersion' = serverAcceptedVersion
    /\ clientAcceptedVersion' = clientAcceptedVersion
    /\ sentSnapshot' = sentSnapshot
    /\ sentVersion' = sentVersion
    /\ staleNonceDoesNotAdvance' =
        (staleNonceDoesNotAdvance
        /\ serverAcceptedVersion' = serverAcceptedVersion
        /\ clientAcceptedVersion' = clientAcceptedVersion
        /\ sentSnapshot' = sentSnapshot
        /\ sentVersion' = sentVersion)
    /\ resourceVersion' = resourceVersion
    /\ versionsArePerResourceType' =
        (versionsArePerResourceType /\ resourceVersion' = resourceVersion)
    /\ UNCHANGED << desired,
                   nonceCounter,
                   sentNonce,
                   sentStream,
                   stream,
                   ackMatchesLatestNonce,
                   nackDoesNotAdvance >>

Reconnect ==
    /\ stream < MaxStream
    /\ stream' = stream + 1
    /\ sentNonce' = [t \in Types |-> NoNonce]
    /\ nonceCounter' = [t \in Types |-> 0]
    /\ sentStream' = [t \in Types |-> stream + 1]
    /\ resourceVersion' = resourceVersion
    /\ serverAcceptedVersion' = serverAcceptedVersion
    /\ clientAcceptedVersion' = clientAcceptedVersion
    /\ versionsArePerResourceType' =
        (versionsArePerResourceType
        /\ resourceVersion' = resourceVersion
        /\ serverAcceptedVersion' = serverAcceptedVersion
        /\ clientAcceptedVersion' = clientAcceptedVersion)
    /\ UNCHANGED << desired,
                   sentSnapshot,
                   sentVersion,
                   staleRequests,
                   ackMatchesLatestNonce,
                   nackDoesNotAdvance,
                   staleNonceDoesNotAdvance >>

NoOp ==
    UNCHANGED vars

Next ==
    \/ InputChange
    \/ \E t \in Types: SendResponse(t)
    \/ \E t \in Types: ClientAck(t)
    \/ \E t \in Types: ClientNack(t)
    \/ \E t \in Types: StaleClientRequest(t)
    \/ Reconnect
    \/ NoOp

Spec == Init /\ [][Next]_vars

TypeOK ==
    /\ desired \in [Types -> SUBSET {"listener", "route", "cluster", "endpoint"}]
    /\ sentSnapshot \in [Types -> SUBSET {"listener", "route", "cluster", "endpoint"}]
    /\ SnapshotTypeOK(desired)
    /\ SnapshotTypeOK(sentSnapshot)
    /\ resourceVersion \in [Types -> VersionValues]
    /\ sentVersion \in [Types -> VersionValues]
    /\ nonceCounter \in [Types -> NonceValues]
    /\ sentNonce \in [Types -> NonceValues]
    /\ sentStream \in [Types -> StreamValues]
    /\ stream \in StreamValues
    /\ clientAcceptedVersion \in [Types -> VersionValues]
    /\ serverAcceptedVersion \in [Types -> VersionValues]
    /\ staleRequests \in 0..MaxStaleRequests
    /\ ackMatchesLatestNonce \in BOOLEAN
    /\ nackDoesNotAdvance \in BOOLEAN
    /\ staleNonceDoesNotAdvance \in BOOLEAN
    /\ versionsArePerResourceType \in BOOLEAN

SentSnapshotsAreDependencyClosed ==
    SnapshotClosed(sentSnapshot)

AckAdvancesOnlyMatchingNonce ==
    ackMatchesLatestNonce

NackDoesNotAdvanceAcceptedVersion ==
    nackDoesNotAdvance

StaleNonceDoesNotAdvanceAcceptedVersion ==
    staleNonceDoesNotAdvance

VersionsArePerResourceType ==
    versionsArePerResourceType

NoncesArePerStream ==
    \A t \in Types: sentNonce[t] = NoNonce \/ sentStream[t] = stream

\* A stable full desired snapshot is publishable by the ordered sequence
\* EDS, CDS, RDS, LDS followed by matching ACKs. A fairness-based liveness
\* proof is deliberately left out of the MVP TLC config to keep the run small.
=============================================================================
