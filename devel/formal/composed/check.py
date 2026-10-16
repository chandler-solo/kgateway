"""Validate a composed observation trace and emit an existential TLC projection.

This is deliberately separate from schema-1 cache/mock lifecycle traces. A
successful Send is not an application observation. Publication completion and
Send may overlap; the projection orders those intervals using the successful
readback and matching response as witnesses, rather than claiming to record
the cache's internal installation instant or every implementation transition.
"""
import hashlib
import json
from pathlib import Path

PIN = "215ebe8e645080748dbe37727ab2f6f2faa33de6"
PREFIX = "type.googleapis.com/envoy.config."
CDS, EDS, LDS, RDS = [PREFIX + s for s in (
    "cluster.v3.Cluster", "endpoint.v3.ClusterLoadAssignment", "listener.v3.Listener",
    "route.v3.RouteConfiguration")]
# Secret is in extensions.transport_sockets, unlike the other four types.
SDS = "type.googleapis.com/envoy.extensions.transport_sockets.tls.v3.Secret"
TYPES = {CDS, EDS, LDS, RDS, SDS}
PHASES = {"accepted-b": (1, "accepted-b"), "held-before-send": (2, "accepted-b"),
          "held-rollback": (2, "old-a"), "budget-latest": (2, "candidate-d"),
          "coherent-recovery": (2, "recovered"), "after-stale-timer": (2, "recovered")}
FIELDS = {
    "profile": "revision image budget timer_driver server warm_state workers",
    "publication-attempt": "controller versions resources",
    "publication-result": "controller versions status",
    "cache-readback": "controller versions resources",
    "watch-call": "controller type version nonce names",
    "watch-return": "controller type cancel_returned status",
    "stream-open": "controller generation", "stream-close": "controller generation",
    "request": "controller generation request subscription type version nonce names node nack",
    "send-attempt": "controller generation type version nonce resources",
    "send-delayed": "controller generation type version nonce resources",
    "send-return": "controller generation type version nonce resources status",
    "observation": "phase generation version config config_sha256 http_before http_after http_status",
    "gate-state": "action controller candidate pending_candidate timer_episode published_on_connection versions",
    "handoff": "from to generation", "release-send": "generation",
    "timer-invoke": "timer_episode action", "end": "test_failed",
}


def require(condition, message):
    if not condition:
        raise ValueError(message)


def digest(path):
    return hashlib.sha256(path.read_bytes()).hexdigest()


def one(events, kind, **match):
    found = [e for e in events if e["kind"] == kind and all(e.get(k) == v for k, v in match.items())]
    require(len(found) == 1, f"expected exactly one {kind} {match}, got {len(found)}")
    return found[0]


def observe(event, directory):
    """Parse independently fetched admin data; never substitute ACK evidence."""
    path = directory / event["config"]
    require(path.parent == directory and path.name == event["phase"] + "-config.json", "unsafe observation path")
    require(digest(path) == event["config_sha256"], "changed admin artifact")
    configs = json.loads(path.read_text())["configs"]
    routes = [r for c in configs for r in c.get("dynamic_route_configs", [])
              if r["route_config"]["name"] == "routes"]
    require(len(routes) == 1 and routes[0]["version_info"] == event["version"], "active route version mismatch")
    rules = [r for v in routes[0]["route_config"]["virtual_hosts"] for r in v["routes"]]
    markers = [r["direct_response"].get("body", {}).get("inline_string") for r in rules
               if r.get("match", {}).get("prefix") == "/" and "direct_response" in r]
    require(markers == [event["version"]], "active route content mismatch")
    require(any(r.get("route", {}).get("cluster") == "a" for r in rules), "missing backend reference")
    listeners = [l for c in configs for l in c.get("dynamic_listeners", []) if l["name"] == "front"]
    require(len(listeners) == 1 and listeners[0].get("active_state", {}).get("listener", {}).get("name") == "front", "listener not active")
    require(not listeners[0].get("warming_state"), "unexpected warming listener")
    filters = [f for chain in listeners[0]["active_state"]["listener"]["filter_chains"] for f in chain["filters"]]
    require(any(f.get("typed_config", {}).get("rds", {}).get("route_config_name") == "routes"
                and f.get("name") == "envoy.filters.network.http_connection_manager" for f in filters),
            "active listener does not consume observed RDS")
    clusters = [c["cluster"]["name"] for cfg in configs for c in cfg.get("dynamic_active_clusters", [])]
    warming = [c["cluster"]["name"] for cfg in configs for c in cfg.get("dynamic_warming_clusters", [])]
    require("a" in clusters and "a" not in warming, "expected active, non-warming cluster a")
    require(event["http_status"] == 200 and event["http_before"] == event["http_after"] == event["version"], "traffic does not corroborate route application")


