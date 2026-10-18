----------------------------- MODULE CarryEpisodes -----------------------------
EXTENDS Naturals

\* Cache-boundary abstraction of 7d930bb2183b's carry bookkeeping. One cluster,
\* two disappearances separated by a coherent recovery, and bounded time.
\* Current preserves carriedSince across the coherent path, which cancels its
\* timer without calling boundCarryLocked. ResetOnRecovery is a proposed policy.
\* No watch, wire, ACK or application semantics, and no Go refinement theorem.
CONSTANT Mode
Bound == 2
MaxTime == 6
None == MaxTime + 1
VARIABLES now, phase, episode, episodeStart, carriedSince, retained
vars == <<now, phase, episode, episodeStart, carriedSince, retained>>

Init == /\ now = 0 /\ phase = 0 /\ episode = 0
        /\ episodeStart = None /\ carriedSince = None /\ retained = TRUE

FirstDisappearance ==
    /\ phase = 0 /\ now <= 1
    /\ phase' = 1 /\ episode' = 1
    /\ episodeStart' = now /\ carriedSince' = now
    /\ UNCHANGED <<now, retained>>

CoherentRecovery ==
    /\ phase = 1 /\ now < carriedSince + Bound
    /\ phase' = 2 /\ retained' = TRUE
    /\ carriedSince' = IF Mode = "Current" THEN carriedSince ELSE None
    /\ UNCHANGED <<now, episode, episodeStart>>

SecondDisappearance ==
    /\ phase = 2 /\ now <= MaxTime - Bound
    /\ episode' = 2 /\ episodeStart' = now
    /\ carriedSince' = IF carriedSince = None THEN now ELSE carriedSince
    /\ retained' = (carriedSince = None \/ now < carriedSince + Bound)
    /\ phase' = IF retained' THEN 3 ELSE 4
    /\ UNCHANGED now

Expire ==
    /\ phase \in {1, 3} /\ now >= carriedSince + Bound
    /\ phase' = 4 /\ retained' = FALSE
    /\ UNCHANGED <<now, episode, episodeStart, carriedSince>>

Tick == /\ now < MaxTime /\ now' = now + 1
        /\ UNCHANGED <<phase, episode, episodeStart, carriedSince, retained>>
Next == FirstDisappearance \/ CoherentRecovery \/ SecondDisappearance \/ Expire \/ Tick
Spec == Init /\ [][Next]_vars /\ WF_vars(Tick) /\ WF_vars(Expire)
TypeOK == /\ now \in 0..MaxTime /\ phase \in 0..4 /\ episode \in 0..2
          /\ episodeStart \in 0..None /\ carriedSince \in 0..None
          /\ retained \in BOOLEAN
FreshEpisodeGetsFullCarryInterval ==
    (episode = 2 /\ ~retained) => now >= episodeStart + Bound
QuietCarryEventuallyExpires == (phase = 3) ~> (phase = 4)
=============================================================================
