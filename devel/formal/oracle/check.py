#!/usr/bin/env python3
"""Build the native Lean oracle, check adapters, and fuzz actual Go code.

Only read-only Git commands are used. Production mutants use Go overlays in
the artifact directory; neither source files nor Git state are mutated.
"""
import argparse
import hashlib
import json
import os
from pathlib import Path
import subprocess
import tempfile
import time

ROOT = Path(__file__).resolve().parents[3]
PKG = "./pkg/kgateway/proxy_syncer"
PREFIX = "pkg/kgateway/proxy_syncer/"

MUTATIONS = {
    "contents-gate": (
        "perclient.go",
        "_, ok = endpoints[endpointResourceName]\n\t\tif ok {",
        "_, ok = endpoints[endpointResourceName]\n\t\tif ok && len(endpoints[endpointResourceName].Resource.(*envoyendpointv3.ClusterLoadAssignment).GetEndpoints()) > 0 {",
        "derived-empty",
    ),
    "overwrite-current-shared-assignment": (
        "kube_gw_translator_syncer.go",
        "if _, current := endpointItems[claName]; !current {",
        "if true {",
        "shared",
    ),
    "wrong-version-fold": ("eds_set_version.go", "v ^= h", "v += h", "filtered-version"),
    "equality-ignores-derivation": (
        "xdswrapper.go",
        "if p.proxyKey != in.proxyKey || p.deferred != in.deferred ||\n\t\t!slices.Equal(p.missingReferenced, in.missingReferenced) ||\n\t\t!slices.Equal(p.missingEndpointsReferenced, in.missingEndpointsReferenced) ||\n\t\t!slices.Equal(p.erroredClusters, in.erroredClusters) {",
        "if p.proxyKey != in.proxyKey || !slices.Equal(p.erroredClusters, in.erroredClusters) {",
        "equality",
    ),
}


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def run(command, log, env, commands, cwd=ROOT):
    print("Running:", " ".join(map(str, command)), flush=True)
    item = {"argv": list(map(str, command)), "cwd": str(cwd), "log": str(log), "start": time.time()}
    commands.append(item)
    with log.open("w") as out:
        p = subprocess.run(command, cwd=cwd, env=env, stdout=out, stderr=subprocess.STDOUT)
    item.update(exit_code=p.returncode, finish=time.time())
    return p.returncode


