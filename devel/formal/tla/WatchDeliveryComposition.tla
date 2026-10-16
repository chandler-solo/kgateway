------------------------- MODULE WatchDeliveryComposition -------------------------
EXTENDS Naturals
\* One named EDS request, one key/type, three versions, a silent client.
\* v1 is an ineligible superset; v2/v3 are eligible authoritative corrections.
\* Installation precedes fan-out; a canceled fan-out can leave a parked watch.
\* Fixed assumes weak fairness of a NEW correcting publication after failure.
\* Go does not supply that retry automatically. No time bound is claimed.
CONSTANT Mode
VARIABLES admitted, watch, cache, published, failed, queued, sent, ack, applied
vars == <<admitted, watch, cache, published, failed, queued, sent, ack, applied>>

Init == /\ admitted = FALSE /\ watch = FALSE /\ cache = 1
        /\ published = FALSE /\ failed = FALSE
        /\ queued = 0 /\ sent = 0 /\ ack = 0 /\ applied = 0

Admit == /\ ~admitted /\ admitted' = TRUE
         /\ watch' = (Mode # "ImmediateLost")
         /\ UNCHANGED <<cache, published, failed, queued, sent, ack, applied>>

Publish(cancelled) ==
    /\ admitted /\ ~published
    /\ cache' = 2 /\ published' = TRUE /\ failed' = cancelled
    /\ queued' = IF watch /\ ~cancelled THEN 2 ELSE 0
    /\ watch' = (watch /\ cancelled)
    /\ UNCHANGED <<admitted, sent, ack, applied>>

CorrectAgain ==
    /\ published /\ failed /\ Mode # "NoRetry"
    /\ cache' = 3 /\ failed' = FALSE
    /\ queued' = (IF watch THEN 3 ELSE 0) /\ watch' = FALSE
    /\ UNCHANGED <<admitted, published, sent, ack, applied>>

Send == /\ queued # 0 /\ sent' = queued /\ queued' = 0
        /\ UNCHANGED <<admitted, watch, cache, published, failed, ack, applied>>
Ack == /\ sent # 0 /\ ack # sent /\ ack' = sent
       /\ UNCHANGED <<admitted, watch, cache, published, failed, queued, sent, applied>>
\* Application is independent of ACK order, and is not assumed fair here.
Apply == /\ sent # 0 /\ applied # sent /\ applied' = sent
         /\ UNCHANGED <<admitted, watch, cache, published, failed, queued, sent, ack>>
PublishAny == Publish(TRUE) \/ Publish(FALSE)
Next == Admit \/ PublishAny \/ CorrectAgain \/ Send \/ Ack \/ Apply
Spec == Init /\ [][Next]_vars /\ WF_vars(Admit) /\ WF_vars(PublishAny)
        /\ WF_vars(CorrectAgain) /\ WF_vars(Send) /\ WF_vars(Ack)
TypeOK == /\ admitted \in BOOLEAN /\ watch \in BOOLEAN /\ published \in BOOLEAN
          /\ failed \in BOOLEAN /\ cache \in 1..3
          /\ queued \in 0..3 /\ sent \in 0..3 /\ ack \in 0..3 /\ applied \in 0..3
ResponseHasInstallation == sent <= cache
ObservedAckHasResponse == ack <= sent
ApplicationHasResponse == applied <= sent
SilentClientEventuallySent == admitted ~> (sent >= 2)
\* Intentional invalid claims, checked ONLY as named negative controls.
InstallationMeansSend == published => sent >= 2
AckMeansApplication == ack <= applied
=============================================================================