def validate(events, directory):
    require(bool(events), "empty trace")
    for index, e in enumerate(events, 1):
        require(e.get("kind") in FIELDS, "unknown event kind")
        require(set(e) == set(FIELDS[e["kind"]].split()) | {"schema", "seq", "kind", "key"}, "unknown or missing event fields")
        require(e["schema"] == 1 and e["seq"] == index and e["key"] == "held-reconnect", "trace identity/order mismatch")
        if "versions" in e:
            require(set(e["versions"]) == TYPES and all(isinstance(v, str) and v for v in e["versions"].values()), "incomplete version tuple")
        if "type" in e:
            require(e["type"] in TYPES, "unexpected resource type")
        if "generation" in e:
            require(type(e["generation"]) is int and e["generation"] in (1, 2), "unknown stream generation")
            if "controller" in e:
                require(e["controller"] == {1: "B", 2: "A"}[e["generation"]], "stream/controller mismatch")
        if "status" in e:
            require(e["status"] == "ok", "unexpected publication/watch/transport failure")
    require(events[0]["kind"] == "profile" and events[-1]["kind"] == "end", "missing trace boundaries")
    profile = one(events, "profile")
    require(profile["revision"] == PIN and "@sha256:" in profile["image"] and profile["workers"] == 1, "unrecognized source/runtime profile")
    require(profile["budget"] == "1h" and profile["timer_driver"] == "direct callback, no elapsed-time claim" and
            profile["server"] == "raw GCP SotW ADS, unordered" and profile["warm_state"] == "stubPriorXDS(true)", "changed controlled assumptions")
    require(one(events, "end")["test_failed"] is False, "failed driver")
    for generation, controller in ((1, "B"), (2, "A")):
        start = one(events, "stream-open", generation=generation)
        end = one(events, "stream-close", generation=generation)
        require(start["controller"] == end["controller"] == controller and start["seq"] < end["seq"], "stream ownership mismatch")
        requests = [e for e in events if e["kind"] == "request" and e["generation"] == generation]
        subscriptions = {}
        require(bool(requests), "unexercised stream")
        for index, r in enumerate(requests, 1):
            require(r["request"] == index and start["seq"] < r["seq"] < end["seq"] and r["controller"] == controller, "request lifecycle mismatch")
            require(r["node"] in ("", "held-reconnect") and r["nack"] is False, "unexpected identity or NACK")
            names = tuple(sorted(r["names"] or []))
            previous, number = subscriptions.get(r["type"], (None, 0))
            number += names != previous
            require(r["subscription"] == number, "subscription generation mismatch")
            subscriptions[r["type"]] = names, number
        require(requests[0]["node"] == "held-reconnect", "first request lacks client identity")
    require(len([e for e in events if e["kind"] == "stream-open"]) == 2, "unexpected reconnect")
    calls = [e for e in events if e["kind"] == "watch-call"]
    returns = [e for e in events if e["kind"] == "watch-return"]
    requests = [e for e in events if e["kind"] == "request"]
    require(len(calls) == len(returns) == len(requests), "incomplete watch admission observations")
    for call, result in zip(calls, returns):
        request = one(events, "request", **{k: call[k] for k in ("controller", "type", "version", "nonce", "names")})
        require(request["seq"] < call["seq"] < result["seq"] and
                (call["controller"], call["type"]) == (result["controller"], result["type"]), "watch/request correlation mismatch")
    attempts = [e for e in events if e["kind"] == "publication-attempt"]
    require([(e["controller"], e["versions"][RDS]) for e in attempts] ==
            [("A", "old-a"), ("B", "accepted-b"), ("A", "candidate-d"), ("A", "recovered")], "unexpected installation schedule")
    require(len([e for e in events if e["kind"] == "cache-readback"]) == 4 and
            len([e for e in events if e["kind"] == "publication-result"]) == 4, "missing/extra publication completion")
    for attempt in attempts:
        result = one(events, "publication-result", controller=attempt["controller"], versions=attempt["versions"])
        readback = one(events, "cache-readback", controller=attempt["controller"], versions=attempt["versions"])
        require(attempt["seq"] < result["seq"] < readback["seq"] and readback["resources"] == attempt["resources"], "publication readback mismatch")
        require(set(attempt["resources"]) == TYPES, "incomplete resource digest tuple")
    # Underived CLA is omitted at expiry. Derived empty is present in recovery.
    require(attempts[2]["resources"][EDS] == {} and set(attempts[3]["resources"][EDS]) == {"a"}, "underived/derived-empty policy changed")
    sends = [e for e in events if e["kind"] == "send-return"]
    require(len(sends) == len([e for e in events if e["kind"] == "send-attempt"]), "incomplete send")
    for send in sends:
        start = one(events, "send-attempt", generation=send["generation"], type=send["type"], nonce=send["nonce"])
        require(one(events, "stream-open", generation=send["generation"])["seq"] < start["seq"] and
                send["seq"] < one(events, "stream-close", generation=send["generation"])["seq"], "send outside stream lifetime")
        require(start["seq"] < send["seq"] and all(start[k] == send[k] for k in ("controller", "version", "resources")) and bool(send["nonce"]), "send correlation mismatch")
        require(any(a["controller"] == send["controller"] and a["seq"] < start["seq"] and
                    a["versions"][send["type"]] == send["version"] and
                    sorted(a["resources"][send["type"]].values()) == sorted(send["resources"]) for a in attempts), "response has no matching published resource provenance")
        require(any(r["kind"] == "request" and r["generation"] == send["generation"] and r["type"] == send["type"] and r["seq"] < start["seq"] for r in events), "unsolicited response")
    for r in (e for e in events if e["kind"] == "request" and e["nonce"]):
        send = one(sends, "send-return", generation=r["generation"], type=r["type"], nonce=r["nonce"])
        require(r["version"] == send["version"], "ACK version mismatch")
        require(one(events, "send-attempt", generation=r["generation"], type=r["type"], nonce=r["nonce"])["seq"] < r["seq"], "ACK precedes response attempt")
    observations = [e for e in events if e["kind"] == "observation"]
    require([e["phase"] for e in observations] == list(PHASES), "missing/extra/out-of-order independent observations")
    for e in observations:
        require((e["generation"], e["version"]) == PHASES[e["phase"]], "unexpected phase result")
        observe(e, directory)
        source_gen = 1 if e["version"] == "accepted-b" else 2
        send = one(sends, "send-return", generation=source_gen, type=RDS, version=e["version"])
        require(send["seq"] < e["seq"], "application witness precedes send completion")
        require(any(r["kind"] == "request" and r["generation"] == source_gen and r["type"] == RDS and r["nonce"] == send["nonce"] for r in events), "missing correlated RDS ACK (separate from application)")
    gates = [e for e in events if e["kind"] == "gate-state"]
    expected = [("departed", "", "", 0, False, "old-a"),
                ("offer", "candidate-c", "candidate-c", 1, False, "old-a"),
                ("replace", "candidate-d", "candidate-d", 1, False, "old-a"),
                ("still-held", "candidate-d", "candidate-d", 1, False, "old-a"),
                ("expired", "candidate-d", "", 1, True, "candidate-d"),
                ("coherent", "recovered", "", 0, True, "recovered"),
                ("stale-ignored", "recovered", "", 0, True, "recovered")]
    require([(g["action"], g["candidate"], g["pending_candidate"], g["timer_episode"], g["published_on_connection"], g["versions"][RDS]) for g in gates] == expected, "gate episode/candidate/cache mismatch")
    for gate in gates:
        readbacks = [e for e in events if e["kind"] == "cache-readback" and e["controller"] == "A" and e["seq"] < gate["seq"]]
        require(gate["controller"] == "A" and bool(readbacks) and gate["versions"] == readbacks[-1]["versions"], "gate version tuple differs from installed cache")
    require(observations[0]["seq"] < gates[1]["seq"], "candidate offered before accepted B baseline")
    delay = one(events, "send-delayed")
    release = one(events, "release-send")
    require(delay["generation"] == 2 and delay["type"] == RDS and delay["version"] == "old-a", "wrong response delayed")
    require(release["generation"] == 2, "wrong stream released")
    delayed_start = one(events, "send-attempt", generation=2, type=RDS, nonce=delay["nonce"])
    delayed_end = one(events, "send-return", generation=2, type=RDS, nonce=delay["nonce"])
    require(delayed_start["seq"] < delay["seq"] < release["seq"] < delayed_end["seq"], "delay does not bracket actual Send")
    require(gates[1]["seq"] < one(events, "handoff")["seq"] < one(events, "stream-open", generation=2)["seq"] < delay["seq"] < gates[2]["seq"] < observations[1]["seq"] < release["seq"] < observations[2]["seq"] < gates[3]["seq"], "held-send causal schedule mismatch")
    require(one(events, "handoff")["from"] == "B" and one(events, "handoff")["to"] == "A" and one(events, "handoff")["generation"] == 2, "wrong handoff")
    request = next(r for r in events if r["kind"] == "request" and r["generation"] == 2 and r["type"] == RDS)
    require(request["version"] == "accepted-b" and request["nonce"] == "", "reconnect did not carry accepted B version without nonce")
    require([(e["action"], e["timer_episode"]) for e in events if e["kind"] == "timer-invoke"] == [("expire", 1), ("stale", 1)], "missing timer schedule")
    require(gates[3]["seq"] < one(events, "timer-invoke", action="expire")["seq"] < attempts[2]["seq"] < gates[4]["seq"] < observations[3]["seq"] < attempts[3]["seq"] < gates[5]["seq"] < observations[4]["seq"] < one(events, "timer-invoke", action="stale")["seq"] < gates[6]["seq"] < observations[5]["seq"], "timer/recovery causal schedule mismatch")
    return {"events": len(events), "observations": len(observations), "streams": 2,
            "desired_property": "NoHeldRollback", "outcome": "counterexample observed",
            "expiry": "latest underived candidate installed; CLA omitted",
            "stale_callback": "ignored after coherent recovery"}


