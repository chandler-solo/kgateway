#!/usr/bin/env python3
"""Replay all 16 pinned review histories against a COPY of patched upstream.

Only the queued producer's entry point is adapted. Its schedule and assertions,
all other fixtures, and the historical baseline profile remain unchanged.
All Go commands run through upstream's Makefile. No Git/GitHub mutations.
"""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess

HERE = Path(__file__).resolve().parent
REGRESSIONS = HERE.parents[1] / "regressions"
spec = importlib.util.spec_from_file_location("regressions_check", REGRESSIONS / "check.py")
check = importlib.util.module_from_spec(spec)
spec.loader.exec_module(check)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--source", type=Path, required=True, help="upstream with both patches already applied")
    parser.add_argument("--artifacts", type=Path, required=True, help="new, empty scratch directory outside source")
    parser.add_argument("--repeat", type=int, default=1)
    args = parser.parse_args()
    source_path, artifacts = args.source.resolve(), args.artifacts.resolve()
    if args.repeat < 1 or artifacts == source_path or source_path in artifacts.parents:
        parser.error("repeat must be positive and artifacts must be outside the source")
    artifacts.mkdir(parents=True, exist_ok=True)
    if any(artifacts.iterdir()):
        parser.error("artifacts must be empty; previous receipts are never overwritten")
    profile = json.loads((REGRESSIONS / "profiles.json").read_text())["profiles"]["stack-14817-7d930bb2183b"]
    source = check.materialize(profile, artifacts, source_path)
    before = check.production_digest(source)
    receipt = {"base": profile["revision"], "subject": "patched upstream copy",
               "evidence_level": "Go/KRT/admission/SnapshotCache; no live Envoy or Go refinement proof",
               "patch_sha256": {p.name: check.digest(p) for p in HERE.glob("*.patch")},
               "source_go_sha256": before, "go_mod_sha256": check.digest(source / "go.mod"),
               "builds": [], "runs": []}
    for index, build in enumerate(profile["build_checks"]):
        code, _, log = check.run_make(source, build["package"], "^$", artifacts, f"build-{index}")
        check.check_build_result(code, log.read_text())
        receipt["builds"].append({"package": build["package"], "exit": code, "log": log.name})
    for fixture in profile["fixtures"]:
        original = REGRESSIONS / fixture["file"]
        if check.digest(original) != fixture["sha256"]:
            raise ValueError(f"historical fixture changed: {original}")
        shutil.copyfile(original, source / profile["package"] / fixture["target"])
    adapter = json.loads((HERE / "queued-history-adapter.json").read_text())
    check.apply_test_adapters(source, [adapter])
    receipt["test_adapter"] = adapter
    receipt["adapted_fixture_sha256"] = check.digest(source / adapter["file"])
    selector = "^(" + "|".join(profile["tests"]) + ")$"
    for run in range(args.repeat):
        output = artifacts / f"properties-{run}.jsonl"
        log = artifacts / f"properties-{run}.log"
        command = ["make", "go-test", "TEST_TAG=e2e", f"TEST_PKG=./{profile['package']}",
                   "GO_TEST_ARGS=-timeout=8m -count=1 -race",
                   f"GO_TEST_USER_ARGS=-run {check.make_shell_arg(selector)}",
                   f"GOTESTSUM_ARGS=--format=short --jsonfile={check.make_shell_arg(str(output))}"]
        with log.open("w") as stream:
            result = subprocess.run(command, cwd=source, env=dict(os.environ, CGO_ENABLED="1"),
                                    stdout=stream, stderr=subprocess.STDOUT, timeout=600)
        if result.returncode:
            raise ValueError(f"candidate failed; inspect {log}")
        events = [json.loads(line) for line in output.read_text().splitlines() if line.strip()]
        check.check_receipts(events, "github.com/kgateway-dev/kgateway/v2/" + profile["package"],
                             profile["tests"], "pass")
        receipt["runs"].append({"command": command, "exit": result.returncode,
                                "tests": len(profile["tests"]), "jsonl": output.name, "log": log.name})
        print(f"PASS all {len(profile['tests'])} review properties with race detection ({run + 1}/{args.repeat})", flush=True)
    if check.production_digest(source) != before:
        raise ValueError("production sources changed during validation")
    receipt["production_sources_unchanged"] = True
    (artifacts / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")


if __name__ == "__main__":
    main()
