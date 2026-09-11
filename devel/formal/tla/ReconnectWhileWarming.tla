------------------------- MODULE ReconnectWhileWarming -------------------------
EXTENDS Naturals

\* RF-028: a proxy has applied a CDS revision whose cluster is warming and is
\* waiting for EDS when its ADS stream resets (controller restart). Measured on
\* Envoy v1.39.1 over SotW (envoyprobe -scenario restart): the reconnected
\* proxy re-requests EDS at its accepted version, LDS and RDS, and does not
\* request CDS until the warming completes. Envoy #36951 and #34334 report the
\* same shape over Delta xDS.
\*
\* After a controller restart the re-derived EDS version for the client is
\* usually equal to the version the proxy already accepted, so the reconnect
\* EDS request parks under the equal-version rule (RF-014, RF-017) and the
\* warming never completes. A CDS-only repair, such as removing the
\* misconfigured cluster, then cannot reach the proxy. The old active cluster
\* keeps serving throughout; this is a stuck-repair window, not an outage.
\*
\* EndpointsEventuallyChange: the environment eventually changes the warming
\*   cluster's endpoints, which moves the EDS version and releases the park.
\*   An assumption about input, not something the publication policy controls.
\* FirstResponseUnconditional: a proposed policy that answers the first
\*   request on a new stream even at an equal version (or bumps the version on
\*   reconnect for clients with warming candidates). It is not implemented and
\*   is the decision recorded under RF-028 in the plan.
\* EdsCacheFallback: Envoy's use_eds_cache_for_ads, which kgateway's bootstrap
\*   enables, completes a warming cluster from the cached ClusterLoadAssignment
\*   when the EDS config source's initial_fetch_timeout expires. kgateway leaves
\*   that timeout unset (Envoy default 15 s), so in the deployed profile this
\*   exit exists and bounds the window; an explicit 0s would remove it.
CONSTANTS EndpointsEventuallyChange, FirstResponseUnconditional, EdsCacheFallback
VARIABLES rebuild, warming, edsRequestOpen, serverEdsVersion, clientEdsVersion,
          cdsRequestOpen, repairDelivered
vars == <<rebuild, warming, edsRequestOpen, serverEdsVersion, clientEdsVersion,
          cdsRequestOpen, repairDelivered>>

\* The run starts at the reconnect: the candidate is warming, the proxy has
\* parked an EDS request at its accepted version, which equals the re-derived
\* version, CDS has not been requested, and a CDS-only repair is pending.
Init == /\ rebuild = 0
        /\ warming = TRUE
        /\ edsRequestOpen = TRUE
        /\ serverEdsVersion = 1
        /\ clientEdsVersion = 1
        /\ cdsRequestOpen = FALSE
        /\ repairDelivered = FALSE

\* Unrelated input keeps changing; abstracts arbitrarily many recomputations
\* that do not touch the warming cluster's endpoints.
Rebuild == /\ rebuild' = 1 - rebuild
           /\ UNCHANGED <<warming, edsRequestOpen, serverEdsVersion,
                          clientEdsVersion, cdsRequestOpen, repairDelivered>>

\* The warming cluster's endpoints change; the EDS version moves once.
EndpointChange == /\ EndpointsEventuallyChange
                  /\ serverEdsVersion = 1
                  /\ serverEdsVersion' = 2
                  /\ UNCHANGED <<rebuild, warming, edsRequestOpen,
                                 clientEdsVersion, cdsRequestOpen, repairDelivered>>

\* The cache answers the parked EDS request. Under the equal-version rule it
\* answers only when the version differs; the proposed policy also answers the
\* first request on the new stream. The response completes the warming.
SendEds == /\ edsRequestOpen
           /\ (serverEdsVersion # clientEdsVersion \/ FirstResponseUnconditional)
           /\ edsRequestOpen' = FALSE
           /\ clientEdsVersion' = serverEdsVersion
           /\ warming' = FALSE
           /\ UNCHANGED <<rebuild, serverEdsVersion, cdsRequestOpen, repairDelivered>>

\* The EDS initial fetch timeout expires and the cached assignment completes
\* the warming without a response; the parked request stays open and the
\* accepted version does not move (measured: the reconnect then re-requests
\* EDS at the same version and CDS).
CacheFallback == /\ EdsCacheFallback
                 /\ warming
                 /\ warming' = FALSE
                 /\ UNCHANGED <<rebuild, edsRequestOpen, serverEdsVersion,
                                clientEdsVersion, cdsRequestOpen, repairDelivered>>

\* Envoy's CDS discovery is paused while a cluster is warming (measured: no
\* CDS request in the reconnect window). The request appears once warming ends.
RequestCds == /\ ~warming
              /\ ~cdsRequestOpen
              /\ ~repairDelivered
              /\ cdsRequestOpen' = TRUE
              /\ UNCHANGED <<rebuild, warming, edsRequestOpen, serverEdsVersion,
                             clientEdsVersion, repairDelivered>>

\* The pending CDS repair is delivered on the open CDS request.
SendCds == /\ cdsRequestOpen
           /\ cdsRequestOpen' = FALSE
           /\ repairDelivered' = TRUE
           /\ UNCHANGED <<rebuild, warming, edsRequestOpen, serverEdsVersion,
                          clientEdsVersion>>

Next == Rebuild \/ EndpointChange \/ SendEds \/ CacheFallback \/ RequestCds \/ SendCds
Spec == Init /\ [][Next]_vars /\ WF_vars(Rebuild) /\ WF_vars(EndpointChange)
             /\ WF_vars(SendEds) /\ WF_vars(CacheFallback) /\ WF_vars(RequestCds)
             /\ WF_vars(SendCds)

TypeOK == /\ rebuild \in {0, 1}
          /\ warming \in BOOLEAN
          /\ edsRequestOpen \in BOOLEAN
          /\ serverEdsVersion \in {1, 2}
          /\ clientEdsVersion \in {1, 2}
          /\ cdsRequestOpen \in BOOLEAN
          /\ repairDelivered \in BOOLEAN

\* The measured Envoy behavior, stated as an invariant of the model: no CDS
\* request is open while the candidate is warming.
NoCdsRequestWhileWarming == cdsRequestOpen => ~warming

\* A repair is delivered only on a request; the model never pushes CDS to a
\* client that has not asked, which SotW forbids.
RepairNeedsRequest == repairDelivered => ~warming

\* Progress: the CDS-only repair eventually reaches the proxy.
EventuallyRepaired == <>repairDelivered
=============================================================================
