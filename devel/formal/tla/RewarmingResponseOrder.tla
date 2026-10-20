----------------------- MODULE RewarmingResponseOrder -----------------------
EXTENDS Naturals

\* One stable same-name EDS cluster on one established stream. The assignment
\* is present and subscription-compatible. Its new version is received before
\* changed CDS, so another version bump cannot answer the warming subscription.
\* Repair models the October 20 per-stream replay mark, not a refinement of Go.
\* Optional fallback represents expiry using cached EDS; no clock bound is proved.
CONSTANTS Repair, ClearAtRequest, TimeoutFallback
VARIABLE state
vars == <<state>>
Init == state = [early |-> FALSE, cds |-> FALSE, marked |-> FALSE,
                 requested |-> FALSE, queued |-> FALSE, sent |-> FALSE,
                 received |-> FALSE, acked |-> FALSE, active |-> FALSE,
                 fallback |-> FALSE, replays |-> 0]

Step(s, t, action) ==
    \/ /\ action = "ReceiveEarlyEDS" /\ ~s.early
       /\ t = [s EXCEPT !.early = TRUE]
    \/ /\ action = "SendChangedCDS" /\ s.early /\ ~s.cds
       /\ t = [s EXCEPT !.cds = TRUE, !.marked = Repair]
    \/ /\ action = "RequestHeldEDS" /\ s.cds /\ ~s.requested
       /\ t = [s EXCEPT !.requested = TRUE,
                        !.marked = IF ClearAtRequest THEN FALSE ELSE s.marked]
    \/ /\ action = "QueueReplay" /\ s.requested /\ s.marked
       /\ ~s.queued /\ ~s.sent /\ ~s.active
       /\ t = [s EXCEPT !.queued = TRUE]
    \/ /\ action = "SendReplay" /\ s.queued /\ ~s.sent /\ ~s.active
       /\ t = [s EXCEPT !.queued = FALSE, !.sent = TRUE,
                        !.marked = FALSE, !.replays = @ + 1]
    \/ /\ action = "ReceiveReplay" /\ s.sent /\ ~s.received
       /\ t = [s EXCEPT !.received = TRUE]
    \/ /\ action = "AckReplay" /\ s.received /\ ~s.acked
       /\ t = [s EXCEPT !.acked = TRUE]
    \/ /\ action = "Apply" /\ s.received /\ ~s.active
       /\ t = [s EXCEPT !.active = TRUE]
    \/ /\ action = "Expire" /\ TimeoutFallback /\ s.requested
       /\ ~s.active /\ ~s.received
       /\ t = [s EXCEPT !.active = TRUE, !.fallback = TRUE]
    \/ /\ action = "RepublishSameVersion" /\ t = s

Actions == {"ReceiveEarlyEDS", "SendChangedCDS", "RequestHeldEDS", "QueueReplay",
            "SendReplay", "ReceiveReplay", "AckReplay", "Apply", "Expire",
            "RepublishSameVersion"}
Next == \E action \in Actions : Step(state, state', action)
Spec == /\ Init /\ [][Next]_vars
        /\ \A action \in Actions : WF_vars(Step(state, state', action))

TypeOK == /\ state.early \in BOOLEAN /\ state.cds \in BOOLEAN
          /\ state.marked \in BOOLEAN /\ state.requested \in BOOLEAN
          /\ state.queued \in BOOLEAN /\ state.sent \in BOOLEAN
          /\ state.received \in BOOLEAN /\ state.acked \in BOOLEAN
          /\ state.active \in BOOLEAN /\ state.fallback \in BOOLEAN
          /\ state.replays \in 0..1
NoFabricatedReceive == state.received => state.sent
ReplayFollowsCDS == state.sent => state.cds /\ state.early
NoApplyWithoutReceiveOrFallback == state.active => state.received \/ state.fallback
OneReplay == state.replays <= 1
EventuallyActive == <>state.active
=============================================================================
