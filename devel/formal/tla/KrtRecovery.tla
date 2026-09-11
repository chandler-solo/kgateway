--------------------------- MODULE KrtRecovery ---------------------------
EXTENDS Naturals

\* RF-005 / KRT-A1: the Lean theorem stuck_client_has_recovery_path exhibits a
\* safe path from a deferred-partial client to convergence, using an abstract
\* heartbeat that supplies a closed candidate. Recoverability is not liveness.
\* This specification states the temporal claim under explicit fairness and
\* names what must be true of the environment or the implementation.
\*
\* InputEventuallyCoherent: KRT delivers the missing dependency, so the
\*   candidate becomes coherent without any watchdog. This is the fairness the
\*   deployed code relies on; no watchdog or periodic re-derivation exists in
\*   pkg/kgateway/proxy_syncer or pkg/kgateway/setup as of this profile.
\* Watchdog: a periodic re-derivation exists and runs fairly.
\* WatchdogReadsAuthoritative: the watchdog rereads authoritative inputs. A
\*   watchdog that replays the stuck cached derivation makes no progress.
CONSTANTS InputEventuallyCoherent, Watchdog, WatchdogReadsAuthoritative
VARIABLES phase, churn
vars == <<phase, churn>>

Phases == {"stableOld", "deferredPartial", "coherentInput", "publishedNew", "activeNew"}

Init == /\ phase = "stableOld"
        /\ churn = 0

\* An input change whose fan-out left this client with a partial candidate.
InputChange == /\ phase = "stableOld"
               /\ phase' = "deferredPartial"
               /\ UNCHANGED churn

\* Unrelated input keeps changing while the client is stuck or converged. The
\* toggle abstracts arbitrarily many rebuilds and keeps every state live so
\* TLC reports the temporal outcome rather than a deadlock.
Churn == /\ phase \in {"deferredPartial", "activeNew"}
         /\ churn' = 1 - churn
         /\ UNCHANGED phase

\* KRT delivers the dependency; the candidate is recomputed coherent.
DependencyArrives == /\ InputEventuallyCoherent
                     /\ phase = "deferredPartial"
                     /\ phase' = "coherentInput"
                     /\ UNCHANGED churn

\* A watchdog re-derives. Reading authoritative inputs yields a coherent
\* candidate; replaying the stuck derivation changes nothing, so under weak
\* fairness the watchdog can run forever without progress.
WatchdogRederive == /\ Watchdog
                    /\ phase = "deferredPartial"
                    /\ phase' = IF WatchdogReadsAuthoritative THEN "coherentInput" ELSE "deferredPartial"
                    /\ churn' = IF WatchdogReadsAuthoritative THEN churn ELSE 1 - churn

Publish == /\ phase = "coherentInput"
           /\ phase' = "publishedNew"
           /\ UNCHANGED churn

Activate == /\ phase = "publishedNew"
            /\ phase' = "activeNew"
            /\ UNCHANGED churn

Next == InputChange \/ Churn \/ DependencyArrives \/ WatchdogRederive \/ Publish \/ Activate

\* Publication and activation are always fair (the cache and Envoy run).
\* Dependency delivery and the watchdog are fair only when assumed to exist.
Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(Publish) /\ WF_vars(Activate)
        /\ WF_vars(DependencyArrives) /\ WF_vars(WatchdogRederive)

TypeOK == phase \in Phases /\ churn \in {0, 1}

\* Temporal claim: a deferred-partial client eventually activates.
DeferredEventuallyActive == [](phase = "deferredPartial" => <>(phase = "activeNew"))
=============================================================================
