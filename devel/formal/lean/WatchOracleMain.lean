import XdsSpec.WatchLifecycle
open Lean XdsSpec.WatchLifecycle

def main : IO UInt32 := do
  let stdin ← IO.getStdin
  let stdout ← IO.getStdout
  repeat
    let line ← stdin.getLine
    if line.isEmpty then break
    let result := do
      let json ← Json.parse line
      let request ← fromJson? (α := Request) json
      if json != toJson request then throw "unknown, missing, or noncanonical fields"
      let (observations, outcomes) ← run request
      pure (Json.mkObj [("ok", toJson true), ("observations", toJson observations), ("outcomes", toJson outcomes)])
    let response := match result with
      | .ok value => value
      | .error message => Json.mkObj [("ok", toJson false), ("error", toJson message)]
    stdout.putStrLn response.compress
    stdout.flush
  return 0
