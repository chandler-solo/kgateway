----------------------- MODULE PerTypeCacheRejection -----------------------
\* PR #14865: one parent Envoy rejects (a cluster in CDS, or a listener in LDS)
\* must not freeze its children's type (EDS, or RDS). Envoy applies the valid
\* parents of a NACKed response and requests only the accepted parents'
\* children, so the rejected parent's child stays published but unrequested.
\* PerType = FALSE is go-control-plane's ADS rule: a named child request is
\* answered only when the snapshot holds no child outside it, and a
\* version-new request it declines leaves no watch. PerType = TRUE is the
\* per-type non-ADS cache, which answers the requested names.
\* One child per parent; every publish changes child content and version.
EXTENDS Naturals
CONSTANTS PerType, MaxVersion
VARIABLES version, parents, parentApplied, accepted, reqNames, reqVersion,
          pending, watch, childVersion
vars == <<version, parents, parentApplied, accepted, reqNames, reqVersion,
          pending, watch, childVersion>>

ParentSets == {{"good"}, {"good", "bad"}}

\* The cache's answer rule for a child request: names at held version held.
Respondable(names, held) == held # version /\ (PerType \/ parents \subseteq names)

Init == /\ version = 1
        /\ parents \in ParentSets
        /\ parentApplied = 0
        /\ accepted = {}
        /\ reqNames = {}
        /\ reqVersion = 0
        /\ pending = FALSE
        /\ watch = FALSE
        /\ childVersion = 0

\* The control plane publishes; it may add or remove the rejected parent.
Publish == /\ version < MaxVersion
           /\ version' = version + 1
           /\ parents' \in ParentSets
           /\ UNCHANGED <<parentApplied, accepted, reqNames, reqVersion, pending, watch, childVersion>>

\* Envoy applies a newer parent response, rejecting "bad"; a changed set of
\* accepted parents makes it re-request children, replacing its request.
ApplyParents == /\ parentApplied # version
                /\ parentApplied' = version
                /\ accepted' = parents \ {"bad"}
                /\ IF accepted' # reqNames
                      THEN /\ reqNames' = accepted'
                           /\ reqVersion' = childVersion
                           /\ pending' = TRUE
                           /\ watch' = FALSE
                      ELSE UNCHANGED <<reqNames, reqVersion, pending, watch>>
                /\ UNCHANGED <<version, parents, childVersion>>

\* Envoy receives the children and acknowledges them, a new request at that version.
Respond == /\ childVersion' = version
           /\ reqVersion' = version
           /\ pending' = TRUE
           /\ watch' = FALSE
           /\ UNCHANGED <<version, parents, parentApplied, accepted, reqNames>>

\* The cache handles Envoy's latest request: answers it, parks it at an equal
\* version, or (ADS only) declines a version-new one without keeping a watch.
HandleRequest == /\ pending
                 /\ IF Respondable(reqNames, reqVersion)
                       THEN Respond
                       ELSE /\ pending' = FALSE
                            /\ watch' = (reqVersion = version \/ PerType)
                            /\ UNCHANGED <<version, parents, parentApplied, accepted, reqNames, reqVersion, childVersion>>

\* A later install answers a parked watch the rule now allows.
AnswerWatch == /\ watch
               /\ ~pending
               /\ Respondable(reqNames, reqVersion)
               /\ Respond

Next == Publish \/ ApplyParents \/ HandleRequest \/ AnswerWatch

Spec == Init /\ [][Next]_vars /\ WF_vars(ApplyParents) /\ WF_vars(HandleRequest) /\ WF_vars(AnswerWatch)

TypeOK == /\ version \in 1..MaxVersion
          /\ parents \in ParentSets
          /\ parentApplied \in 0..MaxVersion
          /\ accepted \subseteq {"good"}
          /\ reqNames \subseteq {"good"}
          /\ reqVersion \in 0..MaxVersion
          /\ pending \in BOOLEAN
          /\ watch \in BOOLEAN
          /\ childVersion \in 0..MaxVersion

\* Envoy never holds a newer child version than was published.
NoFutureChildren == childVersion <= version

\* Once publishing stops, Envoy's children catch up to the published version,
\* whether or not the rejected parent is still published, and for a proxy that
\* starts with it published.
ChildrenConverge == <>[](childVersion = version)
=============================================================================
