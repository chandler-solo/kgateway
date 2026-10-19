------------------------ MODULE InitialFetchTimeouts ------------------------
EXTENDS Naturals, Integers, FiniteSets, TLC

\* One bootstrap and one dynamic CDS/EDS/LDS/RDS chain. Durations are logical
\* seconds. A remaining-time counter models each subscription independently;
\* elapsed time saturates only for the deliberately false global-bound claim.
\* Tick fairness is an abstract time-progress assumption, not a dispatcher bound.
\* Old models and their stronger/historical policy assumptions remain separate.
CONSTANTS Profile, Omit, CachedEDS, CachedRDS, CacheKeyMatches, Fault
Types == {"local", "cds", "eds", "lds", "rds"}
Phases == Types \cup {"done"}
Duration(t) == IF t = "local" THEN IF Profile = "local-disabled" THEN 0 ELSE 1
               ELSE IF t \in {"cds", "lds"} THEN 0
               ELSE IF Profile = "disabled" THEN 0
               ELSE IF Profile = "short" THEN 2 ELSE 15
Reasons == {"none", "fresh", "rejected", "timeout-empty", "cached"}
VARIABLE s
vars == <<s>>
Initial == [phase |-> "local", elapsed |-> 0,
            started |-> [t \in Types |-> FALSE],
            armed |-> [t \in Types |-> FALSE],
            left |-> [t \in Types |-> 0],
            initialized |-> [t \in Types |-> FALSE],
            reason |-> [t \in Types |-> "none"],
            received |-> [t \in Types |-> "none"],
            accepted |-> [t \in Types |-> "none"],
            wire |-> [t \in Types |-> 0], actualWire |-> [t \in Types |-> 0],
            generation |-> 1, replaced |-> FALSE,
            oldPending |-> FALSE, oldLeft |-> 0,
            staleEffect |-> FALSE, earlyEffect |-> FALSE,
            disabledEffect |-> FALSE, fabricated |-> FALSE,
            cacheWrong |-> FALSE, rejected |-> FALSE,
            connectionFailed |-> FALSE, ready |-> FALSE]
Init == s = Initial

Start == /\ s.phase \in Types /\ ~s.started[s.phase]
         /\ s' = [s EXCEPT !.started[s.phase] = TRUE,
                           !.armed[s.phase] = Duration(s.phase) > 0,
                           !.left[s.phase] = Duration(s.phase)]
Tick == s' = [s EXCEPT !.elapsed = IF @ < 35 THEN @ + 1 ELSE @,
                      !.left = [t \in Types |-> IF s.armed[t] /\ s.left[t] > 0
                                                THEN s.left[t] - 1 ELSE s.left[t]],
                      !.oldLeft = IF @ > 0 THEN @ - 1 ELSE @]
Receive == /\ s.phase \in Types /\ s.started[s.phase]
           /\ s.phase \notin Omit /\ s.received[s.phase] = "none"
           /\ \E payload \in (IF s.phase \in {"local", "eds"} THEN {"empty", "data"} ELSE {"data"}) :
               s' = [s EXCEPT !.received[s.phase] = payload,
                              !.wire[s.phase] = @ + 1, !.actualWire[s.phase] = @ + 1]
Accept == /\ s.phase \in Types /\ s.received[s.phase] # "none"
          /\ ~s.initialized[s.phase]
          /\ s' = [s EXCEPT !.accepted[s.phase] = s.received[s.phase],
                            !.initialized[s.phase] = TRUE,
                            !.reason[s.phase] = "fresh", !.armed[s.phase] = FALSE]
CacheAllowed(t) == (t = "eds" /\ CachedEDS /\ CacheKeyMatches)
                  \/ (t = "rds" /\ CachedRDS)
Expire == /\ s.phase \in Types /\ s.started[s.phase]
          /\ ~s.initialized[s.phase]
          /\ (s.armed[s.phase] \/ (Fault = "zero" /\ Duration(s.phase) = 0))
          /\ (s.left[s.phase] = 0 \/ Fault = "early")
          /\ LET cached == CacheAllowed(s.phase)
                           \/ (Fault = "cache" /\ s.phase = "eds" /\ CachedEDS)
             IN s' = [s EXCEPT !.initialized[s.phase] = TRUE,
                       !.armed[s.phase] = FALSE,
                       !.reason[s.phase] = IF cached THEN "cached" ELSE "timeout-empty",
                       !.earlyEffect = @ \/ s.left[s.phase] > 0,
                       !.disabledEffect = @ \/ Duration(s.phase) = 0,
                       !.fabricated = @ \/ Fault = "delivery",
                       !.wire[s.phase] = IF Fault = "delivery" THEN @ + 1 ELSE @,
                       !.cacheWrong = @ \/ (cached /\ s.phase = "eds" /\ ~CacheKeyMatches)]
\* Existing RDS providers can satisfy listener warming without a new response.
ReuseRDS == /\ s.phase = "rds" /\ s.started["rds"] /\ CachedRDS
            /\ ~s.initialized["rds"]
            /\ s' = [s EXCEPT !.initialized["rds"] = TRUE,
                              !.reason["rds"] = "cached", !.armed["rds"] = FALSE]
