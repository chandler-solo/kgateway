-------------------------- MODULE NackWatchLifecycle --------------------------
EXTENDS Naturals, FiniteSets
\* One key/type/stream, two names, opaque versions {1,2,3}, queue capacity one.
\* Version 2 has an unrequested name. Versions 1 and 3 are answerable; returning
\* to 1 is legal. This is the named-ADS slice, not a general cache refinement.
CONSTANTS Policy, Scenario, FairTimer, FairTransport
VARIABLES version, extra, publication, pubPC, lock, phase, captured,
          watch, timer, own, queued, sent, ack, applied, history
vars == <<version, extra, publication, pubPC, lock, phase, captured,
          watch, timer, own, queued, sent, ack, applied, history>>

Init == /\ version = IF Scenario = "fallback" THEN 2 ELSE 1
        /\ extra = (Scenario = "fallback")
        /\ publication = IF Scenario = "fallback" THEN 1 ELSE 0
        /\ pubPC = IF Scenario = "fallback" THEN "fanout" ELSE "idle"
        /\ lock = IF Scenario = "fallback" THEN "publish" ELSE "none"
        /\ phase = IF Scenario \in {"initial", "fallback"} THEN "initial-read" ELSE "waiting"
        /\ captured = 0
        /\ watch = (Scenario \notin {"initial", "fallback"})
        /\ timer = IF watch THEN "armed" ELSE "off"
        /\ own = 0 /\ queued = 0 /\ sent = 0 /\ ack = 0 /\ applied = 0
        /\ history = {version}

\* On contention the repaired initial path delegates directly, with no retry.
InitialRead ==
 /\ phase = "initial-read"
 /\ (pubPC # "fanout" \/ (Policy = "parking" /\ lock # "none"))
 /\ IF Policy = "parking" /\ lock # "none"
     THEN /\ phase' = "fallback" /\ UNCHANGED <<lock, captured>>
     ELSE /\ phase' = "initial-park" /\ captured' = version
          /\ lock' = IF Policy = "parking" THEN "watch" ELSE lock
 /\ UNCHANGED <<version,extra,publication,pubPC,watch,timer,own,queued,sent,ack,applied,history>>

InitialPark ==
 /\ phase \in {"initial-park", "fallback"}
 /\ pubPC # "fanout"
 /\ (extra \/ version = captured \/ queued = 0)
 /\ watch' = (~extra /\ version = captured)
 /\ queued' = IF ~extra /\ version # captured THEN version ELSE queued
 /\ timer' = IF phase = "fallback" THEN "off" ELSE "armed"
 /\ lock' = IF lock = "watch" THEN "none" ELSE lock
 /\ phase' = "waiting"
 /\ UNCHANGED <<version,extra,publication,pubPC,captured,own,sent,ack,applied,history>>

BeginPublish ==
 /\ pubPC = "idle" /\ publication < 2 /\ lock = "none"
 /\ (publication = 1 \/ phase \in {"initial-park", "retry-replace", "retry-cancel", "waiting"}
                         \/ Scenario = "return")
 /\ lock' = "publish" /\ pubPC' = "install"
 /\ UNCHANGED <<version,extra,publication,phase,captured,watch,timer,own,queued,sent,ack,applied,history>>
Install ==
 /\ pubPC = "install" /\ lock = "publish"
 /\ publication' = publication + 1
 /\ version' = IF publication = 0 THEN 2 ELSE IF Scenario = "return" THEN 1 ELSE 3
 /\ extra' = (publication = 0)
 /\ history' = history \cup {version'} /\ pubPC' = "fanout"
 /\ UNCHANGED <<lock,phase,captured,watch,timer,own,queued,sent,ack,applied>>
Fanout ==
 /\ pubPC = "fanout" /\ lock = "publish"
 /\ (~watch \/ extra \/ version = 1 \/ queued = 0)
 /\ queued' = IF watch /\ ~extra /\ version # 1 THEN version ELSE queued
 /\ watch' = (watch /\ (extra \/ version = 1))
 /\ pubPC' = "idle" /\ lock' = "none"
 /\ UNCHANGED <<version,extra,publication,phase,captured,timer,own,sent,ack,applied,history>>

Expire ==
 /\ timer = "armed" /\ timer' = "due"
 /\ UNCHANGED <<version,extra,publication,pubPC,lock,phase,captured,watch,own,queued,sent,ack,applied,history>>
