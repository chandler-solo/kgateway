#!/usr/bin/env python3
"""Check the October 20 backend-resource change on immutable source profiles.

All downloads, fixture injection, Go execution and captures use fresh scratch
directories. The optional candidate is read-only. No Git state is changed.
"""
import argparse
import copy
import hashlib
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile

import check as regression

HERE = Path(__file__).resolve().parent
ROOT = HERE.parents[2]
CDS = "type.googleapis.com/envoy.config.cluster.v3.Cluster"
LDS = "type.googleapis.com/envoy.config.listener.v3.Listener"
IMAGE = "sha256:66612e482b84c51de5d64c1f530c42e1ab458082b53a56a0303803d1b6a112d3"
PROFILES = {
    "before": "stack-14817-before-backend-listeners",
    "pr14717": "stack-14817-2fe5-backend-closure",
    "tip": "stack-14817-e9436e-backend-closure",
    "histories": "stack-14817-e9436e-histories",
    "rewarming": "stack-14817-e9436e-rewarming",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def resources(snapshot, type_url):
    return {item["name"]: item for item in snapshot[type_url].get("resources", [])}


def active(admin, kind):
    out = {}
    for config in admin["configs"]:
        if kind == "cluster":
            for row in config.get("dynamic_active_clusters", []):
                out[row["cluster"]["name"]] = row["cluster"]
        elif kind == "listener":
            for row in config.get("dynamic_listeners", []):
                listener = row.get("active_state", {}).get("listener")
                if listener:
                    out[listener["name"]] = listener
        else:
            for row in config.get("dynamic_route_configs", []):
                out[row["route_config"]["name"]] = row["route_config"]
    return out


def pipe(cluster, source=False):
    assignment = cluster["loadAssignment" if source else "load_assignment"]
    locality = assignment["endpoints"][0]
    endpoint = locality["lbEndpoints" if source else "lb_endpoints"][0]["endpoint"]
    return endpoint["address"]["pipe"]["path"]


def validate_live(data, fixed=False):
    initial, carried, before, after, observed, container, bootstrap, received = data
    initial_clusters, carried_clusters = resources(initial, CDS), resources(carried, CDS)
    require(carried_clusters["tunneled"] == initial_clusters["tunneled"], "the carried cluster changed")
    before_clusters, after_clusters = active(before, "cluster"), active(after, "cluster")
    before_listeners, after_listeners = active(before, "listener"), active(after, "listener")
    require("tunneled" in before_clusters and "tunneled" in after_clusters, "missing active carried cluster")
    expected_pipe = pipe(initial_clusters["tunneled"], source=True)
    require(pipe(before_clusters["tunneled"]) == pipe(after_clusters["tunneled"]) == expected_pipe, "active cluster no longer uses its captured pipe")
    require(before_listeners["tunnel_tunneled"]["address"]["pipe"]["path"] == expected_pipe, "baseline owned socket mismatch")
    tcp = before_listeners["tunnel_tunneled"]["filter_chains"][0]["filters"][0]["typed_config"]
    require(tcp["cluster"] == "proxy" and "proxy" in before_clusters, "baseline proxy dependency missing")
    require(observed["baseline_code"] == 200, "baseline traffic did not succeed")
    for admin in (before, after):
        front = active(admin, "listener")["listener"]
        hcm = front["filter_chains"][0]["filters"][0]["typed_config"]
        require(hcm["rds"]["route_config_name"] == "route-config", "the active front listener stopped using the captured RDS")
        route = active(admin, "route")["route-config"]
        require(any(r.get("route", {}).get("cluster") == "tunneled" for host in route["virtual_hosts"] for r in host["routes"]), "the route stopped naming the carried backend")
    for url, kind in ((CDS, "Clusters"), (LDS, "Listeners")):
        versions = [c.get("version_info") for c in after["configs"] if c["@type"].endswith(kind+"ConfigDump")]
        version = carried[url]["versionInfo"]
        require(any("gRPC config for "+url+" accepted with " in line and line.endswith(" with version "+version) for line in received.splitlines()), "Envoy did not record the captured response version")
        if versions != [version]:
            require(url == CDS and versions == [initial[url]["versionInfo"]]
                    and resources(initial, url) == resources(carried, url),
                    "admin version is stale for changed resource content")
    require(container["Image"] == IMAGE, "wrong runtime image")
    args = container["Args"]
    require(args[args.index("--concurrency")+1] == "2", "wrong worker count")
    require(args[args.index("--drain-time-s")+1] == "0", "wrong drain profile")
    require(bootstrap["node"]["id"] == "review-owned-backend", "wrong runtime identity")
    for field in ("cds_config", "lds_config"):
        require(bootstrap["dynamic_resources"][field]["initial_fetch_timeout"] == "0s", "parent timeout changed")
    owned = "tunnel_tunneled" in after_listeners
    require(observed["owned_listener_active"] == owned and observed["carried_cluster_active"], "observation disagrees with admin")
    if fixed:
        require(owned and "proxy" in after_clusters, "candidate dropped a carried dependency")
        require(observed["after_codes"] == [200, 200, 200] and observed["after_body"] == "owned-backend-fixture", "candidate did not preserve forwarding")
    else:
        require(not owned and "tunnel_tunneled" not in resources(carried, LDS), "counterexample did not remove the owned listener")
        require(observed["after_codes"] == [503, 503, 503] and "remote connection failure" in observed["after_body"], "wrong failure or absent traffic evidence")
        require(("proxy" in after_clusters) == (observed["mode"] == "ALL"), "wrong mode-specific proxy closure")


def load_live(captures, mode):
    source = captures / ("TestReviewBackendCarryKeepsListener" + mode)
    runtime = captures / ("live-" + mode)
    paths = [source / "initial.json", source / "carried.json", runtime / "initial-admin.json",
             runtime / "carried-admin.json", runtime / "observation.json", runtime / "container.json", runtime / "bootstrap.json"]
    values = [json.loads(path.read_text()) for path in paths]
    values[5] = values[5][0]
    paths.append(runtime / "received.log")
    values.append(paths[-1].read_text())
    return values, {str(path.relative_to(captures)): regression.digest(path) for path in paths}


def mutation_checks(data, fixed=False):
    mutations = []
    for index, mutate in (
        (4, lambda d: d.update(baseline_code=503)),
        (4, lambda d: d.update(after_codes=[200, 200, 200] if not fixed else [503, 503, 503])),
        (4, lambda d: d.update(owned_listener_active=not d["owned_listener_active"])),
        (5, lambda d: d.update(Image="wrong-image")),
        (6, lambda d: d["node"].update(id="wrong-node")),
        (2, lambda d: d.update(configs=[])),
        (3, lambda d: d.update(configs=[])),
        (1, lambda d: resources(d, CDS)["tunneled"].update(loadAssignment={})),
        (4, lambda d: d.update(after_body="unrelated response")),
        (6, lambda d: d["dynamic_resources"]["cds_config"].update(initial_fetch_timeout="15s")),
    ):
        corrupted = copy.deepcopy(data)
        mutate(corrupted[index])
        try:
            validate_live(corrupted, fixed)
        except (ValueError, KeyError, IndexError):
            mutations.append(index)
        else:
            raise ValueError("live checker accepted a corrupted capture")
    corrupted = copy.deepcopy(data)
    corrupted[7] = "unrelated Envoy output"
    try:
        validate_live(corrupted, fixed)
    except ValueError:
        mutations.append(7)
    else:
        raise ValueError("live checker accepted absent runtime receipt evidence")
    return len(mutations)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path)
    parser.add_argument("--source", type=Path, help="read-only repaired candidate")
    parser.add_argument("--expect-fixed", action="store_true")
    parser.add_argument("--no-live", action="store_true", help="explicitly omit private enterprise Envoy replay")
    args = parser.parse_args()
    if bool(args.source) != args.expect_fixed:
        parser.error("candidate runs require both --source and --expect-fixed")
    out = (args.artifacts or Path(tempfile.mkdtemp(prefix="backend-closure-"))).resolve()
    require(out != ROOT and ROOT not in out.parents, "artifacts must be outside the research repository")
    if args.source:
        require(args.source.resolve() != out and args.source.resolve() not in out.parents, "artifacts must be outside the candidate")
    out.mkdir(parents=True, exist_ok=True)
    require(not any(out.iterdir()), "artifacts must be fresh")
    print(f"Backend closure artifacts: {out}", flush=True)
    harness_paths = [HERE / "backend_closure.py", HERE / "check.py", HERE / "profiles.json",
                     HERE / "fixtures/backend_resource_carry_test.go.txt",
                     HERE / "fixtures/backend_emission_graph_test.go.txt",
                     HERE / "fixtures/backend_resource_carry_live_test.go.txt"]
    harness_hashes = {str(path.relative_to(ROOT)): regression.digest(path) for path in harness_paths}
    captures = out / "captures"
    receipt = {"profiles": {}, "harness_sha256": harness_hashes, "live": "explicitly omitted" if args.no_live else {},
               "evidence_level": "pinned Go/KRT/publisher checks" + ("; real-Envoy replay omitted" if args.no_live else " plus separate real-Envoy snapshot replay"),
               "limits": ["fixture translated backend rows", "runtime replays captured snapshots", "no full Kubernetes/downstream deployment", "no TLS/FIPS or live CONNECT-header scenario", "two workers, zero drain, fixture upstream closes connections"]}
    for label, name in PROFILES.items():
        directory = out / label
        command = ["python3", "-B", str(HERE / "check.py"), "--profile", name, "--artifacts", str(directory)]
        if args.source and label in ("tip", "histories", "rewarming"):
            command += ["--source", str(args.source.resolve()), "--expect-fixed"]
        env = dict(os.environ)
        if label == "tip":
            env["REVIEW_BACKEND_OUT"] = str(captures)
        with (out / (label+".log")).open("w") as log:
            subprocess.run(command, env=env, stdout=log, stderr=subprocess.STDOUT, check=True)
        receipt["profiles"][label] = json.loads((directory / "receipt.json").read_text())
        print("PASS", label, flush=True)
    if not args.no_live:
        source = out / "tip/source"
        fixture = HERE / "fixtures/backend_resource_carry_live_test.go.txt"
        target = source / "pkg/kgateway/setup/review_backend_live_test.go"
        require(not target.exists(), "live fixture would overwrite source")
        shutil.copyfile(fixture, target)
        before = regression.production_digest(source)
        old = os.environ.get("REVIEW_BACKEND_OUT")
        os.environ["REVIEW_BACKEND_OUT"] = str(captures)
        try:
            code, events, log = regression.run_make(source, "pkg/kgateway/setup", "^TestReviewBackendCarryLive", out, "live")
        finally:
            if old is None:
                os.environ.pop("REVIEW_BACKEND_OUT", None)
            else:
                os.environ["REVIEW_BACKEND_OUT"] = old
        expected = "pass" if args.expect_fixed else "fail"
        require((code == 0) == args.expect_fixed, f"unexpected live Make result; inspect {log}")
        regression.check_receipts([json.loads(line) for line in events.read_text().splitlines()],
            "github.com/kgateway-dev/kgateway/v2/pkg/kgateway/setup",
            {"TestReviewBackendCarryLive"+mode: "carrying a live backend must preserve forwarding through its owned listener" for mode in ("ALL", "REFERENCED")}, expected)
        require(regression.production_digest(source) == before, "original production sources changed during live replay")
        for mode in ("ALL", "REFERENCED"):
            data, hashes = load_live(captures, mode)
            validate_live(data, args.expect_fixed)
            receipt["live"][mode] = {"observation": data[4], "capture_sha256": hashes,
                "mutations_rejected": mutation_checks(data, args.expect_fixed)}
        receipt["live_fixture_sha256"] = regression.digest(fixture)
    require(harness_hashes == {str(path.relative_to(ROOT)): regression.digest(path) for path in harness_paths},
            "harness sources changed during verification")
    (out / "receipt.json").write_text(json.dumps(receipt, indent=2)+"\n")
    print("PASS designated backend-closure outcomes;", out, flush=True)


if __name__ == "__main__":
    main()