\* A transport failure does not take the rejected-update path in the pinned
\* gRPC subscription. This flag bounds the representative failure to one event.
ConnectionFailure == /\ s.phase \in Types /\ s.started[s.phase]
                     /\ ~s.connectionFailed
                     /\ s' = [s EXCEPT !.connectionFailed = TRUE]
Reject == /\ s.phase \in Types /\ s.started[s.phase]
          /\ ~s.initialized[s.phase] /\ ~s.rejected /\ s.received[s.phase] # "none"
          /\ s.phase \notin Omit
          /\ s' = [s EXCEPT !.initialized[s.phase] = TRUE,
                            !.reason[s.phase] = "rejected", !.armed[s.phase] = FALSE,
                            !.rejected = TRUE]
\* Replacing one EDS provider arms a new timer. A canceled old callback may
\* already be queued, but cannot initialize the new provider.
Replace == /\ s.phase = "eds" /\ s.started["eds"] /\ s.armed["eds"]
           /\ ~s.initialized["eds"] /\ ~s.replaced
           /\ s' = [s EXCEPT !.generation = @ + 1, !.replaced = TRUE,
                             !.oldPending = TRUE, !.oldLeft = s.left["eds"],
                             !.left["eds"] = Duration("eds"),
                             !.received["eds"] = "none"]
OldCallback == /\ s.oldPending /\ s.oldLeft = 0
               /\ s' = [s EXCEPT !.oldPending = FALSE,
                                 !.staleEffect = @ \/ (Fault = "stale" /\ s.generation > 1),
                                 !.initialized["eds"] = IF Fault = "stale" THEN TRUE ELSE @,
                                 !.armed["eds"] = IF Fault = "stale" THEN FALSE ELSE @,
                                 !.reason["eds"] = IF Fault = "stale" THEN "timeout-empty" ELSE @]
NextPhase == CASE s.phase = "local" -> "cds"
               [] s.phase = "cds" -> IF s.accepted["cds"] = "data" THEN "eds" ELSE "lds"
               [] s.phase = "eds" -> "lds"
               [] s.phase = "lds" -> IF s.accepted["lds"] = "data" THEN "rds" ELSE "done"
               [] OTHER -> "done"
Advance == /\ s.phase \in Types /\ s.initialized[s.phase]
           /\ s' = [s EXCEPT !.phase = NextPhase]
Activate == /\ s.phase = "done" /\ ~s.ready
            /\ s' = [s EXCEPT !.ready = TRUE]
Next == Start \/ Tick \/ Receive \/ Accept \/ Expire \/ ReuseRDS
        \/ ConnectionFailure \/ Reject \/ Replace \/ OldCallback \/ Advance \/ Activate
Spec == Init /\ [][Next]_vars /\ WF_vars(Start) /\ WF_vars(Tick)
        /\ WF_vars(Receive) /\ WF_vars(Accept) /\ WF_vars(Expire)
        /\ WF_vars(ReuseRDS) /\ WF_vars(OldCallback) /\ WF_vars(Advance) /\ WF_vars(Activate)

TypeOK == /\ s.phase \in Phases /\ s.elapsed \in 0..35
          /\ s.started \in [Types -> BOOLEAN] /\ s.armed \in [Types -> BOOLEAN]
          /\ s.left \in [Types -> 0..15] /\ s.oldLeft \in 0..15
          /\ s.initialized \in [Types -> BOOLEAN] /\ s.reason \in [Types -> Reasons]
          /\ s.received \in [Types -> {"none", "empty", "data"}]
          /\ s.accepted \in [Types -> {"none", "empty", "data"}]
          /\ s.wire \in [Types -> 0..3] /\ s.actualWire \in [Types -> 0..2] /\ s.generation \in 1..2
NoEarlyExpiry == ~s.earlyEffect
NoDisabledExpiry == ~s.disabledEffect
NoStaleTimerEffect == ~s.staleEffect
NoFabricatedDelivery == s.wire = s.actualWire
CacheIdentityRespected == ~s.cacheWrong
InitializedHasCause == \A t \in Types : s.initialized[t] => s.reason[t] # "none"
AcceptedNeedsWire == \A t \in Types : s.accepted[t] # "none" => s.wire[t] > 0
Safety == TypeOK /\ NoEarlyExpiry /\ NoDisabledExpiry /\ NoStaleTimerEffect
          /\ NoFabricatedDelivery /\ CacheIdentityRespected /\ InitializedHasCause /\ AcceptedNeedsWire
EventuallyInitialized == <>s.ready
\* Deliberately false claims. Required configurations retain their counterexamples.
ReadyMeansComplete == s.ready => \A t \in Types : s.accepted[t] # "none"
CacheMeansFresh == s.reason["eds"] = "cached" => s.accepted["eds"] # "none"
GlobalFifteenSecondBound == s.ready => s.elapsed <= 15
=============================================================================
