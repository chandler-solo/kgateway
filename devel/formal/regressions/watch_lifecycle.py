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
for _typ in ("EDS", "RDS"):
    for _suffix in ("Compatible", "Incompatible"):
        MARKERS[f"TestFormalWatchContended{_typ}{_suffix}"] = "WATCH-CONTENDED-ACCOUNTING:"


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


def compose(repository, subjects, destination, artifacts):
    # Reuse the tested three-way file overlay, with only allowed read-only Git
    # commands. The three immutable trees, not the source worktree, are inputs.
    from pertype_composition import overlay, diff3
    materialize(repository, subjects["pr"], destination)
    def show(subject, path):
        result = subprocess.run(["git", "-C", str(repository), "show", subjects[subject] + ":" + path], capture_output=True)
        if result.returncode == 0:
            return result.stdout
        if result.returncode == 128 and (b"does not exist in" in result.stderr or b"exists on disk, but not in" in result.stderr):
            return None
        raise ValueError("cannot read composition object: " + result.stderr.decode())
    def write(path, data):
        target = destination / path
        target.parent.mkdir(parents=True, exist_ok=True)
        target.write_bytes(data)
    changes = [tuple(line.split("\t", 1)) for line in subprocess.check_output(
        ["git", "-C", str(repository), "diff", "--name-status", "--no-renames", subjects["stack_base"], subjects["stack_tip"]], text=True).splitlines()]
    taken, merged, conflicts = overlay(changes, show, write, lambda path: (destination / path).unlink(), diff3)
    (artifacts / "composition.json").write_text(json.dumps({"subjects": subjects, "taken": taken,
        "merged": merged, "conflicts": conflicts}, indent=2) + "\n")
    if conflicts:
        raise ValueError("composition requires review of conflicts: " + str(conflicts))


def apply_mutation(source, mutation):
    """One declared production mutation, only in a newly materialized subject."""
    path = source / mutation["file"]
    if source.resolve() not in path.resolve().parents or path.suffix != ".go" or path.name.endswith("_test.go"):
        raise ValueError("mutation must address production Go inside the scratch tree")
    if regression.digest(path) != mutation["sha256"]:
        raise ValueError("mutation source hash mismatch")
    contents = path.read_text()
    if not mutation["before"] or contents.count(mutation["before"]) != 1 or mutation["before"] == mutation["after"]:
        raise ValueError("mutation must change exactly one declared site")
    path.write_text(contents.replace(mutation["before"], mutation["after"]))


def check_receipts(events, outcomes, exit_code, log, failure_markers=None):
    markers, expected = {}, {}
    failure_markers = failure_markers or {}
    if any(outcomes.get(name) != "fail" or not marker for name, marker in failure_markers.items()):
        raise ValueError("failure marker must name a required negative property")
    for test, outcome in outcomes.items():
        markers[test], expected[test] = failure_markers.get(test, "--- FAIL: " + test), outcome
        if test in MARKERS or test.startswith("TestReviewPerTypePolicy"):
            for mode in ("default", "ordered"):
                name = test + "/" + mode
                markers[name], expected[name] = MARKERS.get(test, ""), outcome
    if any(text in log for text in ("HARNESS:", "panic:", "test timed out", "DATA RACE", "[build failed]")):
        raise ValueError("harness/build/race failure is not the expected watch counterexample")
    regression.check_receipts(events, PACKAGE, markers, expected)
    if (exit_code == 0) != ("fail" not in expected.values()):
        raise ValueError("Make exit disagrees with required outcomes")


