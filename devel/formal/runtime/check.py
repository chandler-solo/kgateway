#!/usr/bin/env python3
"""Run required, digest-pinned direct Envoy profiles via the Make build target."""
import argparse
import hashlib
import json
from pathlib import Path
import subprocess
import tempfile

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def complete(exit_code, observations, required):
    phases = [e.get("phase") for e in observations if e.get("event") == "observation"]
    return exit_code == 0 and bool(required) and all(phases.count(p) == 1 for p in required)


def main():
    profiles = json.loads((HERE / "profiles.json").read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", choices=list(profiles["images"]), action="append")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-runtime-"))).resolve()
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        parser.error("artifacts must be fresh; previous results are never reused")
    print(f"Runtime artifacts: {out}", flush=True)
    binary = out / "envoyprobe"
    subprocess.run(["make", "formal-envoyprobe-build", f"FORMAL_ENVOYPROBE_BIN={binary}"], cwd=ROOT, check=True)
    receipt = {"evidence_level": "direct Envoy; no kgateway controller, downstream adoption or deployment",
               "go_mod_sha256": digest(ROOT / "go.mod"), "probe_binary_sha256": digest(binary),
               "probe_sources": {p.name: digest(p) for p in sorted((ROOT / "devel/formal/cmd/envoyprobe").glob("*.go"))},
               "profiles": {}, "scenarios": []}
    for name in args.profile or list(profiles["images"]):
        image = profiles["images"][name]["image"]
        # Pulling is explicit and fails closed. Public CI need only select the
        # upstream profile.
        with (out / f"{name}-pull.log").open("w") as log:
            subprocess.run(["docker", "pull", image], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
        inspected = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
        receipt["profiles"][name] = {"requested": image, "id": inspected["Id"],
                                     "repo_digests": inspected["RepoDigests"], "architecture": inspected["Architecture"]}
        for scenario, config in profiles["scenarios"].items():
            dest = out / name / scenario
            dest.parent.mkdir(parents=True, exist_ok=True)
            with (out / f"{name}-{scenario}.log").open("w") as log:
                command = [str(binary), "-image", image, "-out", str(dest), *config["args"]]
                result = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, timeout=240)
            observations = []
            if (dest / "wire.jsonl").exists():
                observations = [json.loads(line) for line in (dest / "wire.jsonl").read_text().splitlines()]
            phases = [e.get("phase") for e in observations if e.get("event") == "observation"]
            passed = complete(result.returncode, observations, config["required_phases"])
            record = {"profile": name, "scenario": scenario, "exit": result.returncode,
                      "required_phases": config["required_phases"], "observed_phases": phases, "passed": passed,
                      "artifacts": {p.name: digest(p) for p in dest.iterdir() if p.is_file()} if dest.exists() else {}}
            receipt["scenarios"].append(record)
            (out / "progress.json").write_text(json.dumps(receipt, indent=2) + "\n")
            if not passed:
                raise ValueError(f"{name}/{scenario} failed or lacks required observations; see {out}")
            print(f"PASS {name}/{scenario}: {len(config['required_phases'])} required observations", flush=True)
    receipt["outcome"] = "all selected runtime scenarios passed"
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(f"PASS direct runtime profiles; {out}", flush=True)


if __name__ == "__main__":
    main()
