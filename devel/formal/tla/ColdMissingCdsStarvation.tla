---------------------- MODULE ColdMissingCdsStarvation ----------------------
EXTENDS Naturals

\* RF-003: a proxy with no cached snapshot has a candidate whose routes
\* reference a cluster absent from CDS and not recorded as errored. syncXds
\* withholds the whole first publication until the reference resolves.
\*
\* InputEventuallyCoherent: the environment eventually supplies the cluster
\*   (transient derivation lag). This is an assumption about input, not
\*   something the publication policy can enforce.
\* ClassifyPermanent: a proposed policy that, after some bound the model
\*   leaves abstract, records the unresolved reference as errored so the
\*   candidate publishes fail-closed for that route only. It is not
\*   implemented, and the model does not choose the bound.
CONSTANTS InputEventuallyCoherent, ClassifyPermanent
VARIABLES rebuild, published, resolved, classified
vars == <<rebuild, published, resolved, classified>>

Init == /\ rebuild = 0
        /\ published = FALSE
        /\ resolved = FALSE
        /\ classified = FALSE

\* Unrelated input keeps changing; each rebuild is a new deferred candidate.
\* The toggle abstracts arbitrarily many candidates, not version allocation.
Rebuild == /\ rebuild' = 1 - rebuild
           /\ UNCHANGED <<published, resolved, classified>>

\* The missing cluster arrives in CDS. Enabled only when the environment is
\* assumed coherent; a permanently missing reference never enables it.
Resolve == /\ InputEventuallyCoherent
           /\ ~resolved
           /\ resolved' = TRUE
           /\ UNCHANGED <<rebuild, published, classified>>

\* Proposed policy step: an unresolved reference is treated as errored.
Classify == /\ ClassifyPermanent
            /\ ~resolved
            /\ ~classified
            /\ classified' = TRUE
            /\ UNCHANGED <<rebuild, published, resolved>>

\* Current first-publication guard: every nonexempt referenced cluster is in
\* CDS. Classification makes the reference exempt.
Publish == /\ (resolved \/ classified)
           /\ ~published
           /\ published' = TRUE
           /\ UNCHANGED <<rebuild, resolved, classified>>

Next == Rebuild \/ Resolve \/ Classify \/ Publish
Spec == Init /\ [][Next]_vars /\ WF_vars(Rebuild) /\ WF_vars(Resolve)
             /\ WF_vars(Classify) /\ WF_vars(Publish)

TypeOK == /\ rebuild \in {0, 1}
          /\ published \in BOOLEAN
          /\ resolved \in BOOLEAN
          /\ classified \in BOOLEAN

\* A published snapshot never carries a silently dangling dynamic reference:
\* either the cluster is present or its absence is an explicit classified
\* outcome. This is the fail-closed shape already used for errored clusters.
NoSilentDanglingPublish == published => (resolved \/ classified)

\* Progress: the cold proxy eventually receives its first snapshot.
EventuallyPublished == <>published
=============================================================================
