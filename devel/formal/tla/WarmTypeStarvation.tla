------------------------- MODULE WarmTypeStarvation -------------------------
EXTENDS Naturals

\* RF-002: after a warm publication, a new route targets a permanently empty
\* backend. An independent route/secret change is pending in every candidate.
\* Isolation is a proposed policy, not a refinement of current Go code.
CONSTANT IsolateIndependent
VARIABLES cdsRevision, independentApplied, blockedFlipApplied
vars == <<cdsRevision, independentApplied, blockedFlipApplied>>

Init == /\ cdsRevision = 0
        /\ independentApplied = FALSE
        /\ blockedFlipApplied = FALSE

\* The two revisions abstract arbitrarily many rebuilds; no content identity
\* or hashing guarantee is inferred from this finite toggle.
Publish == /\ cdsRevision' = 1 - cdsRevision
           /\ independentApplied' = (independentApplied \/ IsolateIndependent)
           /\ UNCHANGED blockedFlipApplied

Spec == Init /\ [][Publish]_vars /\ WF_vars(Publish)
TypeOK == /\ cdsRevision \in {0, 1}
          /\ independentApplied \in BOOLEAN
          /\ blockedFlipApplied \in BOOLEAN
BlockedFlipHeld == ~blockedFlipApplied
IndependentEventuallyApplied == <>independentApplied
=============================================================================
