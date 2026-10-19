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


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def production_digest(source):
    """Adapters may repair test API drift, never the subject being measured."""
    return {str(p.relative_to(source)): digest(p) for p in source.rglob("*.go")
            if not p.name.endswith("_test.go") and "vendor" not in p.parts}


def apply_test_adapters(source, adapters):
    for adapter in adapters:
        path = source / adapter["file"]
        if not path.name.endswith("_test.go") or source.resolve() not in path.resolve().parents:
            raise ValueError("adapters must address test files inside the scratch source")
        if digest(path) != adapter["sha256"]:
            raise ValueError(f"test adapter source changed: {path}")
        contents = path.read_text()
        if contents.count(adapter["before"]) != 1:
            raise ValueError(f"test adapter must replace exactly one site: {path}")
        path.write_text(contents.replace(adapter["before"], adapter["after"]))


def check_build_result(code, output, expected_error=None):
    if expected_error is None:
        if code:
            raise ValueError("package failed to compile")
    elif code == 0 or expected_error not in output:
        raise ValueError(f"expected pinned compile failure absent: {expected_error}")


def check_receipts(events, package, tests, expected):
    """A build error, skip, panic, or different assertion is not a reproduction."""
    outcomes = {name: expected for name in tests} if isinstance(expected, str) else expected
    if set(outcomes) != set(tests) or any(v not in ("pass", "fail") for v in outcomes.values()):
        raise ValueError("every required property needs an explicit pass/fail outcome")
    package_expected = "fail" if "fail" in outcomes.values() else "pass"
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
    if package_results != [package_expected]:
        raise ValueError(f"unexpected package result: {package_results}")
    for name, marker in tests.items():
        if runs[name] != 1 or terminals[name] != [outcomes[name]]:
            raise ValueError(f"unexpected execution result for {name}")
        if "panic:" in output.get(name, "") or "test timed out" in output.get(name, ""):
            raise ValueError(f"panic/timeout is not a counterexample: {name}")
        if outcomes[name] == "fail" and (not marker or marker not in output.get(name, "")):
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
    profiles = json.loads((HERE / "profiles.json").read_text())["profiles"]
    parser.add_argument("--profile", choices=list(profiles), default="stack-14817")
    parser.add_argument("--source", type=Path, help="read-only candidate source; requires --expect-fixed")
    parser.add_argument("--expect-fixed", action="store_true", help="require the stack properties and setup build to pass")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    if bool(args.source) != args.expect_fixed or (args.expect_fixed and not args.profile.startswith("stack-14817")):
        parser.error("candidate runs require both --source and --expect-fixed for a stack-14817 profile")
    profile = profiles[args.profile]
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix="xds-regressions-"))
    artifacts = artifacts.resolve()
    if args.source and (args.source.resolve() == artifacts or args.source.resolve() in artifacts.parents):
        parser.error("artifacts must be outside the candidate source tree")
    artifacts.mkdir(parents=True, exist_ok=True)
    if any(artifacts.iterdir()):
        parser.error("artifacts directory must be empty; previous receipts are never overwritten or reused")
    print(f"Artifacts: {artifacts}", flush=True)
    source = materialize(profile, artifacts, args.source)
    fixtures = profile.get("fixtures", [{"file": profile.get("fixture"),
                            "target": profile.get("replace_file", "review_stack14817_test.go")}])
    for fixture in fixtures:
        if fixture.get("sha256", digest(HERE / fixture["file"])) != digest(HERE / fixture["file"]):
            raise ValueError(f"pinned fixture changed: {fixture['file']}")
    before = production_digest(source)
    evidence = {"profile": args.profile, "baseline_revision": profile["revision"],
                "subject": "candidate copy" if args.source else "immutable historical source",
                "evidence_level": "Go/cache boundary; no live Envoy or temporal proof",
                "go_mod_sha256": digest(source / "go.mod"),
                "fixtures": {f["file"]: digest(HERE / f["file"]) for f in fixtures},
                "build_checks": [], "test_adapters": []}
    (artifacts / "profile.json").write_text(json.dumps(evidence, indent=2) + "\n")
    builds = profile.get("build_checks", [])
    if "setup_build_error" in profile:
        builds = [{"package": "pkg/kgateway/setup", "error": profile["setup_build_error"]}]
    for index, build in enumerate(builds):
        code, _, log = run_make(source, build["package"], "^$", artifacts, f"pristine-build-{index}")
        check_build_result(code, log.read_text(), None if args.expect_fixed else build.get("error"))
        evidence["build_checks"].append({**build, "exit": code, "log": log.name})
    # A candidate must compile unchanged. Diagnostic adapters are only for the
    # pinned failing baseline, and their complete contents/hashes are recorded.
    if not args.expect_fixed:
        apply_test_adapters(source, profile.get("test_adapters", []))
        evidence["test_adapters"] = profile.get("test_adapters", [])
        if evidence["test_adapters"]:
            for index, build in enumerate(builds):
                code, _, log = run_make(source, build["package"], "^$", artifacts, f"adapted-build-{index}")
                check_build_result(code, log.read_text())
    for fixture in fixtures:
        target = source / profile["package"] / fixture["target"]
        shutil.copyfile(HERE / fixture["file"], target)
    # New implementation APIs can require a fixture to mirror initialization
    # that production already performs. Keep the immutable fixture unchanged;
    # apply exact-hash, test-only adapters after materializing it in scratch.
    apply_test_adapters(source, profile.get("fixture_adapters", []))
    evidence["fixture_adapters"] = profile.get("fixture_adapters", [])
    selector = "^(" + "|".join(profile["tests"]) + ")$"
    code, receipt, log = run_make(source, profile["package"], selector, artifacts, "properties")
    expected = "pass" if args.expect_fixed else profile.get("expected_by_test", profile["expected"])
    package_expected = ("fail" if "fail" in expected.values() else "pass") if isinstance(expected, dict) else expected
    if (code == 0) != (package_expected == "pass"):
        raise ValueError(f"unexpected Make exit {code}; inspect {log}")
    events = [json.loads(line) for line in receipt.read_text().splitlines() if line.strip()]
    check_receipts(events, "github.com/kgateway-dev/kgateway/v2/" + profile["package"], profile["tests"], expected)
    if production_digest(source) != before:
        raise ValueError("production Go sources changed during the diagnostic run")
    evidence["production_sources_unchanged"] = True
    evidence["production_source_sha256"] = hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()
    evidence["outcome"] = "properties pass" if args.expect_fixed else "pinned property outcomes reproduced"
    evidence["expected_by_test"] = expected
    evidence["tests"] = len(profile["tests"])
    (artifacts / "receipt.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"PASS {evidence['outcome']}; {len(profile['tests'])} required tests; {artifacts}", flush=True)


if __name__ == "__main__":
    main()
