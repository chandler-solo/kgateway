--------------------------- MODULE QueuedPublication ---------------------------
EXTENDS Naturals

\* One KRT handler can hold an old event while GetKey already returns a newer,
\* deferred row. Admission replays that row before the queued handler resumes.
\* Current stamps at gate entry using connGen; CapturedOrigin rejects the old
\* job using a generation captured at enqueue. This is a bounded proposal, not
\* a proof that a generation token alone orders every same-generation revision.
\* Cache installation, watch response queueing and response delivery are separate.
\* ACK, application, Kubernetes/controller scheduling and multiple keys omitted.
CONSTANT Mode
VARIABLES epoch, adopted, candidate, queuedOld, enqueued, cache,
          hookDone, requested, waiting, response, delivered, staleServed
vars == <<epoch, adopted, candidate, queuedOld, enqueued, cache,
          hookDone, requested, waiting, response, delivered, staleServed>>
Init == /\ epoch = 1 /\ adopted = 1 /\ candidate = 1 /\ cache = 1
        /\ queuedOld = FALSE /\ enqueued = FALSE /\ hookDone = FALSE
        /\ requested = FALSE /\ waiting = FALSE /\ response = 0
        /\ delivered = 0 /\ staleServed = FALSE

Enqueue == /\ ~enqueued /\ enqueued' = TRUE /\ queuedOld' = TRUE
           /\ UNCHANGED <<epoch, adopted, candidate, cache, hookDone,
                           requested, waiting, response, delivered, staleServed>>
BuildCurrent == /\ enqueued /\ candidate = 1 /\ candidate' = 2
                /\ UNCHANGED <<epoch, adopted, queuedOld, enqueued, cache,
                                hookDone, requested, waiting, response, delivered, staleServed>>
Start == /\ epoch = 1 /\ candidate = 2 /\ epoch' = 2 /\ cache' = 0
         /\ UNCHANGED <<adopted, candidate, queuedOld, enqueued, hookDone,
                         requested, waiting, response, delivered, staleServed>>
Adopt == /\ epoch = 2 /\ adopted = 1 /\ adopted' = 2
         /\ UNCHANGED <<epoch, candidate, queuedOld, enqueued, cache, hookDone,
                         requested, waiting, response, delivered, staleServed>>
ReplayCurrent == /\ adopted = 2 /\ ~hookDone /\ hookDone' = TRUE
                 \* The current row is deferred; no first publication occurs.
                 /\ UNCHANGED <<epoch, adopted, candidate, queuedOld, enqueued, cache,
                                 requested, waiting, response, delivered, staleServed>>
PublishOld ==
    /\ queuedOld /\ queuedOld' = FALSE
    /\ LET origin == IF Mode = "Current" THEN adopted ELSE 1
       IN IF origin = epoch
          THEN /\ cache' = 1
               /\ response' = IF waiting THEN 1 ELSE response
               /\ waiting' = FALSE
          ELSE UNCHANGED <<cache, response, waiting>>
    /\ UNCHANGED <<epoch, adopted, candidate, enqueued, hookDone,
                    requested, delivered, staleServed>>
Watch ==
    /\ hookDone /\ ~requested /\ requested' = TRUE
    /\ response' = cache /\ waiting' = (cache = 0)
    /\ UNCHANGED <<epoch, adopted, candidate, queuedOld, enqueued, cache,
                    hookDone, delivered, staleServed>>
Derive == /\ hookDone /\ ~queuedOld /\ candidate = 2 /\ candidate' = 3
          /\ UNCHANGED <<epoch, adopted, queuedOld, enqueued, cache, hookDone,
                          requested, waiting, response, delivered, staleServed>>
PublishCurrent ==
    /\ candidate = 3 /\ cache # 3 /\ cache' = 3
    /\ response' = (IF waiting THEN 3 ELSE response)
    /\ waiting' = FALSE
    /\ UNCHANGED <<epoch, adopted, candidate, queuedOld, enqueued, hookDone,
                    requested, delivered, staleServed>>
Send == /\ response # 0 /\ delivered' = response /\ response' = 0
        /\ staleServed' = (staleServed \/ (response = 1 /\ epoch = 2))
        /\ UNCHANGED <<epoch, adopted, candidate, queuedOld, enqueued, cache,
                        hookDone, requested, waiting>>
Next == Enqueue \/ BuildCurrent \/ Start \/ Adopt \/ ReplayCurrent \/ PublishOld
        \/ Watch \/ Derive \/ PublishCurrent \/ Send
Spec == Init /\ [][Next]_vars /\ WF_vars(PublishOld) /\ WF_vars(Derive)
        /\ WF_vars(Watch) /\ WF_vars(PublishCurrent) /\ WF_vars(Send)
TypeOK == /\ epoch \in 1..2 /\ adopted \in 1..2 /\ candidate \in 1..3
          /\ cache \in 0..3 /\ response \in 0..3 /\ delivered \in 0..3
          /\ queuedOld \in BOOLEAN /\ enqueued \in BOOLEAN /\ hookDone \in BOOLEAN
          /\ requested \in BOOLEAN /\ waiting \in BOOLEAN /\ staleServed \in BOOLEAN
NoObsoleteCandidateResponse == ~staleServed
CoherentCandidateEventuallySent == (candidate = 3) ~> (delivered = 3)
=============================================================================