def project(events, out):
    """Emit milestones plus evidence indices; TLC checks the SAME Step relation.

    This existential interval ordering is not a verified refinement map. The
    detailed checker above is the trust boundary for the observation adapter.
    """
    state = dict(candidate=0, cache=0, generation=1, held=False, timer=0, sent=0,
                 active=1, rollback=False, checked=False, timerRollback=False)
    rows = [{"state": state.copy(), "evidence_seq": one(events, "observation", phase="accepted-b")["seq"]}]
    def step(action, witness, **changes):
        state.update(changes)
        rows.append({"action": action, "evidence_seq": witness["seq"], "state": state.copy()})
    step("Offer", one(events, "gate-state", action="offer"), candidate=1, held=True, timer=1)
    step("Reconnect", one(events, "stream-open", generation=2), generation=2)
    step("Replace", one(events, "gate-state", action="replace"), candidate=2)
    step("SendOld", one(events, "send-return", generation=2, type=RDS, version="old-a"), sent=1)
    step("ApplyOld", one(events, "observation", phase="held-rollback"), active=0, rollback=True)
    step("Expire", one(events, "gate-state", action="expired"), cache=2, held=False, timer=2)
    step("SendLatest", one(events, "send-return", generation=2, type=RDS, version="candidate-d"), sent=2)
    step("ApplyLatest", one(events, "observation", phase="budget-latest"), active=2)
    step("Recover", one(events, "gate-state", action="coherent"), cache=3, timer=0)
    step("SendLatest", one(events, "send-return", generation=2, type=RDS, version="recovered"), sent=3)
    step("ApplyLatest", one(events, "observation", phase="coherent-recovery"), active=3)
    step("StaleTimer", one(events, "gate-state", action="stale-ignored"), checked=True)
    (out / "projection.json").write_text(json.dumps(rows, indent=2) + "\n")
    def tla(value):
        return str(value).upper() if isinstance(value, bool) else str(value)
    states = ",\n".join("[" + ", ".join(f"{k} |-> {tla(v)}" for k, v in row["state"].items()) + "]" for row in rows)
    actions = ", ".join(json.dumps(row["action"]) for row in rows[1:])
    (out / "HeldReconnectReplay.tla").write_text(
        "---- MODULE HeldReconnectReplay ----\nEXTENDS HeldReconnect\n"
        f"Observed == <<{states}>>\nObservedActions == <<{actions}>>\n"
        "VARIABLE cursor\nReplayInit == /\\ cursor = 1 /\\ state = Observed[1]\n"
        "ReplayNext == /\\ cursor < Len(Observed) /\\ cursor' = cursor + 1\n"
        "              /\\ state' = Observed[cursor+1]\n"
        "ReplaySpec == ReplayInit /\\ [][ReplayNext]_<<cursor, state>>\n"
        "ReplayOK == IF cursor = 1 THEN Observed[1] = Initial ELSE\n"
        "  Step(Observed[cursor-1], Observed[cursor], ObservedActions[cursor-1])\n"
        "ObservedNoRollback == ~Observed[cursor].rollback\n====\n")
    for name, extra in (("valid", ""), ("counterexample", "INVARIANT ObservedNoRollback\n")):
        (out / f"replay-{name}.cfg").write_text('CONSTANT Mode = "OldCache"\nSPECIFICATION ReplaySpec\nCHECK_DEADLOCK FALSE\nINVARIANT ReplayOK\n' + extra)
    return len(rows)
