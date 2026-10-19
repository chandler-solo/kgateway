import XdsSpec.PublicationOracle

open Lean XdsSpec.PublicationOracle

-- One independent history per line; stdout is reserved for the protocol.
def main : IO UInt32 := do
  let stdin ← IO.getStdin
  let stdout ← IO.getStdout
  repeat
    let line ← stdin.getLine
    if line.isEmpty then break
    let result := do
      let json ← Json.parse line
      let request ← fromJson? (α := Request) json
      -- Reject ignored/unknown fields and missing optional fields. Object key
      -- order and whitespace have no significance in Json equality.
      if json != toJson request then throw "unknown, missing, or noncanonical fields"
      let observations ← run request
      pure (toJson observations)
    let response := match result with
      | .ok observations => Json.mkObj [("ok", toJson true), ("observations", observations)]
      | .error message => Json.mkObj [("ok", toJson false), ("error", toJson message)]
    stdout.putStrLn response.compress
    stdout.flush
  return 0
