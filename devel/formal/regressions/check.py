#!/usr/bin/env python3
"""Run immutable negative controls separately from the research implementation.

Only Makefile test targets execute Go. No Git mutations, checkout, or patching
of a supplied source tree: every run uses a fresh temporary copy/archive.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import shutil
import shlex
import subprocess
import tarfile
import tempfile

HERE = Path(__file__).resolve().parent


def check_receipts(events, package, tests, expected):
    """A build error, skip, panic, or different assertion is not a reproduction."""
    runs, terminals, output, package_results = {}, {}, {}, []
    for event in events:
        if event.get("Package") != package:
            continue
        name, action = event.get("Test"), event.get("Action")
        if not name:
            if action in ("pass", "fail", "skip"):
                package_results.append(action)
            continue
        if action == "run":
            runs[name] = runs.get(name, 0) + 1
        if action in ("pass", "fail", "skip"):
            terminals.setdefault(name, []).append(action)
        if action == "output":
            output[name] = output.get(name, "") + event.get("Output", "")
    if set(runs) != set(tests) or set(terminals) != set(tests):
        raise ValueError("missing or unexpected test execution")
    if package_results != [expected]:
        raise ValueError(f"unexpected package result: {package_results}")
    for name, marker in tests.items():
        if runs[name] != 1 or terminals[name] != [expected]:
            raise ValueError(f"unexpected execution result for {name}")
        if "panic:" in output.get(name, "") or "test timed out" in output.get(name, ""):
            raise ValueError(f"panic/timeout is not a counterexample: {name}")
        if expected == "fail" and marker not in output.get(name, ""):
            raise ValueError(f"expected property failure absent: {name}")


def materialize(profile, artifacts, supplied=None):
    destination = artifacts / "source"
    if supplied:
        shutil.copytree(supplied, destination, symlinks=False,
                        ignore=shutil.ignore_patterns(".git", "vendor", "_output", "_test", "bin", ".lake"))
        return destination
    archive = artifacts / "source.tar.gz"
    with archive.open("wb") as stream:
        subprocess.run(["gh", "api", f"repos/{profile['repository']}/tarball/{profile['revision']}"],
                       stdout=stream, check=True, timeout=180)
    with tarfile.open(archive) as tar:
        members = tar.getmembers()
        roots = {m.name.split("/")[0] for m in members}
        if len(roots) != 1 or profile["revision"][:7] not in next(iter(roots)):
            raise ValueError("archive root does not identify the pinned revision")
        tar.extractall(artifacts / "archive", filter="data")
    shutil.move(artifacts / "archive" / next(iter(roots)), destination)
    return destination


def make_shell_arg(value):
    # Make expands dollars before the recipe shell interprets quoting.
    return shlex.quote(value).replace("$", "$$")


def run_make(source, package, selector, artifacts, name):
    receipt = artifacts / f"{name}.jsonl"
    log = artifacts / f"{name}.log"
    command = ["make", "go-test", "TEST_TAG=e2e", f"TEST_PKG=./{package}",
               "GO_TEST_ARGS=-timeout=8m -count=1", f"GO_TEST_USER_ARGS=-run {make_shell_arg(selector)}",
               f"GOTESTSUM_ARGS=--format=short --jsonfile={make_shell_arg(str(receipt))}"]
    env = dict(os.environ, CGO_ENABLED="0")
    with log.open("w") as stream:
        result = subprocess.run(command, cwd=source, env=env, stdout=stream,
                                stderr=subprocess.STDOUT, timeout=600)
    return result.returncode, receipt, log


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", choices=["stack-14817", "historical-contents-gate"], default="stack-14817")
    parser.add_argument("--source", type=Path, help="read-only candidate source; requires --expect-fixed")
    parser.add_argument("--expect-fixed", action="store_true", help="require the stack properties and setup build to pass")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    if bool(args.source) != args.expect_fixed or (args.expect_fixed and args.profile != "stack-14817"):
        parser.error("candidate runs require both --source and --expect-fixed for stack-14817")
    profile = json.loads((HERE / "profiles.json").read_text())["profiles"][args.profile]
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix="xds-regressions-"))
    artifacts = artifacts.resolve()
    if args.source and (args.source.resolve() == artifacts or args.source.resolve() in artifacts.parents):
        parser.error("artifacts must be outside the candidate source tree")
    artifacts.mkdir(parents=True, exist_ok=True)
    if any(artifacts.iterdir()):
        parser.error("artifacts directory must be empty; previous receipts are never overwritten or reused")
    print(f"Artifacts: {artifacts}", flush=True)
    source = materialize(profile, artifacts, args.source)
    evidence = {"profile": args.profile, "baseline_revision": profile["revision"],
                "subject": "candidate copy" if args.source else "immutable historical source",
                "evidence_level": "Go/cache boundary; no live Envoy or temporal proof",
                "go_mod_sha256": hashlib.sha256((source / "go.mod").read_bytes()).hexdigest(),
                "fixture_sha256": hashlib.sha256((HERE / profile["fixture"]).read_bytes()).hexdigest()}
    (artifacts / "profile.json").write_text(json.dumps(evidence, indent=2) + "\n")
    if "setup_build_error" in profile:
        code, _, log = run_make(source, "pkg/kgateway/setup", "^$", artifacts, "setup-build")
        if args.expect_fixed:
            if code:
                raise ValueError(f"candidate setup package failed to compile: {log}")
        elif not code or profile["setup_build_error"] not in log.read_text():
            raise ValueError(f"expected pinned setup compile failure absent: {log}")
    target = source / profile["package"] / profile.get("replace_file", "review_stack14817_test.go")
    shutil.copyfile(HERE / profile["fixture"], target)
    selector = "^(" + "|".join(profile["tests"]) + ")$"
    code, receipt, log = run_make(source, profile["package"], selector, artifacts, "properties")
    expected = "pass" if args.expect_fixed else profile["expected"]
    if (code == 0) != (expected == "pass"):
        raise ValueError(f"unexpected Make exit {code}; inspect {log}")
    events = [json.loads(line) for line in receipt.read_text().splitlines() if line.strip()]
    check_receipts(events, "github.com/kgateway-dev/kgateway/v2/" + profile["package"], profile["tests"], expected)
    evidence["outcome"] = "properties pass" if args.expect_fixed else "historical counterexamples reproduced"
    evidence["tests"] = len(profile["tests"])
    (artifacts / "receipt.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"PASS {evidence['outcome']}; {len(profile['tests'])} required tests; {artifacts}", flush=True)


if __name__ == "__main__":
    main()
