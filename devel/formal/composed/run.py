#!/usr/bin/env python3
"""Compose an immutable stack gate with real cache/server and pinned Envoy.

Passing this runner means the old-route counterexample and later recovery were
observed. It is never a clean bill of health for the stack or downstream.
"""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from check import digest, project, require, validate

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
JAR_HASH = "936a262061c914694dfd669a543be24573c45d5aa0ff20a8b96b23d01e050e88"


def regression_helpers():
    spec = importlib.util.spec_from_file_location("regression_helpers", HERE.parent / "regressions/check.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def tlc(jar, directory, config, expected=None, states=None):
    log = directory / f"{config}.log"
    with log.open("w") as stream:
        result = subprocess.run(["docker", "run", "--rm", "-v", f"{directory}:/model:ro",
                                 "-v", f"{jar}:/tla2tools.jar:ro", "-w", "/model",
                                 "eclipse-temurin:21-jre", "java", "-XX:+UseParallelGC", "-jar",
                                 "/tla2tools.jar", "-metadir", "/tmp/states", "-config",
                                 config + ".cfg", "HeldReconnectReplay.tla"],
                                stdout=stream, stderr=subprocess.STDOUT, timeout=120)
    output = log.read_text()
    if expected:
        require(result.returncode == 12 and f"Error: Invariant {expected} is violated." in output,
                f"wrong TLC negative-control outcome: {log}")
    else:
        require(result.returncode == 0 and "Model checking completed. No error has been found." in output,
                f"invalid observation projection: {log}")
        if states is not None:
            require(f"{states} distinct states found" in output, f"incomplete projection exploration: {log}")
    return {"exit": result.returncode, "invariant_failure": expected, "log_sha256": digest(log)}


def main():
    images = json.loads((HERE.parent / "runtime/profiles.json").read_text())["images"]
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", choices=list(images), action="append")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-composed-"))).resolve()
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        parser.error("artifacts must be empty; previous traces are never overwritten or reused")
    print(f"Composed artifacts: {out}", flush=True)
    reg = regression_helpers()
    profile = json.loads((HERE.parent / "regressions/profiles.json").read_text())["profiles"]["stack-14817-2026-10-16"]
    source = reg.materialize(profile, out)
    before = reg.production_digest(source)
    receipt = {"schema": 1, "revision": profile["revision"],
               "evidence_level": "controlled gate/cache/server + direct Envoy observations + bounded existential model replay",
               "desired_property": "NoHeldRollback", "expected": "counterexample",
               "fixture_sha256": digest(HERE / "held_reconnect_test.go.txt"),
               "adapter_sources": {p.name: digest(p) for p in HERE.glob("*.py")},
               "model_sha256": digest(HERE.parent / "tla/HeldReconnect.tla"),
               "archive_sha256": digest(out / "source.tar.gz"),
               "go_mod_sha256": digest(source / "go.mod"), "pristine_build_checks": [],
               "go_sum_sha256": digest(source / "go.sum"),
               "test_adapters": profile["test_adapters"], "profiles": {}}
    (out / "profile.json").write_text(json.dumps(receipt, indent=2) + "\n")
    for index, build in enumerate(profile["build_checks"]):
        code, _, log = reg.run_make(source, build["package"], "^$", out, f"pristine-build-{index}")
        reg.check_build_result(code, log.read_text(), build["error"])
        receipt["pristine_build_checks"].append({**build, "exit": code, "log_sha256": digest(log)})
    reg.apply_test_adapters(source, profile["test_adapters"])
    shutil.copyfile(HERE / "held_reconnect_test.go.txt", source / profile["package"] / "review_composed_test.go")
    jar = Path(os.environ.get("TLA2TOOLS_JAR", out / "tla2tools.jar")).resolve()
    if not jar.exists():
        subprocess.run(["curl", "-fsSL", "https://github.com/tlaplus/tlaplus/releases/download/v1.7.4/tla2tools.jar",
                        "-o", str(jar)], check=True, timeout=120)
    require(digest(jar) == JAR_HASH, "TLC artifact checksum mismatch")
    for name in args.profile or list(images):
        dest = out / name
        dest.mkdir()
        image = images[name]["image"]
        with (dest / "pull.log").open("w") as log:
            subprocess.run(["docker", "pull", image], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
        inspected = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
        (dest / "image.json").write_text(json.dumps(inspected, indent=2) + "\n")
        env = dict(os.environ, FORMAL_COMPOSED_OUT=str(dest), FORMAL_COMPOSED_IMAGE=image, CGO_ENABLED="0")
        with (dest / "test.log").open("w") as log:
            result = subprocess.run(["make", "go-test", "TEST_TAG=e2e", f"TEST_PKG=./{profile['package']}",
                                     "GO_TEST_ARGS=-timeout=3m -count=1",
                                     "GO_TEST_USER_ARGS=-run '^TestComposedHeldReconnect$$'",
                                     f"GOTESTSUM_ARGS=--format=short --jsonfile={reg.make_shell_arg(str(dest / 'test.jsonl'))}"],
                                    cwd=source, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=300)
        require(result.returncode == 0, f"driver failed, not a reproduced property failure: {dest / 'test.log'}")
        reg.check_receipts([json.loads(line) for line in (dest / "test.jsonl").read_text().splitlines()],
                           "github.com/kgateway-dev/kgateway/v2/" + profile["package"],
                           {"TestComposedHeldReconnect": ""}, "pass")
        events = [json.loads(line) for line in (dest / "events.jsonl").read_text().splitlines()]
        require(events[0]["image"] == image, "trace/image profile mismatch")
        record = validate(events, dest)
        record.update(image=image, image_id=inspected["Id"], architecture=inspected["Architecture"],
                      repo_digests=inspected["RepoDigests"], projection_states=project(events, dest))
        shutil.copyfile(HERE.parent / "tla/HeldReconnect.tla", dest / "HeldReconnect.tla")
        record["replay_valid"] = tlc(jar, dest, "replay-valid", states=record["projection_states"])
        record["replay_counterexample"] = tlc(jar, dest, "replay-counterexample", "ObservedNoRollback")
        # The same Step relation must reject an impossible observed transition.
        # Keep the mutation in a separate directory, preserving the actual trace.
        bad = dest / "projection-negative-control"
        bad.mkdir()
        for file in ("HeldReconnect.tla", "HeldReconnectReplay.tla", "replay-valid.cfg"):
            shutil.copyfile(dest / file, bad / file)
        path = bad / "HeldReconnectReplay.tla"
        text = path.read_text()
        require(text.count("candidate |-> 1") == 2, "projection shape changed")
        path.write_text(text.replace("candidate |-> 1", "candidate |-> 2", 1))
        record["invalid_projection_rejected"] = tlc(jar, bad, "replay-valid", "ReplayOK")
        record["artifacts"] = {str(p.relative_to(dest)): digest(p) for p in dest.rglob("*") if p.is_file()}
        receipt["profiles"][name] = record
        (out / "progress.json").write_text(json.dumps(receipt, indent=2) + "\n")
        print(f"PASS {name}: observed held rollback, latest expiry, recovery and stale-callback rejection; TLC replay checked", flush=True)
    require(reg.production_digest(source) == before, "production sources changed")
    require(digest(source / "go.mod") == receipt["go_mod_sha256"] and
            digest(source / "go.sum") == receipt["go_sum_sha256"], "subject dependencies changed")
    receipt["production_sources_unchanged"] = True
    receipt["production_source_sha256"] = hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest()
    receipt["outcome"] = "held-reconnect counterexample reproduced on every selected image"
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(f"PASS controlled composition; {out}", flush=True)


if __name__ == "__main__":
    main()