def run(profile_id, repository, artifacts, full_setup=False):
    profile = json.loads((HERE / "profiles.json").read_text())["profiles"][profile_id]
    source = artifacts / "source"
    if "composition" in profile:
        compose(repository, profile["composition"], source, artifacts)
    else:
        materialize(repository, profile["revision"], source)
    for path, expected in profile["source_hashes"].items():
        if regression.digest(source / path) != expected:
            raise ValueError(f"subject hash mismatch: {path}")
    if profile["dependency"] not in (source / "go.mod").read_text():
        raise ValueError("wrong go-control-plane dependency")
    baseline = regression.production_digest(source)
    if "mutation" in profile:
        if not profile.get("failure_markers") or "fail" not in profile["expected_by_test"].values():
            raise ValueError("production mutation requires a designated negative property")
        apply_mutation(source, profile["mutation"])
    before = regression.production_digest(source)
    regression.apply_test_adapters(source, profile.get("test_adapters", []))
    fixture = HERE / profile["fixture"]
    if regression.digest(fixture) != profile["fixture_sha256"]:
        raise ValueError("fixture hash mismatch")
    target = source / "pkg/kgateway/setup/formal_watch_lifecycle_test.go"
    if target.exists():
        raise ValueError("fixture would overwrite subject source")
    target.write_bytes(fixture.read_bytes())
    for extra in profile.get("extra_fixtures", []):
        extra_source = HERE / extra["file"]
        extra_target = source / profile["package"] / extra["target"]
        if extra_target.exists() or regression.digest(extra_source) != extra["sha256"]:
            raise ValueError("extra fixture collision or hash mismatch")
        extra_target.write_bytes(extra_source.read_bytes())
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
                "baseline_production_digest": hashlib.sha256(json.dumps(baseline, sort_keys=True).encode()).hexdigest(),
                "subject_kind": "declared production mutation" if "mutation" in profile else "unmodified production source",
                "production_digest": hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
                "evidence": profile.get("evidence_scope", "real pinned cache and mock ADS transport under race detector; bounded timer observations, no live Envoy or refinement proof")}
    (artifacts / "subject.json").write_text(json.dumps(evidence, indent=2) + "\n")
    with log.open("w") as stream:
        result = subprocess.run(command, cwd=source, env=dict(os.environ, CGO_ENABLED="1"),
                                stdout=stream, stderr=subprocess.STDOUT, timeout=600)
    events = [json.loads(line) for line in receipt.read_text().splitlines()]
    check_receipts(events, outcomes, result.returncode, log.read_text(), profile.get("failure_markers"))
    if full_setup:
        if "fail" in outcomes.values():
            raise ValueError("full acceptance run requires a fixed subject")
        full_command = [arg for arg in command if not arg.startswith(("GO_TEST_USER_ARGS=", "GOTESTSUM_ARGS="))]
        packages = profile.get("acceptance_packages", [profile["package"]])
        full_command = [arg for arg in full_command if not arg.startswith("TEST_PKG=")]
        full_command.append("TEST_PKG=" + " ".join("./" + package for package in packages))
        full_receipt = artifacts / "setup.jsonl"
        full_command.append("GOTESTSUM_ARGS=--format=short --jsonfile=" + regression.make_shell_arg(str(full_receipt)))
        with (artifacts / "setup.log").open("w") as stream:
            subprocess.run(full_command, cwd=source, env=dict(os.environ, CGO_ENABLED="1"), stdout=stream,
                           stderr=subprocess.STDOUT, timeout=600, check=True)
        full = [json.loads(line) for line in full_receipt.read_text().splitlines()]
        names = {e["Test"] for e in full if e.get("Action") == "run" and e.get("Test") and e.get("Package") == PACKAGE}
        if not set(outcomes) <= names:
            raise ValueError("full setup run omitted watch tests")
        evidence["acceptance_test_events"] = {}
        for package in packages:
            module = "github.com/kgateway-dev/kgateway/v2/" + package
            required = {e["Test"] for e in full if e.get("Action") == "run" and e.get("Test") and e.get("Package") == module}
            if not required:
                raise ValueError("empty acceptance package: " + package)
            regression.check_receipts(full, module, dict.fromkeys(required, ""), "pass")
            evidence["acceptance_test_events"][package] = len(required)
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
