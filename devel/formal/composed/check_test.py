"""Mutation tests against a saved real capture, not fabricated success receipts."""
import json
from pathlib import Path
import shutil
import tempfile
import unittest

from check import EDS, RDS, digest, one, project, validate

FIXTURE = Path(__file__).parent / "fixtures/upstream-215ebe8"


class ComposedTraceTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name) / "capture"
        shutil.copytree(FIXTURE, self.directory)
        self.events = [json.loads(line) for line in (self.directory / "events.jsonl").read_text().splitlines()]

    def check(self):
        return validate(self.events, self.directory)

    def remove(self, target):
        self.events.remove(target)
        for seq, event in enumerate(self.events, 1):
            event["seq"] = seq

    def mutate_admin(self, phase, change):
        event = one(self.events, "observation", phase=phase)
        path = self.directory / event["config"]
        value = json.loads(path.read_text())
        change(value)
        path.write_text(json.dumps(value))
        event["config_sha256"] = digest(path)

    def test_real_capture_and_projection(self):
        self.assertEqual(self.check()["outcome"], "counterexample observed")
        self.assertEqual(project(self.events, self.directory), 13)
        rows = json.loads((self.directory / "projection.json").read_text())
        self.assertTrue(rows[5]["state"]["rollback"])
        self.assertEqual(rows[-1]["state"]["active"], 3)
        self.assertTrue(rows[-1]["state"]["checked"])

    def test_empty(self):
        self.events.clear()
        with self.assertRaisesRegex(ValueError, "empty trace"):
            self.check()

    def test_ack_cannot_replace_admin_observation(self):
        self.remove(one(self.events, "observation", phase="held-rollback"))
        with self.assertRaisesRegex(ValueError, "independent observations"):
            self.check()

    def test_cache_readback_cannot_replace_send(self):
        self.remove(one(self.events, "send-return", generation=2, type=RDS, version="old-a"))
        with self.assertRaisesRegex(ValueError, "incomplete send"):
            self.check()

    def test_failed_transport(self):
        one(self.events, "send-return", generation=2, type=RDS, version="old-a")["status"] = "context canceled"
        with self.assertRaisesRegex(ValueError, "transport failure"):
            self.check()

    def test_wrong_nonce_is_not_ack(self):
        request = next(e for e in self.events if e["kind"] == "request" and e["generation"] == 2 and e["type"] == RDS and e["nonce"])
        request["nonce"] = "unrelated"
        # Watch correlation fails before ACK correlation; both are required.
        with self.assertRaisesRegex(ValueError, "exactly one request"):
            self.check()

    def test_missing_watch_admission(self):
        self.remove(next(e for e in self.events if e["kind"] == "watch-call"))
        with self.assertRaisesRegex(ValueError, "watch admission"):
            self.check()

    def test_subscription_generation(self):
        next(e for e in self.events if e["kind"] == "request")["subscription"] = 2
        with self.assertRaisesRegex(ValueError, "subscription generation"):
            self.check()

    def test_source_provenance(self):
        one(self.events, "send-return", generation=2, type=RDS, version="old-a")["resources"] = ["different bytes"]
        with self.assertRaisesRegex(ValueError, "send correlation"):
            self.check()

    def test_candidate_versus_installed_version(self):
        one(self.events, "gate-state", action="still-held")["versions"][RDS] = "candidate-d"
        with self.assertRaisesRegex(ValueError, "episode/candidate/cache"):
            self.check()

    def test_same_rds_version_does_not_hide_an_eds_change(self):
        one(self.events, "gate-state", action="still-held")["versions"][EDS] = "different-eds"
        with self.assertRaisesRegex(ValueError, "gate version tuple"):
            self.check()

    def test_active_config_cannot_be_inferred_from_traffic(self):
        def change(dump):
            for c in dump["configs"]:
                for r in c.get("dynamic_route_configs", []):
                    r["version_info"] = "accepted-b"
        self.mutate_admin("held-rollback", change)
        with self.assertRaisesRegex(ValueError, "active route version"):
            self.check()

    def test_warming_is_not_active(self):
        def change(dump):
            for c in dump["configs"]:
                for listener in c.get("dynamic_listeners", []):
                    listener["warming_state"] = listener.pop("active_state")
        self.mutate_admin("held-rollback", change)
        with self.assertRaisesRegex(ValueError, "listener not active"):
            self.check()

    def test_unrelated_active_route_is_not_listener_application(self):
        def change(dump):
            for c in dump["configs"]:
                for listener in c.get("dynamic_listeners", []):
                    for chain in listener["active_state"]["listener"]["filter_chains"]:
                        for filt in chain["filters"]:
                            filt["typed_config"]["rds"]["route_config_name"] = "unrelated"
        self.mutate_admin("held-rollback", change)
        with self.assertRaisesRegex(ValueError, "does not consume observed RDS"):
            self.check()

    def test_stale_timer_must_preserve_current_cache(self):
        one(self.events, "gate-state", action="stale-ignored")["versions"][RDS] = "candidate-d"
        with self.assertRaisesRegex(ValueError, "episode/candidate/cache"):
            self.check()

    def test_terminal_failure(self):
        self.events[-1]["test_failed"] = True
        with self.assertRaisesRegex(ValueError, "failed driver"):
            self.check()

    def test_unknown_observation_claim(self):
        one(self.events, "observation", phase="held-rollback")["ack_means_applied"] = True
        with self.assertRaisesRegex(ValueError, "unknown or missing"):
            self.check()


if __name__ == "__main__":
    unittest.main()