def check_unit_receipt(path):
    records = [json.loads(line) for line in path.read_text().splitlines()]
    passed = {r.get("Test") for r in records if r.get("Action") == "pass"}
    required = {"TestPublicationOracleExamples", "TestPublicationOracleKRT", "TestPublicationOracleRandomHistories", "TestPublicationOracleProtocol", "TestPublicationOracleVersionCollision", "TestPublicationOracleComparatorRejectsCorruption", "TestPublicationOracleCacheExamples", "TestPublicationOracleCacheProtocol"}
    if not required <= passed:
        raise RuntimeError(f"missing passing tests: {required - passed}")
    started = {r.get("Test") for r in records if r.get("Action") == "run"}
    if not required <= started or not started <= passed:
        raise RuntimeError("test execution/completion receipt missing")
    if any(r.get("Action") in ("skip", "fail") for r in records):
        raise RuntimeError("unit receipt contains skipped or failed tests")
    if not any(r.get("Action") == "pass" and "Test" not in r for r in records):
        raise RuntimeError("missing package completion")
    return len(passed)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--fuzztime", default="60s")
    parser.add_argument("--minimizetime", default="10s")
    parser.add_argument("--histories", type=int, default=1000)
    parser.add_argument("--parallel", type=int, default=2)
    parser.add_argument("--mutations", action="store_true")
    args = parser.parse_args()
    if args.histories <= 0 or args.parallel <= 0:
        parser.error("histories and parallel must be positive")
    artifacts = args.artifacts or Path(tempfile.mkdtemp(prefix="kxds-oracle-"))
    artifacts = artifacts.resolve()
    if artifacts == ROOT or ROOT in artifacts.parents:
        raise RuntimeError("artifacts must be outside the repository")
    if args.artifacts:
        artifacts.mkdir(parents=True, exist_ok=False)
    env = dict(os.environ, XDS_ORACLE_BIN=str(ROOT / "devel/formal/lean/.lake/build/bin/xdsoracle"), XDS_CACHE_ORACLE_BIN=str(ROOT / "devel/formal/lean/.lake/build/bin/xdscacheoracle"), XDS_ORACLE_HISTORIES=str(args.histories))
    env.setdefault("CGO_ENABLED", "0")
    receipt = {"schema": 1, "profiles": ["research-publication-v1", "research-cache-1cd122661-v1"], "start": time.time(), "commands": [], "mutations": {}, "parameters": {"histories": args.histories, "fuzztime_per_target": args.fuzztime, "minimizetime": args.minimizetime, "parallel": args.parallel, "mutations": args.mutations}}
    commands = receipt["commands"]
    receipt["head"] = subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=ROOT, text=True).strip()
    (artifacts / "working.diff").write_bytes(subprocess.check_output(["git", "diff", "HEAD"], cwd=ROOT))
    (artifacts / "status.txt").write_bytes(subprocess.check_output(["git", "status", "--short"], cwd=ROOT))
    sources = list((ROOT / "pkg/kgateway/proxy_syncer").glob("*.go"))
    sources += list((ROOT / "devel/formal/lean").rglob("*.lean"))
    sources += [p for p in (ROOT / "pkg/kgateway/proxy_syncer/testdata").rglob("*") if p.is_file()]
    sources += [ROOT / "go.mod", ROOT / "go.sum", ROOT / "Makefile", ROOT / "devel/formal/lean/lean-toolchain", ROOT / "devel/formal/lean/lakefile.toml", Path(__file__).resolve()]
    sources += [ROOT / "devel/formal/oracle/test_check.py", ROOT / "devel/formal/oracle/README.md", ROOT / ".github/workflows/formal-verification.yaml"]
    receipt["sources"] = {str(p.relative_to(ROOT)): digest(p) for p in sources if ".lake" not in p.parts}
    try:
        receipt["tools"] = {}
        for name, argv in {"go": ["go", "version"], "lean": ["lake", "env", "lean", "--version"], "python": ["python3", "--version"]}.items():
            receipt["tools"][name] = subprocess.check_output(argv, cwd=ROOT / "devel/formal/lean", env=env, text=True).strip()
        receipt["go_control_plane"] = subprocess.check_output(["go", "list", "-m", "github.com/envoyproxy/go-control-plane"], cwd=ROOT, env=env, text=True).strip()
        if receipt["go_control_plane"].split()[-1] != "v0.14.1-0.20260702184136-1cd1226616f5":
            raise RuntimeError("cache profile requires an explicit review for the changed dependency pin")
        command = ["lake", "build", "xdsoracle", "xdscacheoracle"]
        if run(command, artifacts / "lean-build.log", env, commands, ROOT / "devel/formal/lean"):
            raise RuntimeError("Lean build failed")
        receipt["oracle_sha256"] = digest(Path(env["XDS_ORACLE_BIN"]))
        receipt["cache_oracle_sha256"] = digest(Path(env["XDS_CACHE_ORACLE_BIN"]))
        if run(["python3", "-B", "-m", "unittest", "discover", "-s", "devel/formal/oracle", "-p", "test_*.py"], artifacts / "runner-tests.log", env, commands):
            raise RuntimeError("receipt checker tests failed")
        command = ["go", "test", "-tags", "e2e,formal_oracle", "-count=1", "-json", "-run", "^TestPublicationOracle", PKG]
        if run(command, artifacts / "unit.jsonl", env, commands):
            raise RuntimeError("differential unit/adapter checks failed")
        receipt["unit_cases"] = check_unit_receipt(artifacts / "unit.jsonl")
        if args.mutations:
            for name, (file, before, after, case) in MUTATIONS.items():
                source = ROOT / PREFIX / file
                original = source.read_text()
                if original.count(before) != 1:
                    raise RuntimeError(f"mutation {name} anchor is no longer unique")
                directory = artifacts / name
                directory.mkdir()
                mutated = directory / file
                mutated.write_text(original.replace(before, after))
                overlay = directory / "overlay.json"
                overlay.write_text(json.dumps({"Replace": {str(source): str(mutated)}}))
                command = ["go", "test", "-tags", "e2e,formal_oracle", "-count=1", "-json", "-overlay", str(overlay), "-run", f"^TestPublicationOracleExamples$/{case}$", PKG]
                code = run(command, directory / "test.jsonl", env, commands)
                output = (directory / "test.jsonl").read_text()
                records = [json.loads(line) for line in output.splitlines() if line.startswith("{")]
                expected_failure = any(r.get("Action") == "fail" and r.get("Test") == f"TestPublicationOracleExamples/{case}" for r in records)
                if code == 0 or not expected_failure or "differential mismatch:" not in output:
                    raise RuntimeError(f"mutation {name} did not fail at the required differential assertion")
                if source.read_text() != original:
                    raise RuntimeError("mutation changed source")
                receipt["mutations"][name] = {"detected": True, "overlay_sha256": digest(mutated)}
        receipt["fuzz_progress"] = {}
        for target in ["FuzzPublicationOracle", "FuzzPublicationCacheOracle"]:
            command = ["go", "test", "-tags", "e2e,formal_oracle", "-run", "^$", "-fuzz", f"^{target}$", f"-fuzztime={args.fuzztime}", f"-fuzzminimizetime={args.minimizetime}", f"-parallel={args.parallel}", PKG]
            log = artifacts / f"{target}.log"
            if run(command, log, env, commands):
                raise RuntimeError(f"{target} found a mismatch or harness failure; preserve the corpus and log")
            output = log.read_text()
            if "execs:" not in output or "PASS" not in output:
                raise RuntimeError("no completed fuzz execution receipt")
            receipt["fuzz_progress"][target] = [s for s in output.splitlines() if "execs:" in s]
        if not all(digest(ROOT / p) == h for p, h in receipt["sources"].items()):
            raise RuntimeError("sources changed during verification; rerun on a stable tree")
        receipt["outcome"] = "pass"
    except Exception as error:
        receipt["outcome"] = "fail"
        receipt["error"] = str(error)
        raise
    finally:
        receipt["finish"] = time.time()
        receipt["source_unchanged"] = all(digest(ROOT / p) == h for p, h in receipt["sources"].items())
        (artifacts / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n")
        print("Evidence:", artifacts, flush=True)


if __name__ == "__main__":
    main()
