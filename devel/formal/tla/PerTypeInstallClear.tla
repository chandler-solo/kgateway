------------------------ MODULE PerTypeInstallClear ------------------------
\* PR #14865 installs a snapshot one per-type cache at a time, in publish
\* order (SDS, CDS, EDS, LDS, RDS). Stack #14817's connection-start hook
\* clears the node's entry outside the gate's lock while the gate may be
\* installing a build for the previous connection; the stack relies on that
\* build either finishing before the clear (and being cleared) or being a
\* whole fresh build after it. NodeLock models perTypeCache.lockNode, which
\* serializes installs and clears per node.
EXTENDS Naturals
CONSTANTS NodeLock, MaxBuilds, MaxClears
Types == 1..5
VARIABLES cache, epochOf, installing, installStep, installEpoch,
          clearing, clearStep, clears, builds, holder
vars == <<cache, epochOf, installing, installStep, installEpoch,
          clearing, clearStep, clears, builds, holder>>

Free == ~NodeLock \/ holder = "none"

Init == /\ cache = [t \in Types |-> 0]
        /\ epochOf = [t \in Types |-> 0]
        /\ installing = 0 /\ installStep = 1 /\ installEpoch = 0
        /\ clearing = FALSE /\ clearStep = 1 /\ clears = 0
        /\ builds = 0 /\ holder = "none"

BeginInstall == /\ installing = 0 /\ builds < MaxBuilds /\ Free
                /\ builds' = builds + 1
                /\ installing' = builds + 1
                /\ installStep' = 1
                /\ installEpoch' = clears
                /\ holder' = IF NodeLock THEN "install" ELSE holder
                /\ UNCHANGED <<cache, epochOf, clearing, clearStep, clears>>

InstallStep == /\ installing # 0
               /\ cache' = [cache EXCEPT ![installStep] = installing]
               /\ epochOf' = [epochOf EXCEPT ![installStep] = installEpoch]
               /\ IF installStep = 5
                     THEN /\ installing' = 0 /\ installStep' = 1
                          /\ holder' = IF NodeLock THEN "none" ELSE holder
                     ELSE /\ installStep' = installStep + 1
                          /\ UNCHANGED <<installing, holder>>
               /\ UNCHANGED <<installEpoch, clearing, clearStep, clears, builds>>

BeginClear == /\ ~clearing /\ clears < MaxClears /\ Free
              /\ clearing' = TRUE /\ clearStep' = 1
              /\ holder' = IF NodeLock THEN "clear" ELSE holder
              /\ UNCHANGED <<cache, epochOf, installing, installStep, installEpoch, clears, builds>>

ClearStep == /\ clearing
             /\ cache' = [cache EXCEPT ![clearStep] = 0]
             /\ IF clearStep = 5
                   THEN /\ clearing' = FALSE /\ clearStep' = 1 /\ clears' = clears + 1
                        /\ holder' = IF NodeLock THEN "none" ELSE holder
                   ELSE /\ clearStep' = clearStep + 1
                        /\ UNCHANGED <<clearing, clears, holder>>
             /\ UNCHANGED <<epochOf, installing, installStep, installEpoch, builds>>

Next == BeginInstall \/ InstallStep \/ BeginClear \/ ClearStep

Spec == Init /\ [][Next]_vars

TypeOK == /\ cache \in [Types -> 0..MaxBuilds]
          /\ clears \in 0..MaxClears
          /\ holder \in {"none", "install", "clear"}

\* Once a clear has completed, every cached type comes from an install that
\* began after it: nothing begun for the previous connection survives it.
NoStaleAfterClear == ~clearing => \A t \in Types : cache[t] # 0 => epochOf[t] = clears
=============================================================================
