#!/usr/bin/env python3
"""Characterize a pinned kgw-ent adoption in scratch copies; never edit its checkout.

This baseline runner intentionally requires the known constructor and client-state
contract failures. A green receipt is not an adoption approval or an image build.
"""
import argparse
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile

from check import HERE, check_build_result, digest, make_shell_arg, materialize, production_digest

PACKAGES = ["internal/xdsclients", "internal/plugins/peering_outbound",
            "internal/plugins/enterprisekgatewaydestinationselector", "internal/gateway_parameters", "cmd/sds"]
DOWNSTREAM = "1bc4cde8b4edc780135c98d2268f43289978373c"


def check_suite(events, required):
    runs, passed, package_passes = set(), set(), set()
    for event in events:
        package = event.get("Package", "").removeprefix("github.com/solo-io/gloo-gateway/v2/")
        if package not in PACKAGES:
            continue
        name, action = event.get("Test"), event.get("Action")
        if action in ("fail", "skip"):
            raise ValueError(f"required downstream package/test {action}: {package}/{name}")
        if not name and action == "pass":
            package_passes.add(package)
        if name and "/" not in name:
            key = (package, name)
            if action == "run":
                if key in runs:
                    raise ValueError("repeated downstream test execution")
                runs.add(key)
            if action == "pass":
                passed.add(key)
    if runs != required or passed != required or package_passes != set(PACKAGES):
        raise ValueError(f"missing/unexpected downstream tests: required={len(required)} run={len(runs)} pass={len(passed)}")


def run_make(source, out, name, args):
    with (out / f"{name}.log").open("w") as log:
        result = subprocess.run(["make", *args], cwd=source, env=dict(os.environ, CGO_ENABLED="0"),
                                stdout=log, stderr=subprocess.STDOUT, timeout=900)
    return result.returncode, (out / f"{name}.log").read_text()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--downstream", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    original = args.downstream.resolve()
    head = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=original, text=True).strip()
    if head != DOWNSTREAM:
        parser.error("this immutable negative profile requires the reviewed downstream revision; add a new profile for another revision")
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-adoption-"))).resolve()
    if out == original or original in out.parents:
        parser.error("artifacts must be outside downstream source")
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        parser.error("artifacts must be fresh")
    print(f"Adoption artifacts: {out}", flush=True)
    # Copy the actual working contents and record their digest; HEAD is not a
    # guarantee that the user's working tree has no additional modifications.
    source = out / "downstream"
    shutil.copytree(original, source, symlinks=False, ignore=shutil.ignore_patterns(
        ".git", "vendor", "_output", "_test", "bin", "node_modules", ".lake"))
    before = production_digest(source)
    profile = json.loads((HERE / "profiles.json").read_text())["profiles"]["stack-14817-2026-10-16"]
    upstream = materialize(profile, out)
    module = source / "go.mod"
    original_module_hash = digest(module)
    module.write_text(module.read_text() + f"\nreplace github.com/kgateway-dev/kgateway/v2 => {upstream}\n")
    evidence = {"evidence_level": "downstream Go compilation/unit tests; no deployment, complete adoption, or image validation",
                "downstream_head": head, "stack_revision": profile["revision"],
                "downstream_status": subprocess.check_output(["git", "status", "--short"], cwd=original, text=True),
                "source_files": before, "original_go_mod_sha256": original_module_hash,
                "adoption_go_mod_sha256": digest(module), "builds": {}, "test_packages": PACKAGES}
    (out / "profile.json").write_text(json.dumps(evidence, indent=2) + "\n")
    # Compile using the authorized image target, but explicitly omit Docker and
    # the Rust module. The expected failure precedes either: this is Go evidence.
    code, output = run_make(source, out, "constructor-build",
                            ["build-gateway-image", "DOCKER_BUILD=true", "ENVOY_MODULE_OUTPUT="])
    check_build_result(code, output, "not enough arguments in call to upstreamsetup.NewControlPlane")
    evidence["builds"]["constructor"] = "expected compile failure"
    required = set()
    for package in PACKAGES:
        for path in (source / package).glob("*_test.go"):
            required.update((package, name) for name in re.findall(r"^func (Test\w+)\(t \*testing.T\)", path.read_text(), re.M))
    jsonl = out / "unit.jsonl"
    unit_args = ["-o", "_output/stamps/generate-charts", "unit", "TEST_TAG=e2e",
                 "TEST_PKG=" + " ".join("./" + p for p in PACKAGES),
                 "TEST_ARGS=-count=1 -timeout=10m",
                 f"GOTESTSUM_ARGS=--format=short --jsonfile={make_shell_arg(str(jsonl))}"]
    code, output = run_make(source, out, "unit", unit_args)
    check_build_result(code, output)
    events = [json.loads(line) for line in jsonl.read_text().splitlines()]
    check_suite(events, required)
    evidence["required_top_level_tests"] = len(required)
    evidence["passed_test_events"] = sum(e.get("Action") == "pass" and bool(e.get("Test")) for e in events)
    fixture = HERE / "fixtures/downstream_state_contract_test.go.txt"
    shutil.copyfile(fixture, source / "internal/xdsclients/formal_state_contract_test.go")
    code, output = run_make(source, out, "client-state-contract",
                            ["-o", "_output/stamps/generate-charts", "unit", "TEST_TAG=e2e",
                             "TEST_PKG=./internal/xdsclients", "TEST_ARGS=-run '^$$' -count=1"])
    for method in ["HasPriorXDSVersion", "HasColdStreamSince", "RunIfDisconnected"]:
        check_build_result(code, output, "missing method " + method)
    evidence["builds"]["client_state"] = "all three missing-method contract failures reproduced"
    evidence["state_fixture_sha256"] = digest(fixture)
    if production_digest(source) != before:
        raise ValueError("downstream production Go files changed in scratch diagnostics")
    evidence["production_sources_unchanged"] = True
    evidence["outcome"] = "known adoption gaps reproduced; selected existing suites pass"
    (out / "receipt.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"PASS adoption negative controls; {len(required)} required existing tests; {out}", flush=True)


if __name__ == "__main__":
    main()
