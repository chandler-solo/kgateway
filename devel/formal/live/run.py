#!/usr/bin/env python3
"""Run pinned historical and repaired publication histories through real Envoy."""
import argparse
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

from check import digest, mutation_checks, require, validate

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]


def helpers():
    spec = importlib.util.spec_from_file_location("live_regression_helpers", HERE.parent / "regressions/check.py")
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def outside_repository(path):
    return path != ROOT and ROOT not in path.parents


def bridge_for(subject):
    text = (HERE / "bridge.go.txt").read_text()
    if subject == "7d930":
        # Test-bridge API compatibility only. Original subject files are not edited.
        for region in ("CURRENT_BUILD", "RESET"):
            start, end = f"\t// BEGIN_{region}", f"\t// END_{region}\n"
            require(text.count(start) == text.count(end) == 1, "test bridge marker changed")
            left, right = text.index(start), text.index(end) + len(end)
            text = text[:left] + text[right:]
    return text


def check_original_sources(reg, source, before):
    current = reg.production_digest(source)
    bridge = current.pop("pkg/kgateway/proxy_syncer/formal_live_bridge.go", None)
    require(bridge is not None and current == before, "an original production Go file changed")
    return bridge


def main():
    profiles = json.loads((HERE / "profiles.json").read_text())
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--profile", choices=list(profiles["images"]), action="append")
    parser.add_argument("--subject", choices=list(profiles["subjects"]), action="append")
    parser.add_argument("--workers", type=int, choices=(1, 2), action="append")
    parser.add_argument("--artifacts", type=Path)
    args = parser.parse_args()
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="xds-live-"))).resolve()
    if not outside_repository(out):
        parser.error("generated artifacts must be outside the research repository")
    out.mkdir(parents=True, exist_ok=True)
    if any(out.iterdir()):
        parser.error("artifacts must be fresh; existing evidence is never reused")
    print(f"Live publication artifacts: {out}", flush=True)
    renderer = out / "bootstrap-renderer"
    subprocess.run(["make", "formal-envoyprobe-build", f"FORMAL_ENVOYPROBE_BIN={renderer}"], cwd=ROOT, check=True)
    reg = helpers()
    receipt = {"schema": 1, "evidence_level": "real control-plane setup/callbacks/KRT/gate/timers/gRPC + independent Envoy admin/HTTP",
               "limits": ["supplied translated snapshots", "fixture Kubernetes TokenReview authority",
                          "one client key/proxy, two control planes", "plaintext local test transport",
                          "no complete controller or deployed-system refinement proof"],
               "tools": {name: subprocess.check_output(command, cwd=ROOT, text=True).strip()
                         for name, command in {"helm": ["helm", "version", "--short"], "go": ["go", "version"]}.items()},
               "profiles_sha256": digest(HERE / "profiles.json"),
               "render_values_sha256": digest(HERE.parent / "timeouts/render-values.yaml"),
               "fixture_sha256": {p.name: digest(p) for p in HERE.glob("*.txt")}, "subjects": {}, "images": {}, "runs": []}
    receipt["runner_sources_sha256"] = {p.name: digest(p) for p in HERE.glob("*.py")}
    sources = {}
    for name in args.subject or profiles["subjects"]:
        subject = profiles["subjects"][name]
        dest = out / name
        dest.mkdir()
        source = reg.materialize(subject, dest)
        rendered = dest / "rendered-bootstrap.json"
        subprocess.run([str(renderer), "-render-chart", str(source / "pkg/kgateway/helm/envoy"),
                        "-render-values", str(HERE.parent / "timeouts/render-values.yaml"),
                        "-render-output", str(rendered)], check=True, timeout=60)
        before = reg.production_digest(source)
        dependencies = {p: digest(source / p) for p in ("go.mod", "go.sum")}
        code, _, log = reg.run_make(source, "pkg/kgateway/proxy_syncer ./pkg/kgateway/setup", "^$", dest, "pristine-build")
        reg.check_build_result(code, log.read_text())
        bridge = source / "pkg/kgateway/proxy_syncer/formal_live_bridge.go"
        require(not bridge.exists(), "bridge would overwrite subject source")
        bridge.write_text(bridge_for(name))
        target = source / "pkg/kgateway/setup/formal_live_test.go"
        require(not target.exists(), "driver would overwrite subject tests")
        shutil.copyfile(HERE / "publication_test.go.txt", target)
        bootstrap_helper = source / "pkg/kgateway/setup/formal_live_bootstrap_test.go"
        require(not bootstrap_helper.exists(), "bootstrap helper would overwrite subject tests")
        shutil.copyfile(HERE / "bootstrap.go.txt", bootstrap_helper)
        receipt["subjects"][name] = {**subject, "archive_sha256": digest(dest / "source.tar.gz"),
                                    "dependencies": dependencies, "original_production_sha256": hashlib.sha256(json.dumps(before, sort_keys=True).encode()).hexdigest(),
                                    "additional_bridge_sha256": check_original_sources(reg, source, before), "pristine_compile": "pass"}
        receipt["subjects"][name]["bootstrap_template_sha256"] = digest(source / "pkg/kgateway/helm/envoy/templates/configmap.yaml")
        receipt["subjects"][name]["rendered_bootstrap_sha256"] = digest(rendered)
        sources[name] = source, before, dependencies
    for name in args.profile or profiles["images"]:
        image = profiles["images"][name]["image"]
        with (out / f"{name}-pull.log").open("w") as log:
            subprocess.run(["docker", "pull", image], stdout=log, stderr=subprocess.STDOUT, check=True, timeout=240)
        inspected = json.loads(subprocess.check_output(["docker", "image", "inspect", image]))[0]
        receipt["images"][name] = {"requested": image, "id": inspected["Id"], "repo_digests": inspected["RepoDigests"], "architecture": inspected["Architecture"]}
        for workers in args.workers or [1, 2]:
            for subject_name, (source, _, _) in sources.items():
                subject = profiles["subjects"][subject_name]
                for scenario in subject["scenarios"]:
                    dest = out / subject_name / name / f"workers-{workers}" / scenario
                    dest.mkdir(parents=True)
                    selector = f"^TestFormalLivePublication$/^{scenario}$"
                    command = ["make", "go-test", "TEST_TAG=e2e,formal_live", "TEST_PKG=./pkg/kgateway/setup",
                               "GO_TEST_ARGS=-timeout=2m -count=1 -race", f"GO_TEST_USER_ARGS=-run {reg.make_shell_arg(selector)}",
                               f"GOTESTSUM_ARGS=--format=short --jsonfile={reg.make_shell_arg(str(dest / 'test.jsonl'))}"]
                    env = dict(os.environ, CGO_ENABLED="1", FORMAL_LIVE_OUT=str(dest), FORMAL_LIVE_IMAGE=image,
                               FORMAL_LIVE_SUBJECT=subject_name, FORMAL_LIVE_REVISION=subject["revision"], FORMAL_LIVE_WORKERS=str(workers),
                               FORMAL_LIVE_BOOTSTRAP=str(out / subject_name / "rendered-bootstrap.json"))
                    with (dest / "test.log").open("w") as log:
                        result = subprocess.run(command, cwd=source, env=env, stdout=log, stderr=subprocess.STDOUT, timeout=240)
                    require(result.returncode == 0, f"driver failure is not a counterexample: {dest / 'test.log'}")
                    names = {"TestFormalLivePublication": "", f"TestFormalLivePublication/{scenario}": ""}
                    reg.check_receipts([json.loads(line) for line in (dest / "test.jsonl").read_text().splitlines()],
                                       "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/setup", names, "pass")
                    events = [json.loads(line) for line in (dest / "events.jsonl").read_text().splitlines()]
                    record = validate(events, dest, subject, scenario, image, workers)
                    record.update(subject=subject_name, profile=name, workers=workers,
                                  mutation_checks=mutation_checks(events, dest, subject, scenario, image, workers),
                                  artifacts={p.name: digest(p) for p in dest.iterdir() if p.is_file()})
                    receipt["runs"].append(record)
                    (out / "progress.json").write_text(json.dumps(receipt, indent=2) + "\n")
                    print(f"PASS {subject_name}/{name}/workers-{workers}/{scenario}: {record['outcome']}; admin, traffic, transport and checker mutations verified", flush=True)
    for name, (source, before, dependencies) in sources.items():
        require(check_original_sources(reg, source, before) == receipt["subjects"][name]["additional_bridge_sha256"], "test bridge changed during execution")
        require(all(digest(source / p) == value for p, value in dependencies.items()), "subject dependencies changed")
    receipt["original_production_sources_unchanged"] = True
    receipt["outcome"] = "every selected history matched its expected observation"
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
    print(f"PASS live publication histories; {out}", flush=True)


if __name__ == "__main__":
    main()
