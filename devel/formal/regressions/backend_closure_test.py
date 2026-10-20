import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import backend_closure as closure


class BackendClosureTests(unittest.TestCase):
    def data(self, fixed=False):
        source_cluster = {"name": "tunneled", "loadAssignment": {"endpoints": [{"lbEndpoints": [{"endpoint": {"address": {"pipe": {"path": "@fixture"}}}}]}]}}
        active_cluster = {"name": "tunneled", "load_assignment": {"endpoints": [{"lb_endpoints": [{"endpoint": {"address": {"pipe": {"path": "@fixture"}}}}]}]}}
        listener = {"name": "tunnel_tunneled", "address": {"pipe": {"path": "@fixture"}}, "filter_chains": [{"filters": [{"typed_config": {"cluster": "proxy"}}]}]}
        front = {"name": "listener", "filter_chains": [{"filters": [{"typed_config": {"rds": {"route_config_name": "route-config"}}}]}]}
        initial = {closure.CDS: {"versionInfo": "c1", "resources": [source_cluster, {"name": "proxy"}]}, closure.LDS: {"versionInfo": "l1", "resources": [listener, front]}}
        carried = copy.deepcopy(initial)
        carried[closure.CDS]["versionInfo"] = "carry"
        if not fixed:
            carried[closure.LDS] = {"versionInfo": "l2", "resources": [front]}
        def admin(phase):
            owned = phase == "initial" or fixed
            return {"configs": [
                {"@type": "ClustersConfigDump", "version_info": "c1" if phase == "initial" else "carry", "dynamic_active_clusters": [{"cluster": active_cluster}, {"cluster": {"name": "proxy"}}]},
                {"@type": "ListenersConfigDump", "version_info": "l1" if owned else "l2", "dynamic_listeners": [{"active_state": {"listener": x}} for x in ([front, listener] if owned else [front])]},
                {"@type": "RoutesConfigDump", "dynamic_route_configs": [{"route_config": {"name": "route-config", "virtual_hosts": [{"routes": [{"route": {"cluster": "tunneled"}}]}]}}]},
            ]}
        observed = {"mode": "ALL", "baseline_code": 200, "after_codes": [200]*3 if fixed else [503]*3, "after_body": "owned-backend-fixture" if fixed else "upstream remote connection failure", "owned_listener_active": fixed, "carried_cluster_active": True}
        container = {"Image": closure.IMAGE, "Args": ["--concurrency", "2", "--drain-time-s", "0"]}
        bootstrap = {"node": {"id": "review-owned-backend"}, "dynamic_resources": {"cds_config": {"initial_fetch_timeout": "0s"}, "lds_config": {"initial_fetch_timeout": "0s"}}}
        received = "\n".join("gRPC config for "+url+" accepted with 2 resources with version "+carried[url]["versionInfo"] for url in (closure.CDS, closure.LDS))
        return [initial, carried, admin("initial"), admin("carried"), observed, container, bootstrap, received]

    def test_accepts_counterexample_and_fixed_observations_separately(self):
        closure.validate_live(self.data())
        closure.validate_live(self.data(True), fixed=True)
        for data, fixed in ((self.data(), True), (self.data(True), False)):
            with self.subTest(fixed=fixed), self.assertRaises(ValueError):
                closure.validate_live(data, fixed)

    def test_capture_mutations_are_rejected(self):
        self.assertEqual(11, closure.mutation_checks(self.data()))
        self.assertEqual(11, closure.mutation_checks(self.data(True), fixed=True))

    def test_unchanged_cds_can_keep_its_previous_admin_version(self):
        data = self.data()
        data[3]["configs"][0]["version_info"] = "c1"
        closure.validate_live(data)
        data[1][closure.CDS]["resources"].append({"name": "new-content"})
        with self.assertRaises(ValueError):
            closure.validate_live(data)

    def test_route_and_active_listener_are_required(self):
        for which in ("listener", "route"):
            data = self.data()
            if which == "listener":
                data[3]["configs"][1]["dynamic_listeners"][0]["active_state"]["listener"]["filter_chains"][0]["filters"][0]["typed_config"]["rds"]["route_config_name"] = "wrong"
            else:
                data[3]["configs"][2]["dynamic_route_configs"][0]["route_config"]["virtual_hosts"][0]["routes"][0]["route"]["cluster"] = "wrong"
            with self.subTest(which=which), self.assertRaises(ValueError):
                closure.validate_live(data)

    def test_preserves_existing_artifacts(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory) / "keep.json"
            marker.write_text(json.dumps({"owner": "user"}))
            with patch("sys.argv", ["backend_closure.py", "--artifacts", directory]), patch("backend_closure.subprocess.run") as run, self.assertRaises(ValueError):
                closure.main()
            run.assert_not_called()
            self.assertEqual({"owner": "user"}, json.loads(marker.read_text()))

    def test_artifacts_cannot_be_inside_candidate(self):
        with tempfile.TemporaryDirectory() as directory:
            artifacts = Path(directory) / "artifacts"
            with patch("sys.argv", ["backend_closure.py", "--source", directory, "--expect-fixed", "--artifacts", str(artifacts)]), patch("backend_closure.subprocess.run") as run, self.assertRaises(ValueError):
                closure.main()
            run.assert_not_called()
            self.assertFalse(artifacts.exists())


if __name__ == "__main__":
    unittest.main()
