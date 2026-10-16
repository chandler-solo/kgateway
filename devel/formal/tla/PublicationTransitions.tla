------------------------- MODULE PublicationTransitions -------------------------
EXTENDS Naturals

\* Cache-boundary abstraction of stack 14817's overlapping publications.
\* No watches, delivery, ACKs or Envoy application are represented here.
\* Finite scope: one key, three revisions, one departing and one arriving cluster.
\* Logical time 1 = ahead expiry, 2 = removal expiry, 3 = unready-flip expiry.
\* Fixed is a proposed combined policy, not a proved refinement of Go.
CONSTANT Mode
VARIABLES candidate, installed, newestInstalled, pending, clock,
          ahead, underived, grace
vars == <<candidate, installed, newestInstalled, pending, clock,
          ahead, underived, grace>>

Init == /\ candidate = 1 /\ installed = 1 /\ newestInstalled = 1
        /\ pending = 0 /\ clock = 0
        /\ ahead = FALSE /\ underived = FALSE /\ grace = FALSE

Introduce(ready) ==
    /\ candidate = 1 /\ clock = 0
    /\ candidate' = 2 /\ pending' = 2
    /\ ahead' = ready /\ underived' = ~ready /\ grace' = TRUE
    /\ UNCHANGED <<installed, newestInstalled, clock>>

Update ==
    /\ candidate = 2 /\ clock = 0
    /\ candidate' = 3
    /\ IF ahead /\ Mode \in {"StaleTimer", "CacheMeansDelivered"}
          THEN /\ installed' = 3 /\ newestInstalled' = 3
               /\ pending' = IF Mode = "StaleTimer" THEN pending ELSE 0
          ELSE /\ pending' = 3
               /\ UNCHANGED <<installed, newestInstalled>>
    /\ UNCHANGED <<clock, ahead, underived, grace>>

Tick == /\ clock < 3 /\ clock' = clock + 1
        /\ UNCHANGED <<candidate, installed, newestInstalled, pending,
                       ahead, underived, grace>>

Deadline == IF underived THEN 3 ELSE 1
Release ==
    /\ pending # 0 /\ clock >= Deadline
    /\ installed' = pending
    /\ newestInstalled' = IF pending > newestInstalled THEN pending ELSE newestInstalled
    /\ pending' = 0 /\ ahead' = FALSE /\ underived' = FALSE
    /\ UNCHANGED <<candidate, clock, grace>>

Prune ==
    /\ grace /\ clock >= 2
    /\ grace' = FALSE
    /\ IF Mode = "PruneReleasesHold" /\ underived /\ pending # 0 /\ clock < Deadline
          THEN /\ installed' = candidate /\ newestInstalled' = candidate
          ELSE UNCHANGED <<installed, newestInstalled>>
    /\ UNCHANGED <<candidate, pending, clock, ahead, underived>>

Next == Introduce(TRUE) \/ Introduce(FALSE) \/ Update \/ Tick \/ Release \/ Prune
Spec == Init /\ [][Next]_vars /\ WF_vars(Tick) /\ WF_vars(Release)

TypeOK == /\ candidate \in 1..3 /\ installed \in 1..3 /\ newestInstalled \in 1..3
          /\ pending \in 0..3 /\ clock \in 0..3
          /\ ahead \in BOOLEAN /\ underived \in BOOLEAN /\ grace \in BOOLEAN
NoPublicationRollback == installed >= newestInstalled
ReferenceAheadIntervalRespected == (ahead /\ clock < 1) => installed = 1
IndependentHoldNotReleasedByPrune == (underived /\ clock < 3) => installed = 1
LatestEventuallyInstalled == (candidate = 3) ~> (installed = 3)
=============================================================================
