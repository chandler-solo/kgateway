#!/usr/bin/env python3
"""Replay immutable watch-policy subjects without modifying their checkouts.

Only Git show/diff/rev-parse read objects. Logs and materialized subjects must
be outside the research and source repositories. An expected property failure
is successful characterization, never a passing correctness claim.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile

import check as regression

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
PACKAGE = "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/setup"
MARKERS = {
    "TestFormalWatchInitialParking": "WATCH-INITIAL-ACCOUNTING:",
    "TestFormalWatchRetryReplacement": "WATCH-RETRY-ACCOUNTING:",
    "TestFormalWatchVersionReturn": "WATCH-RETRY-PERSISTENCE:",
}


def materialize(repository, revision, destination):
    """Extract exactly the committed tree, including executable bits/symlinks."""
    def git(*args):
        return subprocess.check_output(["git", "-C", str(repository), *args])
    if git("rev-parse", revision + "^{commit}").decode().strip() != revision:
        raise ValueError("subject must name a full immutable commit")
    raw = git("diff", "--raw", "--no-abbrev", "--no-renames", "-z", EMPTY_TREE, revision).split(b"\0")
    for index in range(0, len(raw) - 1, 2):
        meta, path = raw[index].decode().split(), raw[index + 1].decode()
        mode = meta[1]
        if mode not in ("100644", "100755", "120000"):
            raise ValueError(f"unsupported tree entry {mode}: {path}")
        target = destination / path
        if target.is_absolute() and destination not in target.parents:
            raise ValueError("tree entry escapes destination")
        target.parent.mkdir(parents=True, exist_ok=True)
        data = git("show", f"{revision}:{path}")
        if mode == "120000":
            link = Path(os.path.normpath(target.parent / data.decode()))
            if destination not in link.parents:
                raise ValueError(f"symlink escapes source: {path}")
            target.symlink_to(data.decode())
        else:
            target.write_bytes(data)
            target.chmod(int(mode, 8) & 0o777)


def check_receipts(events, outcomes, exit_code, log):
    markers, expected = {}, {}
    for test, outcome in outcomes.items():
        markers[test], expected[test] = "--- FAIL: " + test, outcome
        if test in MARKERS:
            for mode in ("default", "ordered"):
                name = test + "/" + mode
                markers[name], expected[name] = MARKERS[test], outcome
    if any(text in log for text in ("HARNESS:", "panic:", "test timed out", "DATA RACE", "[build failed]")):
        raise ValueError("harness/build/race failure is not the expected watch counterexample")
    regression.check_receipts(events, PACKAGE, markers, expected)
    if (exit_code == 0) != ("fail" not in expected.values()):
        raise ValueError("Make exit disagrees with required outcomes")


def run(profile_id, repository, artifacts, full_setup=False):
    profile = json.loads((HERE / "profiles.json").read_text())["profiles"][profile_id]
    source = artifacts / "source"
    materialize(repository, profile["revision"], source)
    for path, expected in profile["source_hashes"].items():
        if regression.digest(source / path) != expected:
            raise ValueError(f"subject hash mismatch: {path}")
    if profile["dependency"] not in (source / "go.mod").read_text():
        raise ValueError("wrong go-control-plane dependency")
    before = regression.production_digest(source)
    fixture = HERE / profile["fixture"]
    if regression.digest(fixture) != profile["fixture_sha256"]:
        raise ValueError("fixture hash mismatch")
    target = source / "pkg/kgateway/setup/formal_watch_lifecycle_test.go"
    if target.exists():
        raise ValueError("fixture would overwrite subject source")
    target.write_bytes(fixture.read_bytes())
    regression.apply_test_adapters(source, profile["fixture_adapters"])
    outcomes = dict(profile["expected_by_test"])
    outcomes[profile["node_copy_test"]] = "pass"
    selector = "^(" + "|".join(outcomes) + ")$"
    receipt, log = artifacts / "tests.jsonl", artifacts / "tests.log"
    command = ["make", "go-test", "TEST_TAG=e2e", "TEST_PKG=./pkg/kgateway/setup", "GO_TEST_RETRIES=0",
               "GO_TEST_ARGS=-race -count=1 -timeout=5m",
               "GO_TEST_USER_ARGS=-run " + regression.make_shell_arg(selector),
               "OUTPUT_DIR=" + str(artifacts / "output"), "TEST_ASSET_DIR=" + str(artifacts / "assets"),
               "GOTESTSUM_ARGS=--format=short --jsonfile=" + regression.make_shell_arg(str(receipt))]
    evidence = {"profile": profile_id, "subject": profile, "command": command,
                "go_version": subprocess.check_output(["go", "version"], text=True).strip(),
                "production_digest": hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
                "evidence": "real pinned cache and mock ADS transport under race detector; bounded timer observations, no live Envoy or refinement proof"}
    (artifacts / "subject.json").write_text(json.dumps(evidence, indent=2) + "\n")
    with log.open("w") as stream:
        result = subprocess.run(command, cwd=source, env=dict(os.environ, CGO_ENABLED="1"),
                                stdout=stream, stderr=subprocess.STDOUT, timeout=600)
    events = [json.loads(line) for line in receipt.read_text().splitlines()]
    check_receipts(events, outcomes, result.returncode, log.read_text())
    if full_setup:
        if "fail" in outcomes.values():
            raise ValueError("full acceptance run requires a fixed subject")
        full_command = [arg for arg in command if not arg.startswith(("GO_TEST_USER_ARGS=", "GOTESTSUM_ARGS="))]
        full_receipt = artifacts / "setup.jsonl"
        full_command.append("GOTESTSUM_ARGS=--format=short --jsonfile=" + regression.make_shell_arg(str(full_receipt)))
        with (artifacts / "setup.log").open("w") as stream:
            subprocess.run(full_command, cwd=source, env=dict(os.environ, CGO_ENABLED="1"), stdout=stream,
                           stderr=subprocess.STDOUT, timeout=600, check=True)
        full = [json.loads(line) for line in full_receipt.read_text().splitlines()]
        names = {e["Test"] for e in full if e.get("Action") == "run" and e.get("Test")}
        if not set(outcomes) <= names:
            raise ValueError("full setup run omitted watch tests")
        regression.check_receipts(full, PACKAGE, dict.fromkeys(names, ""), "pass")
        evidence["full_setup_test_events"] = len(names)
    if before != regression.production_digest(source):
        raise ValueError("production sources changed during replay")
    evidence["outcome"] = "required characterization outcomes reproduced"
    evidence["expected_by_test"] = outcomes
    (artifacts / "result.json").write_text(json.dumps(evidence, indent=2) + "\n")
    print(f"PASS {profile_id}: required outcomes reproduced; artifacts {artifacts}", flush=True)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    profiles = json.loads((HERE / "profiles.json").read_text())["profiles"]
    parser.add_argument("--profile", choices=[p for p in profiles if p.startswith("watch-")], required=True)
    parser.add_argument("--repository", type=Path, required=True)
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--full-setup", action="store_true")
    args = parser.parse_args()
    artifacts = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-watch-"))).resolve()
    for root in (ROOT, args.repository.resolve()):
        if root == artifacts or root in artifacts.parents:
            parser.error("artifacts must be outside both repositories")
    artifacts.mkdir(parents=True, exist_ok=True)
    if any(artifacts.iterdir()):
        parser.error("artifacts must be empty")
    print(f"Artifacts: {artifacts}", flush=True)
    run(args.profile, args.repository.resolve(), artifacts, args.full_setup)


if __name__ == "__main__":
    main()
