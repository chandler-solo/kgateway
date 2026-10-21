--------------------------- MODULE WatchTraceReplay ---------------------------
EXTENDS NackWatchLifecycle, Sequences
CONSTANTS Trace, OracleStates
VARIABLE cursor
replayVars == <<vars, cursor>>
Action(kind) == CASE kind = "initial-read" -> InitialRead
                 [] kind = "initial-park" -> InitialPark
                 [] kind = "publish-begin" -> BeginPublish
                 [] kind = "install" -> Install
                 [] kind = "fanout" -> Fanout
                 [] kind = "expire" -> Expire
                 [] kind = "retry-begin" -> BeginRetry
                 [] kind = "retry-read" -> ReadRetry
                 [] kind = "retry-cancel" -> CancelOld
                 [] kind = "retry-replace" -> Replace
                 [] kind = "forward" -> Forward
                 [] kind = "send" -> Send
                 [] OTHER -> FALSE
ReplayInit == Init /\ cursor = 0
ReplayNext == /\ cursor < Len(Trace) /\ Action(Trace[cursor+1]) /\ cursor' = cursor + 1
ReplaySpec == ReplayInit /\ [][ReplayNext]_replayVars /\ WF_replayVars(ReplayNext)
Projection == [version |-> version, extra |-> extra, phase |-> phase,
               captured |-> captured, lock |-> lock, pubPC |-> pubPC,
               watch |-> watch, timer |-> timer, own |-> own,
               queued |-> queued, sent |-> sent]
OracleProjection == cursor = 0 \/ Projection = OracleStates[cursor]
CompleteReplay == <> (cursor = Len(Trace))
=============================================================================
