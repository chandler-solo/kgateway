---------------------- MODULE ReachabilityIsNotLiveness ----------------------
EXTENDS Naturals
VARIABLE state
vars == <<state>>
Init == state = 0
Toggle == /\ state \in {0, 1}
          /\ state' = 1 - state
Exit == /\ state = 0
        /\ state' = 2
Done == /\ state = 2
        /\ UNCHANGED vars
Next == Toggle \/ Exit \/ Done
Spec == Init /\ [][Next]_vars /\ WF_vars(Toggle) /\ WF_vars(Exit)
EventuallyGoal == <> (state = 2)
=============================================================================
