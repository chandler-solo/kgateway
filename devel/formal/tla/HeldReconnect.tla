--------------------------- MODULE HeldReconnect ---------------------------
EXTENDS Naturals, Sequences
CONSTANT Mode
\* A's retained cache: 0=old, 2=latest underived candidate, 3=coherent recovery.
\* Active: 1=B's accepted route, 0=A's old route, 2/3=the new routes.
\* Sent: 0=none from A, 1=old A, 2/3=new A. ACK is deliberately absent:
\* application is independently observed and has its own transition.
Initial == [candidate |-> 0, cache |-> 0, generation |-> 1, held |-> FALSE,
            timer |-> 0, sent |-> 0, active |-> 1, rollback |-> FALSE,
            checked |-> FALSE, timerRollback |-> FALSE]
Actions == {"Offer", "Replace", "Reconnect", "SendOld", "ApplyOld",
            "Expire", "SendLatest", "ApplyLatest", "Recover", "StaleTimer"}

Step(s, t, action) ==
    CASE action = "Offer" ->
           /\ s.candidate = 0
           /\ t = [s EXCEPT !.candidate = 1, !.held = TRUE, !.timer = 1]
      [] action = "Replace" ->
           /\ s.candidate = 1 /\ s.held
           /\ t = [s EXCEPT !.candidate = 2]
      [] action = "Reconnect" ->
           /\ s.generation = 1 /\ s.held
           /\ t = [s EXCEPT !.generation = 2]
      [] action = "SendOld" ->
           /\ Mode = "OldCache" /\ s.generation = 2
           /\ s.held /\ s.sent = 0
           /\ t = [s EXCEPT !.sent = 1]
      [] action = "ApplyOld" ->
           /\ s.sent = 1 /\ s.held /\ s.active = 1
           /\ t = [s EXCEPT !.active = 0, !.rollback = TRUE]
      [] action = "Expire" ->
           /\ s.held /\ s.timer = 1 /\ s.generation = 2 /\ s.candidate = 2
           /\ t = [s EXCEPT !.cache = 2, !.held = FALSE, !.timer = 2]
      [] action = "SendLatest" ->
           /\ s.cache >= 2 /\ s.sent # s.cache
           /\ t = [s EXCEPT !.sent = s.cache]
      [] action = "ApplyLatest" ->
           /\ s.sent >= 2 /\ s.active # s.sent
           /\ t = [s EXCEPT !.active = s.sent]
      [] action = "Recover" ->
           /\ s.cache = 2 /\ ~s.checked
           /\ t = [s EXCEPT !.cache = 3, !.timer = 0]
      [] action = "StaleTimer" ->
           /\ s.cache = 3 /\ s.timer = 0 /\ ~s.checked
           /\ t = [s EXCEPT !.checked = TRUE,
                    !.cache = IF Mode = "StaleTimer" THEN 2 ELSE @,
                    !.timerRollback = (Mode = "StaleTimer")]
      [] OTHER -> FALSE

VARIABLE state
Init == state = Initial
Do(action) == Step(state, state', action)
Next == \E action \in Actions : Do(action)
\* All progress assumptions are external obligations, not properties of Go:
\* a second candidate arrives, a stable reconnect occurs, the expiry callback
\* runs, a coherent revision arrives, transport succeeds and Envoy applies it.
Spec == Init /\ [][Next]_state
        /\ WF_state(Do("Offer")) /\ WF_state(Do("Replace"))
        /\ WF_state(Do("Reconnect")) /\ WF_state(Do("Expire"))
        /\ WF_state(Do("SendLatest")) /\ WF_state(Do("ApplyLatest"))
        /\ WF_state(Do("Recover")) /\ WF_state(Do("StaleTimer"))
TypeOK == state \in [candidate : 0..2, cache : {0, 2, 3}, generation : 1..2,
                     held : BOOLEAN, timer : 0..2, sent : 0..3, active : 0..3,
                     rollback : BOOLEAN, checked : BOOLEAN, timerRollback : BOOLEAN]
NoHeldRollback == ~state.rollback
NoTimerRollback == ~state.timerRollback
EventuallyRecovered == <> (state.active = 3 /\ state.checked)
=============================================================================
