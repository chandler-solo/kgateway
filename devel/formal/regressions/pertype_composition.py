#!/usr/bin/env python3
"""Check stack #14817 composed with PR #14865's per-type snapshot cache.

The subject is the PR's head (current main plus the per-type cache) with every
file the stack changed overlaid by a three-way merge against the stack's base,
as a rebase of the stack onto the PR would produce. Git objects are only read
(diff --raw and show); no ref, index or worktree changes. One exact-hash test
adapter repairs the stack's watch-count helper, which names the secrets cache
the PR replaces. Production Go sources are never adapted.

Required: the five-package race suite passes on the subject; the PR's
rejected-resource tests and the same six scenarios driven through the stack's
watch policy pass in both ADS modes; and the stack's backend-closure, history
and rewarming properties pass on the subject (check.py --source).
"""
import argparse
import json
import os
from pathlib import Path
import shlex
import subprocess
import tempfile

import check as regression

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
MODULE = "github.com/kgateway-dev/kgateway/v2/"
EMPTY_TREE = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
SUBJECTS = {
    "pr": "4635e171b1162672a7c0f55d1dc5d31c162e40e1",          # PR #14865 head, main 86f5499cea merged in
    "stack_base": "93002e65c9fac06ebcedcae1b619d5362648ba58",  # the stack's main base
    "stack_tip": "c38563730069be9340f067467d712f64c846ff13",   # stack #14817 tip
}
PACKAGES = ["pkg/kgateway/proxy_syncer", "pkg/kgateway/setup", "pkg/krtcollections", "api/settings", "pkg/kgateway/controller"]
ADAPTER = {
    "file": "pkg/kgateway/setup/cache_watch_policy_test.go",
    "sha256": "a467b56d36a9dcd76c9548457a0ff02f3d626f97d8f37aaba6e103b7e2993786",
    "before": "\tcase *perRequestSecretsCache:\n\t\tads, adsOK := openWatches(c.SnapshotCache, node)\n"
              "\t\tsecrets, secretsOK := openWatches(c.secrets, node)\n\t\treturn ads + secrets, adsOK || secretsOK\n",
    "after": "",
    "reason": "The PR replaces perRequestSecretsCache; its GetStatusInfo already sums every per-type cache.",
}
FIXTURE = {"file": "fixtures/pertype_policy_composed_test.go.txt", "target": "pkg/kgateway/setup/review_pertype_policy_test.go",
           "sha256": "2f093ee790e4347237062863b33b402cbfb2125fc09506f84a5e87f535a4b3dd"}
SCENARIOS = ["DeliversEndpointsWhileAClusterIsRejected", "DeliversEndpointsAfterARejectedClusterIsRemoved",
             "DeliversRoutesWhileAListenerIsRejected", "DeliversRoutesAfterARejectedListenerIsRemoved",
             "DeliversEndpointsToANewProxyWhileAClusterIsRejected", "DeliversRoutesToANewProxyWhileAListenerIsRejected"]
REQUIRED = {"pkg/kgateway/setup": [prefix + name for prefix in ("TestADSCache", "TestReviewPerTypePolicy") for name in SCENARIOS]
            + ["TestADSCacheAnswersAnEndpointRequestBetweenInstalls", "TestADSCacheAnswersARouteRequestBetweenInstalls"]}
MODES = ("default", "ordered")
PROFILES = ["stack-14817-c38563-backend-closure", "stack-14817-c38563-histories", "stack-14817-c38563-rewarming"]


def require(condition, message):
    if not condition:
        raise ValueError(message)


def overlay(changes, show, write, remove, merge):
    """Apply the stack's changes onto the PR tree. changes is [(status, path)];
    show(rev, path) returns bytes or None. Returns (taken, merged, conflicts)."""
    taken, merged, conflicts = [], [], []
    for status, path in changes:
        base, ours, theirs = show("stack_base", path), show("pr", path), show("stack_tip", path)
        if status == "D":
            if ours == base:
                remove(path)
                taken.append(path)
            elif ours is not None:
                conflicts.append(path)
            continue
        if ours == base or ours == theirs:
            write(path, theirs)
            taken.append(path)
            continue
        result, clean = merge(ours or b"", base or b"", theirs)
        write(path, result)
        (merged if clean else conflicts).append(path)
    return taken, merged, conflicts


def diff3(ours, base, theirs):
    with tempfile.TemporaryDirectory() as tmp:
        names = []
        for label, data in (("ours", ours), ("base", base), ("theirs", theirs)):
            path = Path(tmp) / label
            path.write_bytes(data)
            names.append(str(path))
        result = subprocess.run(["diff3", "-m", *names], capture_output=True)
        require(result.returncode in (0, 1), "diff3 failed")
        return result.stdout, result.returncode == 0


