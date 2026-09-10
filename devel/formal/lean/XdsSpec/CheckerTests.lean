import XdsSpec.Checker

namespace XdsSpec.CheckerTests

-- A <-> B is a weakly fair execution that avoids G: exit is only enabled
-- at A, never continuously enabled. G remains reachable from every state.
private def cycle : System Nat Nat where
  name := "ReachabilityIsNotLiveness"
  init := 0
  actions := [0, 1]
  step := fun s a =>
    if s == 2 then some 2
    else if a == 0 then some (1 - s)
    else if s == 0 then some 2 else none
  describeAction := toString

#guard match checkRecoverability cycle (· != 2) (· == 2) with
  | .ok 3 => true
  | _ => false

#guard match checkRecoverability { cycle with actions := [0] } (· != 2) (· == 2) with
  | .unreachableGoal _ 2 => true
  | _ => false

#guard match checkSafety cycle [("NeverG", (· != 2))] with
  | .violation "NeverG" trace 2 => trace.length == 1
  | _ => false

end XdsSpec.CheckerTests
