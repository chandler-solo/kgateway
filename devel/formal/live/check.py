"""Check live publication histories using protocol, cache, admin and HTTP evidence.

This validates a controlled observation history, not a Go refinement theorem.
Server response callbacks, transport sends, ACKs and application stay distinct.
"""
import copy
import hashlib
import json
from pathlib import Path
import shutil
import tempfile

PREFIX = "type.googleapis.com/envoy.config."
CDS, EDS, LDS, RDS = [PREFIX + name for name in (
    "cluster.v3.Cluster", "endpoint.v3.ClusterLoadAssignment", "listener.v3.Listener", "route.v3.RouteConfiguration")]
SDS = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret"
TYPES = {CDS, EDS, LDS, RDS, SDS}
KEY = "kgateway-kube-gateway-api~formal-live~gw"
FIELDS = {
    "profile": "revision scenario image workers budget_ns carry_bound_ns setup auth ordered nack_policy reconnect_policy timer translation eds_cache fetch_timeout",
    "candidate": "controller version", "queued": "controller version", "event-begin": "controller version",
    "event-end": "controller version", "build-check": "controller generation", "replay-selected": "controller version",
    "reset-enter": "controller", "reset-excluded": "", "reclaim": "controller key cleared",
    "authenticated": "controller", "admitted-request": "controller key type version nonce",
    "handoff": "target", "stream-open": "controller generation", "stream-close": "controller generation",
    "request": "controller generation type version nonce names nack",
    "send-attempt": "controller generation type version nonce resources",
    "send-result": "controller generation type version nonce status",
    "install-attempt": "controller key versions resources",
    "install-result": "controller key version status readback_matches",
    "observation": "phase version config sha256 before after backend_code backend_body",
    "traffic": "code body status", "warm-confirmed": "controller key",
    "pending-confirmed": "pending carry_age_ms generation", "second-carry": "pending carry_age_ms generation",
    "captured-undrained": "version", "invoke-cancelled": "version", "end": "failed",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def one(events, kind, **match):
    found = [e for e in events if e["kind"] == kind and all(e.get(k) == v for k, v in match.items())]
    require(len(found) == 1, f"expected exactly one {kind} {match}")
    return found[0]


def application(event, directory):
    path = directory / event["config"]
    require(path.parent == directory and path.name == event["phase"] + "-config.json", "unsafe admin artifact path")
    require(digest(path) == event["sha256"], "changed admin artifact")
    configs = json.loads(path.read_text())["configs"]
    routes = [r for c in configs for r in c.get("dynamic_route_configs", []) if r["route_config"]["name"] == "routes"]
    require(len(routes) == 1 and routes[0]["version_info"] == event["version"], "active RDS version mismatch")
    rules = [r for v in routes[0]["route_config"]["virtual_hosts"] for r in v["routes"]]
    marker = [r["direct_response"].get("body", {}).get("inline_string") for r in rules
              if r.get("match", {}).get("prefix") == "/" and "direct_response" in r]
    require(marker == [event["version"]], "active route content mismatch")
    require(any(r.get("route", {}).get("cluster") == "a" for r in rules), "backend reference missing")
    listeners = [l for c in configs for l in c.get("dynamic_listeners", []) if l["name"] == "front"]
    require(len(listeners) == 1 and listeners[0].get("active_state") and not listeners[0].get("warming_state"), "listener is not active")
    filters = [f for chain in listeners[0]["active_state"]["listener"]["filter_chains"] for f in chain["filters"]]
    require(any(f.get("typed_config", {}).get("rds", {}).get("route_config_name") == "routes" for f in filters), "listener does not consume observed RDS")
    active = [c["cluster"]["name"] for cfg in configs for c in cfg.get("dynamic_active_clusters", [])]
    warming = [c["cluster"]["name"] for cfg in configs for c in cfg.get("dynamic_warming_clusters", [])]
    if event["backend_code"] == 200:
        require("a" in active and "a" not in warming and event["backend_body"] == "backend-a", "backend is not active and forwarding")
    else:
        require(event["backend_code"] == 503 and "a" not in active, "missing-cluster degradation was not observed")
    require(event["before"] == event["after"] == event["version"], "HTTP does not corroborate route application")


def validate(events, directory, subject, scenario, image, workers):
    require(bool(events), "empty trace")
    last_time = -1
    for seq, event in enumerate(events, 1):
        kind = event.get("kind")
        require(kind in FIELDS, "unknown event kind")
        require(set(event) == set(FIELDS[kind].split()) | {"schema", "seq", "kind", "elapsed_ns"}, "unknown or missing event fields")
        require(event["schema"] == 1 and event["seq"] == seq and type(event["elapsed_ns"]) is int
                and event["elapsed_ns"] >= last_time, "invalid sequence or monotonic time")
        last_time = event["elapsed_ns"]
        if "key" in event:
            require(event["key"] == KEY, "authenticated key mismatch")
        if "controller" in event:
            require(event["controller"] in ("A", "B"), "unknown controller")
    require(events[0]["kind"] == "profile" and events[-1]["kind"] == "end", "missing trace boundaries")
    p = one(events, "profile")
    require(p["revision"] == subject["revision"] and p["scenario"] == scenario and p["image"] == image
            and p["workers"] == workers, "source/runtime/scenario mismatch")
    require(p["setup"] == "NewControlPlane" and p["ordered"] is True and p["nack_policy"] is True
            and p["reconnect_policy"] is True and p["eds_cache"] is True and p["fetch_timeout"] == "unset"
            and p["timer"] == "elapsed time.AfterFunc" and p["translation"] == "supplied snapshots"
            and p["auth"] == "KubeJWTAuthenticator with fixture TokenReview authority", "unrecognized composition assumptions")
    require(p["budget_ns"] == 1200000000 and p["carry_bound_ns"] == 1400000000, "changed timer profile")
    bootstrap = json.loads((directory / "bootstrap.json").read_text())
    for parent in ("cds_config", "lds_config"):
        require(bootstrap["dynamic_resources"][parent].get("initial_fetch_timeout") == "0s",
                "CDS/LDS timeout must be explicitly disabled")
    local_name = bootstrap["cluster_manager"]["local_cluster_name"]
    local = [c for c in bootstrap["static_resources"]["clusters"] if c["name"] == local_name]
    require(len(local) == 1 and local[0]["eds_cluster_config"]["eds_config"].get("initial_fetch_timeout") == "1s",
            "rendered local EDS timeout mismatch")
    require(bootstrap["node"]["metadata"]["role"] == "deliberately-untrusted", "authentication was not challenged by an untrusted role")
    services = bootstrap["dynamic_resources"]["ads_config"]["grpc_services"]
    require(len(services) == 1 and services[0]["initial_metadata"] ==
            [{"key": "authorization", "value": "Bearer formal-live-token"}], "wrong ADS credential fixture")
    runtime_layers = bootstrap["layered_runtime"]["layers"]
    require(any(layer.get("static_layer", {}).get("envoy.restart_features.use_eds_cache_for_ads") is True
                for layer in runtime_layers), "EDS cache bootstrap mismatch")
    require(one(events, "end")["failed"] is False, "driver failed")
    opens = [e for e in events if e["kind"] == "stream-open"]
    require([(e["generation"], e["controller"]) for e in opens] == [(1, "B"), (2, "A")], "wrong reconnect history")
    for start in opens:
        gen, controller = start["generation"], start["controller"]
        end = one(events, "stream-close", generation=gen)
        require(end["controller"] == controller and start["seq"] < end["seq"], "invalid stream lifetime")
        require(any(e["kind"] == "authenticated" and e["controller"] == controller for e in events), "authentication unexercised")
        admitted = [e for e in events if e["kind"] == "admitted-request" and e["controller"] == controller]
        require(bool(admitted), "admission callbacks unexercised")
        requests = [e for e in events if e["kind"] == "request" and e["generation"] == gen]
        require(bool(requests) and all(e["nack"] is False for e in requests), "unexpected NACK or no requests")
        require(all(start["seq"] < r["seq"] < end["seq"] for r in requests), "request outside stream")
    warm = one(events, "warm-confirmed")
    require(warm["controller"] == "A", "wrong warm client")
    require(any(e["kind"] == "admitted-request" and e["controller"] == "A" and e["type"] == LDS
                and e["nonce"] == "" and e["version"] != "" for e in events), "returning LDS version not observed")
    installs = [e for e in events if e["kind"] == "install-attempt"]
    results = [e for e in events if e["kind"] == "install-result"]
    require(len(installs) == len(results), "missing cache completion")
    baseline = next((e["resources"][EDS].get("a") for e in installs if e["controller"] == "B"), None)
    require(bool(baseline), "missing ready-assignment baseline")
    for attempt, result in zip(installs, results):
        require(set(attempt["versions"]) == TYPES and set(attempt["resources"]) == TYPES, "incomplete resource/version tuple")
        # Independent expectation for this one-assignment fixture: ready and
        # carried A contribute 17, while an omitted assignment contributes 0.
        # This catches an artificial version push masking an equal-version gap.
        expected_eds = "17" if set(attempt["resources"][EDS]) == {"a"} else "0"
        require(set(attempt["resources"][EDS]) in (set(), {"a"}) and attempt["versions"][EDS] == expected_eds,
                "EDS version does not match the selected fixture contribution")
        require("a" not in attempt["resources"][EDS] or attempt["resources"][EDS]["a"] == baseline,
                "ready/carried assignment bytes changed at the same fixture contribution")
        require(attempt["seq"] < result["seq"] and attempt["controller"] == result["controller"]
                and attempt["versions"][RDS] == result["version"] and result["status"] == "ok"
                and result["readback_matches"] is True, "cache installation not independently read back")
    sends = [e for e in events if e["kind"] == "send-attempt"]
    require(len(sends) == len([e for e in events if e["kind"] == "send-result"]), "missing transport result")
    for send in sends:
        end = one(events, "send-result", generation=send["generation"], type=send["type"], nonce=send["nonce"])
        require(send["seq"] < end["seq"] and send["controller"] == end["controller"] and send["version"] == end["version"], "transport correlation mismatch")
        require(any(a["seq"] < send["seq"] and a["controller"] == send["controller"]
                    and a["versions"][send["type"]] == send["version"]
                    and sorted(a["resources"][send["type"]].values()) == sorted(send["resources"]) for a in installs), "response has no resource provenance")
    for request in (e for e in events if e["kind"] == "request" and e["nonce"]):
        send = one(events, "send-attempt", generation=request["generation"], type=request["type"], nonce=request["nonce"])
        require(send["seq"] < request["seq"] and send["version"] == request["version"], "ACK/nonce correlation mismatch")
    negative = subject["expected"] == "counterexamples"
    phases = {
        "queued": ["accepted-b", "rollback" if negative else "natural-budget", "recovered"],
        "carry": ["accepted-b", "connected-a", "carry-first", "coherent"] +
                 (["premature-drop"] if negative else ["fresh-carry", "fresh-carry-stable", "natural-carry-expiry"]) + ["recovered"],
        "reset": ["accepted-b", "no-leaked-timer", "recovered"],
        "cancel": ["accepted-b", "cancelled-before-expiry", "after-cancelled-callback"],
    }[scenario]
    observations = [e for e in events if e["kind"] == "observation"]
    require([e["phase"] for e in observations] == phases, "missing or reordered independent application observations")
    phase_versions = {"accepted-b": "accepted-b", "connected-a": "old-a", "carry-first": "carry-first",
                      "coherent": "coherent", "fresh-carry": "carry-second", "fresh-carry-stable": "carry-second",
                      "natural-carry-expiry": "carry-second", "premature-drop": "carry-second",
                      "natural-budget": "candidate-d", "rollback": "queued-old", "recovered": "recovered",
                      "no-leaked-timer": "accepted-b", "cancelled-before-expiry": "recovered",
                      "after-cancelled-callback": "recovered"}
    for obs in observations:
        require(obs["version"] == phase_versions[obs["phase"]], "wrong application phase version")
        require(obs["backend_code"] == (503 if obs["phase"] in ("natural-carry-expiry", "premature-drop") else 200), "wrong application phase backend outcome")
        application(obs, directory)
        matching = [e for e in sends if e["type"] == RDS and e["version"] == obs["version"] and e["seq"] < obs["seq"]]
        require(bool(matching), "application observation has no preceding RDS send")
        require(any(one(events, "send-result", generation=s["generation"], type=RDS, nonce=s["nonce"])["status"] == "ok" for s in matching), "application has no successful transport witness")
    traffic = [e for e in events if e["kind"] == "traffic"]
    require(len(traffic) >= 3 and all(e["code"] == 200 and e["status"] == "ok" for e in traffic), "traffic sampling missing or failed")
    if not negative:
        forbidden = {"queued-old", "old-a"} if scenario != "carry" else set()
        require(not any(e["controller"] == "A" and e["type"] == RDS and e["version"] in forbidden for e in sends), "obsolete configuration sent")
        require(not any(e["body"] == "queued-old" for e in traffic), "obsolete configuration served")
    if scenario in ("queued", "reset", "cancel"):
        queued = one(events, "queued", controller="A", version="queued-old")
        begin = one(events, "event-begin", controller="A", version="queued-old")
        done = one(events, "event-end", controller="A", version="queued-old")
        require(queued["seq"] < begin["seq"] < done["seq"], "queued publisher schedule missing")
    if scenario == "queued":
        pending = one(events, "pending-confirmed")
        require(pending["pending"] == "candidate-d", "wrong first-publish episode")
        target = "queued-old" if negative else "candidate-d"
        applied = one(events, "observation", phase="rollback" if negative else "natural-budget")
        require(applied["version"] == target and done["seq"] < applied["seq"], "wrong queued-publication outcome")
        if not negative:
            # After the blocked queue is released it may legitimately publish
            # the same row again. The FIRST installation must be the timer's.
            publish = next(e for e in installs if e["controller"] == "A" and e["versions"][RDS] == target)
            require(publish["elapsed_ns"] - pending["elapsed_ns"] >= p["budget_ns"] - 100000000, "timer did not elapse")
            event = one(events, "event-begin", controller="A", version="candidate-d")
            require(publish["seq"] < event["seq"], "KRT callback substituted for natural timer expiry")
    if scenario == "carry":
        second = one(events, "second-carry")
        offered = one(events, "candidate", controller="A", version="carry-second")
        if negative:
            require(second["carry_age_ms"] == -1, "old carry bug did not drop the episode")
        else:
            require(0 <= second["carry_age_ms"] < 300, "recovery failed to start a fresh carry episode")
            expiry = one(events, "observation", phase="natural-carry-expiry")
            drops = [e for e in installs if e["seq"] > offered["seq"] and e["controller"] == "A" and e["versions"][RDS] == "carry-second" and "a" not in e["resources"][CDS]]
            require(len(drops) == 1 and drops[0]["elapsed_ns"] - offered["elapsed_ns"] >= p["carry_bound_ns"] - 100000000
                    and drops[0]["seq"] < expiry["seq"], "natural carry expiry missing or premature")
    if scenario == "reset":
        reset = one(events, "reset-enter")
        excluded = one(events, "reset-excluded")
        selected = one(events, "replay-selected")
        held = one(events, "observation", phase="no-leaked-timer")
        require(reset["seq"] < begin["seq"] < excluded["seq"] < selected["seq"] < held["seq"], "reset/adoption schedule missing")
        require(held["version"] == "accepted-b" and held["elapsed_ns"] - selected["elapsed_ns"] > p["budget_ns"], "timer-leak observation window too short")
        require(not any(e["controller"] == "A" and e["versions"][RDS] == "candidate-d" for e in installs), "superseded replay installed")
    if scenario == "cancel":
        captured, invoked = one(events, "captured-undrained"), one(events, "invoke-cancelled")
        coherent = one(events, "observation", phase="cancelled-before-expiry")
        after = one(events, "observation", phase="after-cancelled-callback")
        require(captured["seq"] < coherent["seq"] < invoked["seq"] < after["seq"], "undrained cancellation schedule missing")
        require(coherent["elapsed_ns"] - captured["elapsed_ns"] < p["budget_ns"], "timer payload was not cancelled before expiry")
        require(not any(e["controller"] == "A" and e["versions"][RDS] == "candidate-d" for e in installs), "cancelled timer installed its payload")
    return {"outcome": "counterexample observed" if negative else "properties pass", "events": len(events),
            "observations": len(observations), "traffic_samples": len(traffic), "scenario": scenario}


def mutation_checks(events, directory, subject, scenario, image, workers):
    """Mutate each fresh real capture; no generated logs become source fixtures."""
    passed = []
    targets = [
        ("missing application", lambda es: es.remove(next(e for e in es if e["kind"] == "observation"))),
        ("missing authentication", lambda es: es.__setitem__(slice(None), [e for e in es if e["kind"] != "authenticated"])),
        ("missing warm history", lambda es: es.remove(one(es, "warm-confirmed"))),
        ("wrong authenticated identity", lambda es: next(e for e in es if e["kind"] == "admitted-request").update(key="forged")),
        ("wrong nonce", lambda es: next(e for e in es if e["kind"] == "request" and e["nonce"]).update(nonce="unrelated")),
        ("missing transport", lambda es: es.remove(next(e for e in es if e["kind"] == "send-result"))),
        ("wrong response bytes", lambda es: next(e for e in es if e["kind"] == "send-attempt").update(resources=["wrong"])),
        ("artificial EDS version", lambda es: next(e for e in es if e["kind"] == "install-attempt")["versions"].update({EDS: "artificial-push"})),
        ("changed carried assignment", lambda es: next(e for e in es if e["kind"] == "install-attempt" and e["controller"] == "A")["resources"][EDS].update(a="changed-at-the-same-version")),
        ("wrong admin hash", lambda es: next(e for e in es if e["kind"] == "observation").update(sha256="wrong")),
        ("wrong HTTP result", lambda es: next(e for e in es if e["kind"] == "observation").update(after="wrong")),
        ("failed terminal", lambda es: es[-1].update(failed=True)),
    ]
    for name, mutate in targets:
        changed = copy.deepcopy(events)
        mutate(changed)
        for seq, event in enumerate(changed, 1):
            event["seq"] = seq
        try:
            validate(changed, directory, subject, scenario, image, workers)
        except ValueError:
            passed.append(name)
        else:
            raise ValueError(f"checker accepted mutation: {name}")
    with tempfile.TemporaryDirectory(prefix="xds-live-checker-") as tmp:
        dest = Path(tmp)
        for path in directory.glob("*-config.json"):
            shutil.copyfile(path, dest / path.name)
        shutil.copyfile(directory / "bootstrap.json", dest / "bootstrap.json")
        changed = copy.deepcopy(events)
        obs = next(e for e in changed if e["kind"] == "observation")
        path = dest / obs["config"]
        dump = json.loads(path.read_text())
        for cfg in dump["configs"]:
            for route in cfg.get("dynamic_route_configs", []):
                route["route_config"]["virtual_hosts"][0]["routes"][-1]["direct_response"]["body"]["inline_string"] = "wrong"
        path.write_text(json.dumps(dump))
        obs["sha256"] = digest(path)
        try:
            validate(changed, dest, subject, scenario, image, workers)
        except ValueError as error:
            require("active route content" in str(error), "admin mutation failed for unrelated reason")
            passed.append("changed active route despite matching hash")
        else:
            raise ValueError("checker inferred application from version/HTTP alone")
        # A semantically wrong bootstrap must fail even with intact observations.
        shutil.copyfile(directory / "bootstrap.json", dest / "bootstrap.json")
        bootstrap = json.loads((dest / "bootstrap.json").read_text())
        bootstrap["dynamic_resources"]["cds_config"].pop("initial_fetch_timeout")
        (dest / "bootstrap.json").write_text(json.dumps(bootstrap))
        try:
            validate(events, dest, subject, scenario, image, workers)
        except ValueError as error:
            require("CDS/LDS" in str(error), "bootstrap mutation failed for unrelated reason")
            passed.append("unset CDS bootstrap timeout")
        else:
            raise ValueError("checker accepted a synthetic finite CDS bootstrap timeout")
    return passed