BeginRetry ==
 /\ timer = "due" /\ phase = "waiting"
 /\ (Policy = "early" \/ lock = "none")
 /\ timer' = "running" /\ phase' = "retry-read"
 /\ lock' = IF Policy = "early" THEN lock ELSE "watch"
 /\ UNCHANGED <<version,extra,publication,pubPC,captured,watch,own,queued,sent,ack,applied,history>>
ReadRetry ==
 /\ phase = "retry-read" /\ pubPC # "fanout" /\ captured' = version
 /\ IF version = 1
     THEN /\ phase' = IF Policy = "early" THEN "retry-cancel" ELSE "retry-replace"
          /\ UNCHANGED <<timer,lock>>
     ELSE /\ phase' = "waiting"
          /\ timer' = IF Policy = "early" THEN "off" ELSE "armed"
          /\ lock' = IF lock = "watch" THEN "none" ELSE lock
 /\ UNCHANGED <<version,extra,publication,pubPC,watch,own,queued,sent,ack,applied,history>>
CancelOld ==
 /\ phase = "retry-cancel" /\ pubPC # "fanout" /\ watch' = FALSE /\ phase' = "retry-replace"
 /\ UNCHANGED <<version,extra,publication,pubPC,lock,captured,timer,own,queued,sent,ack,applied,history>>
Replace ==
 /\ phase = "retry-replace" /\ pubPC # "fanout" /\ own = 0
 /\ own' = IF extra THEN 0 ELSE version
 /\ watch' = (watch /\ extra)
 /\ timer' = IF extra /\ Policy # "early" THEN "armed" ELSE "off"
 /\ phase' = "waiting" /\ lock' = IF lock = "watch" THEN "none" ELSE lock
 /\ UNCHANGED <<version,extra,publication,pubPC,captured,queued,sent,ack,applied,history>>
Forward ==
 /\ own # 0 /\ queued = 0 /\ queued' = own /\ own' = 0
 /\ UNCHANGED <<version,extra,publication,pubPC,lock,phase,captured,watch,timer,sent,ack,applied,history>>
Send ==
 /\ queued # 0 /\ sent' = queued /\ queued' = 0
 /\ UNCHANGED <<version,extra,publication,pubPC,lock,phase,captured,watch,timer,own,ack,applied,history>>
Ack == /\ sent # 0 /\ ack # sent /\ ack' = sent
       /\ UNCHANGED <<version,extra,publication,pubPC,lock,phase,captured,watch,timer,own,queued,sent,applied,history>>
Apply == /\ sent # 0 /\ applied # sent /\ applied' = sent
         /\ UNCHANGED <<version,extra,publication,pubPC,lock,phase,captured,watch,timer,own,queued,sent,ack,history>>

Next == InitialRead \/ InitialPark \/ BeginPublish \/ Install \/ Fanout \/ Expire
        \/ BeginRetry \/ ReadRetry \/ CancelOld \/ Replace \/ Forward \/ Send \/ Ack \/ Apply
Spec == Init /\ [][Next]_vars
        /\ WF_vars(InitialRead) /\ WF_vars(InitialPark)
        /\ WF_vars(BeginPublish) /\ WF_vars(Install) /\ WF_vars(Fanout)
        /\ WF_vars(BeginRetry) /\ WF_vars(ReadRetry) /\ WF_vars(CancelOld) /\ WF_vars(Replace)
        /\ (FairTimer => WF_vars(Expire))
        /\ (FairTransport => (WF_vars(Forward) /\ WF_vars(Send)))
TypeOK == /\ version \in 1..3 /\ publication \in 0..2 /\ extra \in BOOLEAN
          /\ captured \in 0..3 /\ watch \in BOOLEAN
          /\ lock \in {"none","publish","watch"}
          /\ pubPC \in {"idle","install","fanout"}
          /\ phase \in {"initial-read","initial-park","fallback","waiting","retry-read","retry-cancel","retry-replace"}
          /\ timer \in {"off","armed","due","running"}
          /\ own \in 0..3 /\ queued \in 0..3 /\ sent \in 0..3
          /\ ack \in 0..3 /\ applied \in 0..3 /\ history \subseteq 1..3
ResponseProvenance == /\ (own # 0 => own \in history) /\ (queued # 0 => queued \in history)
                      /\ (sent # 0 => sent \in history)
LockOwnership == (lock = "watch") => phase \in {"initial-park","retry-read","retry-replace"}
EventuallyAnswered == (<>[] (publication = 2 /\ ~extra)) => <> (sent # 0)
PublicationEventuallyCompletes == <> (publication = 2)
\* Invalid stronger claims are checked in separately classified configurations.
InstallationMeansSend == publication = 2 => sent # 0
AckMeansApplication == ack # 0 => applied # 0
=============================================================================
