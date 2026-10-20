------------------------ MODULE BackendResourceCarry ------------------------
EXTENDS Naturals

\* One previously valid tunnel: cluster owns listener, listener calls proxy and
\* reads a secret. A candidate may be incomplete; closure concerns the published
\* result. This models resource selection, not queueing, ACK or application.
CONSTANTS Scoped, CarryClosure
VARIABLE state
vars == <<state>>
InitialResources == [cluster |-> TRUE, listener |-> TRUE,
                     proxy |-> TRUE, secret |-> TRUE, revoked |-> FALSE]
Init == state = [phase |-> "initial", candidate |-> InitialResources,
                 published |-> InitialResources]

LoseTranslation ==
    /\ state.phase = "initial"
    /\ state' = [state EXCEPT !.phase = "candidate",
        !.candidate = [cluster |-> FALSE, listener |-> FALSE,
                       proxy |-> ~Scoped, secret |-> FALSE, revoked |-> FALSE]]
Revoke ==
    /\ state.phase = "initial"
    /\ state' = [state EXCEPT !.phase = "candidate",
        !.candidate = [cluster |-> FALSE, listener |-> FALSE,
                       proxy |-> ~Scoped, secret |-> FALSE, revoked |-> TRUE]]
PublishCarry ==
    /\ state.phase = "candidate"
    /\ LET c == state.candidate
           carry == ~c.revoked
           close == carry /\ CarryClosure
       IN state' = [state EXCEPT !.phase = "published",
           !.published = [cluster |-> carry,
                          listener |-> c.listener \/ close,
                          proxy |-> c.proxy \/ close,
                          secret |-> c.secret \/ close,
                          revoked |-> c.revoked]]
Next == LoseTranslation \/ Revoke \/ PublishCarry
Spec == Init /\ [][Next]_vars
TypeOK == /\ state.phase \in {"initial", "candidate", "published"}
          /\ state.candidate \in [cluster : BOOLEAN, listener : BOOLEAN,
                                 proxy : BOOLEAN, secret : BOOLEAN, revoked : BOOLEAN]
          /\ state.published \in [cluster : BOOLEAN, listener : BOOLEAN,
                                 proxy : BOOLEAN, secret : BOOLEAN, revoked : BOOLEAN]
PublishedDependencyClosure == state.published.cluster =>
    state.published.listener /\ state.published.proxy /\ state.published.secret
NoRevokedCarry == state.published.revoked => ~state.published.cluster
=============================================================================
