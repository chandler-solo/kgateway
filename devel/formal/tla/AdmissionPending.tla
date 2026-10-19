-------------------------- MODULE AdmissionPending --------------------------
EXTENDS Naturals

\* One client/key, two connections, three candidate rows and one pending timer.
\* Models the reset/adopt gap at 530c4f and atomic reset/adopt at 8e7546.
\* A new-build guard can accept row 1 before it advances: the stored timer then
\* bypasses that guard. Admission's selected row 2 is superseded by row 3 and
\* skipped. Pending origin is observation history, not a field in the Go entry.
\* Cache installation, transport send and remote application remain separate.
\* Atomic mode assumes the mutex excludes publishers across reset AND adoption;
\* the real live test separately attempts publication inside that interval.
\* No ACK/application equivalence, timer duration or general freshness theorem.
CONSTANT Mode
VARIABLE state
vars == <<state>>
Initial == [epoch |-> 1, adopted |-> 1, phase |-> "start", row |-> 1,
            queued |-> TRUE, pending |-> 0, origin |-> 0, selected |-> 0,
            cache |-> 0, cacheOrigin |-> 0, waiting |-> FALSE,
            response |-> 0, responseOrigin |-> 0, sent |-> 0,
            applied |-> 0, staleSent |-> FALSE, staleApplied |-> FALSE]
Init == state = Initial

Step(s, t, action) ==
    \/ /\ action = "Start" /\ s.phase = "start"
       /\ t = [s EXCEPT !.epoch = 2, !.phase = "reset", !.cache = 0]
    \/ /\ action = "Reset" /\ s.phase = "reset"
       /\ t = [s EXCEPT !.pending = 0, !.origin = 0,
                        !.adopted = IF Mode = "Atomic" THEN 2 ELSE s.adopted,
                        !.phase = IF Mode = "Atomic" THEN "replay" ELSE "adopt"]
    \/ /\ action = "BeginQueued" /\ s.phase \in {"adopt", "replay"}
       /\ s.queued /\ s.row = 1
       /\ t = [s EXCEPT !.queued = FALSE, !.pending = 1, !.origin = s.adopted]
    \/ /\ action = "AdvanceRow" /\ ~s.queued /\ s.row = 1
       /\ t = [s EXCEPT !.row = 2]
    \/ /\ action = "Adopt" /\ s.phase = "adopt"
       /\ t = [s EXCEPT !.adopted = 2, !.phase = "replay"]
    \/ /\ action = "SelectReplay" /\ s.phase = "replay" /\ s.row = 2
       /\ t = [s EXCEPT !.selected = 2, !.phase = "validate"]
    \/ /\ action = "SupersedeReplay" /\ s.phase = "validate" /\ s.row = 2
       /\ t = [s EXCEPT !.row = 3]
    \/ /\ action = "Replay" /\ s.phase = "validate" /\ s.row = 3
       \* beginBuildLocked skips selected=2, leaving the pending entry alone.
       /\ t = [s EXCEPT !.phase = "watch"]
    \/ /\ action = "Watch" /\ s.phase = "watch"
       /\ t = [s EXCEPT !.waiting = TRUE, !.phase = "budget"]
    \/ /\ action = "Expire" /\ s.phase = "budget" /\ s.pending # 0
       /\ LET mayInstall == IF Mode = "OriginFence" THEN s.origin = s.epoch
                            ELSE s.adopted = s.epoch
          IN t = [s EXCEPT !.pending = 0, !.phase = "expired",
                           !.cache = IF mayInstall THEN s.pending ELSE s.cache,
                           !.cacheOrigin = IF mayInstall THEN s.origin ELSE s.cacheOrigin,
                           !.response = IF mayInstall /\ s.waiting THEN s.pending ELSE s.response,
                           !.responseOrigin = IF mayInstall /\ s.waiting THEN s.origin ELSE s.responseOrigin,
                           !.waiting = IF mayInstall THEN FALSE ELSE s.waiting]
    \/ /\ action = "Send" /\ s.response # 0
       /\ t = [s EXCEPT !.sent = s.response, !.response = 0,
                        !.staleSent = s.staleSent \/ (s.responseOrigin < s.epoch)]
    \/ /\ action = "Apply" /\ s.sent # 0 /\ s.applied # s.sent
       /\ t = [s EXCEPT !.applied = s.sent,
                        !.staleApplied = s.staleApplied \/ s.staleSent]

Actions == {"Start", "Reset", "BeginQueued", "AdvanceRow", "Adopt",
            "SelectReplay", "SupersedeReplay", "Replay", "Watch", "Expire", "Send", "Apply"}
Next == \E action \in Actions : Step(state, state', action)
Spec == Init /\ [][Next]_vars
TypeOK == /\ state.epoch \in 1..2 /\ state.adopted \in 1..2
          /\ state.row \in 1..3 /\ state.pending \in 0..1 /\ state.origin \in 0..2
          /\ state.selected \in 0..2 /\ state.cache \in 0..1 /\ state.cacheOrigin \in 0..2
          /\ state.response \in 0..1 /\ state.responseOrigin \in 0..2
          /\ state.sent \in 0..1 /\ state.applied \in 0..1
          /\ state.waiting \in BOOLEAN /\ state.queued \in BOOLEAN
          /\ state.staleSent \in BOOLEAN /\ state.staleApplied \in BOOLEAN
NoEarlierPendingResponse == ~state.staleSent
NoEarlierPendingApplication == ~state.staleApplied
=============================================================================
