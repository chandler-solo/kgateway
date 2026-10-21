#!/usr/bin/env python3
"""Required bounded watch checks; outputs are external and never reused."""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

from contract import HERE, validate

ROOT = HERE.parents[2]
LEAN = HERE.parent / "lean"


def compare(request, result):
    if set(result) != {"ok", "outcomes", "observations"} or result.get("ok") is not True:
        raise ValueError(f"oracle rejected {request['scenarioId']}: {result}")
    if result.get("outcomes") != request["expectations"]:
        raise ValueError(f"property mismatch {request['scenarioId']}: {result.get('outcomes')}")
    observations = result.get("observations", [])
    fields = {"version", "names", "phase", "captured", "lock", "publication", "watch", "watchOwner",
              "generation", "generations", "timer", "timerOwner", "own", "queued", "sent", "accepted",
              "rejected", "nonce", "applied", "interested", "channel"}
    for observation in observations:
        if set(observation) != {"id", "state"} or not isinstance(observation["state"], dict) or set(observation["state"]) != fields:
            raise ValueError("malformed oracle observation")
        state = observation["state"]
        for field in ("version", "phase", "captured", "lock", "publication", "timer", "accepted", "rejected", "nonce", "applied"):
            if type(state[field]) is not str:
                raise ValueError("malformed oracle state field")
        for field in ("watch", "interested"):
            if type(state[field]) is not bool:
                raise ValueError("malformed oracle boolean")
        for field in ("watchOwner", "generation", "timerOwner"):
            if type(state[field]) is not int or state[field] < 1:
                raise ValueError("malformed oracle owner")
        if not isinstance(state["names"], list) or not all(type(n) is str for n in state["names"]):
            raise ValueError("malformed oracle resource names")
        for field in ("own", "queued", "sent"):
            packet = state[field]
            if packet is not None and (not isinstance(packet, dict) or set(packet) != {"version", "names", "generation"}):
                raise ValueError("malformed oracle packet")
    if [o.get("id") for o in observations] != [e["id"] for e in request["events"]]:
        raise ValueError("missing, duplicated or reordered oracle observations")


def tla_value(value):
    if isinstance(value, bool):
        return str(value).upper()
    if isinstance(value, dict):
        return "[" + ", ".join(k + " |-> " + tla_value(v) for k, v in value.items()) + "]"
    if isinstance(value, list):
        return "<<" + ", ".join(tla_value(v) for v in value) + ">>"
    return json.dumps(value)


def projection_check(requests, results, artifacts):
    jar = artifacts / "tlc/tla2tools.jar"
    if not jar.exists():
        raise ValueError("cross-model replay requires the completed TLC gate")
    for model in ("WatchTraceReplay.tla", "NackWatchLifecycle.tla"):
        (artifacts / model).write_bytes((HERE.parent / "tla" / model).read_bytes())
    count = 0
    rejected_mutation = False
    for request, result in zip(requests, results):
        name = request["scenarioId"]
        if not name.startswith("watch-"):
            continue
        observations = []
        for item in result["observations"]:
            state = item["state"]
            packet = lambda field: int(state[field]["version"]) if state[field] else 0
            observations.append({"version": int(state["version"]), "extra": "B" in state["names"],
                "phase": state["phase"], "captured": int(state["captured"] or 0),
                "lock": state["lock"], "pubPC": state["publication"], "watch": state["watch"],
                "timer": state["timer"], "own": packet("own"), "queued": packet("queued"), "sent": packet("sent")})
        policy = {"watch-early": "early", "watch-retry": "retry", "watch-parking": "parking"}[request["subjectId"]]
        scenario = "initial" if name.endswith("initial") else "return" if "return" in name else "replacement"
        module = "Replay" + "".join(part.capitalize() for part in name.split("-"))
        (artifacts / (module + ".tla")).write_text("---- MODULE " + module + " ----\nEXTENDS WatchTraceReplay\nInputTrace == " +
            tla_value([e["kind"] for e in request["events"]]) + "\nInputStates == " + tla_value(observations) + "\n====\n")
        config = artifacts / (name + ".cfg")
        config.write_text('SPECIFICATION ReplaySpec\nCONSTANTS\n Policy = ' + tla_value(policy) +
            '\n Scenario = ' + tla_value(scenario) + '\n FairTimer = TRUE\n FairTransport = TRUE\n Trace <- InputTrace\n OracleStates <- InputStates' +
            '\nINVARIANTS TypeOK OracleProjection\nPROPERTY CompleteReplay\nCHECK_DEADLOCK FALSE\n')
        command = ["docker", "run", "--rm", "-v", str(HERE.parent / "tla") + ":/model:ro",
                   "-v", str(artifacts) + ":/replay:ro", "-w", "/replay", "eclipse-temurin:21-jre",
                   "java", "-XX:+UseParallelGC", "-jar", "/replay/tlc/tla2tools.jar", "-metadir", "/tmp/states",
                   "-config", "/replay/" + config.name, module + ".tla"]
        completed = subprocess.run(command, capture_output=True, text=True, timeout=30)
        (artifacts / (name + ".log")).write_text(completed.stdout + completed.stderr)
        if completed.returncode or "Model checking completed. No error has been found." not in completed.stdout:
            raise ValueError("cross-model projection/replay failed: " + name)
        count += 1
        if not rejected_mutation:
            observations[0]["watch"] = not observations[0]["watch"]
            negative_module = module + "Mutation"
            path = artifacts / (negative_module + ".tla")
            path.write_text("---- MODULE " + negative_module + " ----\nEXTENDS WatchTraceReplay\nInputTrace == " +
                tla_value([e["kind"] for e in request["events"]]) + "\nInputStates == " + tla_value(observations) + "\n====\n")
            negative = subprocess.run(command[:-1] + [negative_module + ".tla"], capture_output=True, text=True, timeout=30)
            (artifacts / "projection-mutation.log").write_text(negative.stdout + negative.stderr)
            if negative.returncode != 12 or "Error: Invariant OracleProjection is violated." not in negative.stdout:
                raise ValueError("corrupt projection did not fail its designated invariant")
            rejected_mutation = True
    if count == 0:
        raise ValueError("no shared model histories replayed")
    return count