def compose(kgateway, destination):
    def git(*args):
        return subprocess.run(["git", "-C", str(kgateway), *args], capture_output=True, check=True)

    def show(subject, path):
        result = subprocess.run(["git", "-C", str(kgateway), "show", f"{SUBJECTS[subject]}:{path}"], capture_output=True)
        return result.stdout if result.returncode == 0 else None

    for subject in SUBJECTS.values():
        require(git("cat-file", "-t", subject).stdout.strip() == b"commit", f"missing commit {subject} in {kgateway}")
    for line in git("diff", "--raw", EMPTY_TREE, SUBJECTS["pr"]).stdout.decode().splitlines():
        meta, path = line.split("\t", 1)
        mode = meta.split()[1]
        if mode == "160000":
            continue
        target = destination / path
        target.parent.mkdir(parents=True, exist_ok=True)
        data = show("pr", path)
        if mode == "120000":
            os.symlink(data.decode(), target)
        else:
            target.write_bytes(data)
            if mode == "100755":
                target.chmod(0o755)
    changes = [tuple(line.split("\t", 1)) for line in
               git("diff", "--name-status", "--no-renames", SUBJECTS["stack_base"], SUBJECTS["stack_tip"]).stdout.decode().splitlines()]

    def write(path, data):
        (destination / path).parent.mkdir(parents=True, exist_ok=True)
        (destination / path).write_bytes(data)

    return overlay(changes, show, write, lambda path: (destination / path).unlink(), diff3)


def run_suite(source, artifacts):
    receipt = artifacts / "suite.jsonl"
    log = artifacts / "suite.log"
    packages = " ".join("./" + package for package in PACKAGES)
    command = ["make", "go-test", f"TEST_PKG={packages}", f"REPO_DIR={source}", "VERSION=v0.0.0-review",
               f"GOTESTSUM_ARGS=--format=short --jsonfile={shlex.quote(str(receipt))}"]
    with log.open("w") as stream:
        code = subprocess.run(command, cwd=source, stdout=stream, stderr=subprocess.STDOUT, timeout=3600).returncode
    events = [json.loads(line) for line in receipt.read_text().splitlines()] if receipt.exists() else []
    return code, events, log


def check_suite(code, events, log):
    """Every package passes, no test fails, and each required test passes in both ADS modes."""
    require(code == 0, f"five-package suite failed; inspect {log}")
    package_results = {e["Package"]: e["Action"] for e in events if not e.get("Test") and e.get("Action") in ("pass", "fail", "skip")}
    require(set(package_results) == {MODULE + package for package in PACKAGES}, "missing package result")
    require(set(package_results.values()) == {"pass"}, f"package failure: {package_results}")
    terminals = {}
    for event in events:
        if event.get("Test") and event.get("Action") in ("pass", "fail", "skip"):
            terminals.setdefault((event["Package"], event["Test"]), []).append(event["Action"])
    require(not any("fail" in actions for actions in terminals.values()), "a test failed")
    for package, tests in REQUIRED.items():
        for name in tests:
            for case in [name] + [f"{name}/{mode}" for mode in MODES]:
                require(terminals.get((MODULE + package, case)) == ["pass"], f"required test did not pass exactly once: {case}")
    return {"tests": sum(1 for (_, name) in terminals if "/" not in name),
            "skipped": sorted(f"{package}.{name}" for (package, name), actions in terminals.items() if actions == ["skip"])}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kgateway", type=Path, required=True, help="kgateway clone holding the three subject commits; read only")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="pertype-composition-"))).resolve()
    require(out != ROOT and ROOT not in out.parents, "artifacts must be outside the research repository")
    out.mkdir(parents=True, exist_ok=True)
    require(not any(out.iterdir()), "artifacts must be fresh")
    print(f"Per-type composition artifacts: {out}", flush=True)
    harness = {str(path.relative_to(ROOT)): regression.digest(path)
               for path in (HERE / "pertype_composition.py", HERE / "check.py", HERE / "profiles.json", HERE / FIXTURE["file"])}
    require(harness[str((HERE / FIXTURE["file"]).relative_to(ROOT))] == FIXTURE["sha256"], "pinned fixture changed")

    subject = out / "subject"
    taken, merged, conflicts = compose(args.kgateway.resolve(), subject)
    require(not conflicts, f"the stack does not apply onto the PR cleanly: {conflicts}")
    production = regression.production_digest(subject)
    (out / "subject.json").write_text(json.dumps({"subjects": SUBJECTS, "taken": len(taken), "merged": merged,
                                                  "production_sha256": production}, indent=2, sort_keys=True) + "\n")
    print(f"PASS compose: {len(taken)} stack files taken, {len(merged)} merged cleanly", flush=True)

    regression.apply_test_adapters(subject, [ADAPTER])
    (subject / FIXTURE["target"]).write_bytes((HERE / FIXTURE["file"]).read_bytes())
    suite = check_suite(*run_suite(subject, out))
    print(f"PASS suite: {suite['tests']} tests, {len(suite['skipped'])} skipped; PR and policy-composed scenarios pass in both ADS modes", flush=True)

    receipt = {"subjects": SUBJECTS, "merged": merged, "adapter": ADAPTER, "fixture": FIXTURE, "suite": suite,
               "harness_sha256": harness, "profiles": {},
               "limits": ["bounded Go tests with a mock ADS stream; no Envoy", "the subject is a composition, not a published revision",
                          "the PR's own validation and the TLA models are separate evidence"]}
    for name in PROFILES:
        directory = out / name
        command = ["python3", "-B", str(HERE / "check.py"), "--profile", name, "--source", str(subject),
                   "--expect-fixed", "--artifacts", str(directory)]
        with (out / f"{name}.log").open("w") as log:
            subprocess.run(command, stdout=log, stderr=subprocess.STDOUT, check=True)
        receipt["profiles"][name] = json.loads((directory / "receipt.json").read_text())
        print("PASS", name, flush=True)
    require(regression.production_digest(subject) == production, "production sources changed during verification")
    require(harness == {key: regression.digest(ROOT / key) for key in harness}, "harness sources changed during verification")
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print("PASS per-type composition;", out, flush=True)


if __name__ == "__main__":
    main()
