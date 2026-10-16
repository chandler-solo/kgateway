------------------------- MODULE ConnectionGenerations -------------------------
EXTENDS Naturals
\* One cache key, two connection generations, one retained cache entry and one
\* queued response. A new generation holds a rebuilt candidate until coherent.
\* Generation fencing is a proposed policy, not code adopted by this branch.
CONSTANT Mode
VARIABLES epoch, cacheEpoch, oldRequested, newRequested, coherent, queued,
          waiting, delivered, staleServed
vars == <<epoch, cacheEpoch, oldRequested, newRequested, coherent, queued,
          waiting, delivered, staleServed>>
Init == /\ epoch = 1 /\ cacheEpoch = 1 /\ queued = 0 /\ delivered = 0
        /\ oldRequested = FALSE /\ newRequested = FALSE
        /\ coherent = FALSE /\ waiting = FALSE /\ staleServed = FALSE

OldRequest == /\ epoch = 1 /\ ~oldRequested /\ oldRequested' = TRUE /\ queued' = 1
              /\ UNCHANGED <<epoch, cacheEpoch, newRequested, coherent, waiting, delivered, staleServed>>
Reconnect == /\ epoch = 1 /\ oldRequested /\ epoch' = 2
             /\ queued' = IF Mode = "OldQueue" THEN queued ELSE 0
             /\ UNCHANGED <<cacheEpoch, oldRequested, newRequested, coherent, waiting, delivered, staleServed>>
NewRequest ==
    /\ epoch = 2 /\ ~newRequested /\ queued = 0 /\ newRequested' = TRUE
    /\ IF cacheEpoch = epoch \/ Mode = "OldCache"
          THEN /\ queued' = cacheEpoch /\ waiting' = FALSE
          ELSE /\ queued' = 0 /\ waiting' = TRUE
    /\ UNCHANGED <<epoch, cacheEpoch, oldRequested, coherent, delivered, staleServed>>
Derive == /\ epoch = 2 /\ ~coherent /\ coherent' = TRUE
          /\ UNCHANGED <<epoch, cacheEpoch, oldRequested, newRequested, queued, waiting, delivered, staleServed>>
Install == /\ coherent /\ cacheEpoch = 1 /\ cacheEpoch' = 2
           /\ queued' = (IF waiting THEN 2 ELSE queued) /\ waiting' = FALSE
           /\ UNCHANGED <<epoch, oldRequested, newRequested, coherent, delivered, staleServed>>
Send == /\ queued # 0 /\ delivered' = queued /\ queued' = 0
        /\ staleServed' = (staleServed \/ (queued # epoch))
        /\ UNCHANGED <<epoch, cacheEpoch, oldRequested, newRequested, coherent, waiting>>
Next == OldRequest \/ Reconnect \/ NewRequest \/ Derive \/ Install \/ Send
Spec == Init /\ [][Next]_vars /\ WF_vars(OldRequest) /\ WF_vars(Reconnect)
        /\ WF_vars(NewRequest) /\ WF_vars(Derive) /\ WF_vars(Install) /\ WF_vars(Send)
TypeOK == /\ epoch \in 1..2 /\ cacheEpoch \in 1..2 /\ queued \in 0..2 /\ delivered \in 0..2
          /\ oldRequested \in BOOLEAN /\ newRequested \in BOOLEAN /\ coherent \in BOOLEAN
          /\ waiting \in BOOLEAN /\ staleServed \in BOOLEAN
NoResponseFromEarlierGeneration == ~staleServed
StableConnectionEventuallySent == (epoch = 2 /\ coherent) ~> (delivered = 2)
=============================================================================