def oracle_run(artifacts, cross_models=False):
    with (artifacts / "lean-build.log").open("w") as log:
        subprocess.run(["lake", "build", "xdswatchoracle"], cwd=LEAN,
                       stdout=log, stderr=subprocess.STDOUT, check=True, timeout=180)
    paths = sorted((HERE / "scenarios").glob("*.json"))
    if not paths:
        raise ValueError("empty required scenario corpus")
    requests = [json.loads(p.read_text()) for p in paths]
    for request in requests:
        validate(request)
    invalid = []
    base = requests[0]
    for mutation in ("unknown", "missing", "duplicate", "edge", "actor", "unused", "bound", "witness", "send"):
        r = copy.deepcopy(base)
        if mutation == "unknown": r["surprise"] = True
        elif mutation == "missing": del r["events"][0]["arguments"]["names"]
        elif mutation == "duplicate": r["events"][1]["id"] = r["events"][0]["id"]
        elif mutation == "edge": r["events"][0]["happensAfter"] = ["future"]
        elif mutation == "actor": r["events"][0]["actor"] = "observer"
        elif mutation == "unused": r["events"][0]["arguments"]["generation"] = 99
        elif mutation == "bound": r["bounds"]["streams"] = 2
        elif mutation == "witness": r["expectations"][0]["witnessOperationIds"] = ["absent"]
        elif mutation == "send": r["events"][0]["kind"], r["events"][0]["actor"] = "send", "transport"
        invalid.append(r)
    payload = "".join(json.dumps(r) + "\n" for r in requests + invalid)
    process = subprocess.run([str(LEAN / ".lake/build/bin/xdswatchoracle")], input=payload,
                             text=True, capture_output=True, check=True, timeout=30)
    results = [json.loads(line) for line in process.stdout.splitlines()]
    if len(results) != len(requests) + len(invalid) or process.stderr:
        raise ValueError("oracle process output incomplete or unexpected")
    for request, result in zip(requests, results):
        compare(request, result)
    for result in results[len(requests):]:
        if result.get("ok") is not False or not result.get("error"):
            raise ValueError("oracle accepted a malformed/illegal history")
    (artifacts / "oracle.json").write_text(json.dumps(results, indent=2) + "\n")
    projections = projection_check(requests, results, artifacts) if cross_models else 0
    return {"cross_model_histories": projections, "valid_scenarios": len(requests), "negative_inputs": len(invalid),
            "scope": "finite abstract histories and kernel-checked local ownership lemmas; no Go differential replay"}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--stage", choices=("models", "oracle", "all"), default="all")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    artifacts = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-watch-models-"))).resolve()
    if artifacts == ROOT or ROOT in artifacts.parents:
        parser.error("artifacts must be outside the repository")
    artifacts.mkdir(parents=True, exist_ok=True)
    if any(artifacts.iterdir()):
        parser.error("artifact directory must be empty")
    sources = list(HERE.rglob("*")) + list((HERE.parent / "tla").glob("NackWatchLifecycle*"))
    sources += [HERE.parent / "tla/WatchTraceReplay.tla", HERE.parent / "tla/check-required.sh",
                LEAN / "WatchOracleMain.lean", LEAN / "XdsSpec/WatchLifecycle.lean", LEAN / "XdsSpec/CacheOracle.lean",
                LEAN / "XdsSpec/PublicationOracle.lean", LEAN / "lakefile.toml", LEAN / "lean-toolchain"]
    result = {"head": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip(),
              "source_hashes": {str(p.relative_to(ROOT)): hashlib.sha256(p.read_bytes()).hexdigest()
                                for p in sorted(set(sources)) if p.is_file() and "__pycache__" not in p.parts}}
    if args.stage in ("models", "all"):
        with (artifacts / "tlc.log").open("w") as log:
            subprocess.run(["make", "formal-tlc"], cwd=ROOT, stdout=log, stderr=subprocess.STDOUT,
                           env=dict(os.environ, TLC_MODEL_PREFIX="NackWatchLifecycle", FORMAL_TLC_ARTIFACT_DIR=str(artifacts / "tlc")),
                           check=True, timeout=300)
        result["tlc"] = "all selected configurations completed with their classified outcomes"
    if args.stage in ("oracle", "all"):
        result["oracle"] = oracle_run(artifacts, args.stage == "all")
    (artifacts / "result.json").write_text(json.dumps(result, indent=2) + "\n")
    print(f"PASS bounded watch {args.stage}; artifacts {artifacts}")


if __name__ == "__main__":
    main()
