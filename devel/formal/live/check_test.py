"""Boundary checks; each executed real history also runs capture mutation tests."""
import json
from pathlib import Path
import unittest

from check import validate
from run import ROOT, bridge_for, outside_repository


class LiveHarnessTest(unittest.TestCase):
    def test_empty_trace_cannot_pass(self):
        with self.assertRaisesRegex(ValueError, "empty trace"):
            validate([], Path("/tmp"), {}, "queued", "image", 1)

    def test_ack_is_not_an_application_observation(self):
        with self.assertRaisesRegex(ValueError, "unknown event kind"):
            validate([{"kind": "applied-because-ACK"}], Path("/tmp"), {}, "queued", "image", 1)

    def test_generated_output_is_outside_repository(self):
        self.assertFalse(outside_repository(ROOT))
        self.assertFalse(outside_repository(ROOT / "devel/formal/receipts/new-run"))
        self.assertTrue(outside_repository(Path("/tmp/formal-live")))

    def test_historical_bridge_preserves_old_policy(self):
        old, current = bridge_for("7d930"), bridge_for("8e7546")
        self.assertNotIn("g.currentBuild =", old)
        self.assertNotIn("gate.connectionReset =", old)
        self.assertIn("g.currentBuild =", current)
        self.assertIn("gate.connectionReset =", current)
        self.assertTrue(old.startswith("//go:build formal_live"))

    def test_profiles_keep_negative_and_repaired_subjects(self):
        p = json.loads((Path(__file__).parent / "profiles.json").read_text())
        self.assertEqual(p["subjects"]["7d930"]["expected"], "counterexamples")
        self.assertEqual(p["subjects"]["8e7546"]["expected"], "properties pass")
        self.assertTrue(all("@sha256:" in x["image"] for x in p["images"].values()))


if __name__ == "__main__":
    unittest.main()
