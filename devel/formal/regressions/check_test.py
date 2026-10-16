import contextlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from check import check_receipts, main


class ReceiptTests(unittest.TestCase):
    def events(self, action="fail", output="expected property"):
        return [
            {"Package": "pkg", "Test": "TestProperty", "Action": "run"},
            {"Package": "pkg", "Test": "TestProperty", "Action": "output", "Output": output},
            {"Package": "pkg", "Test": "TestProperty", "Action": action},
            {"Package": "pkg", "Action": action},
        ]

    def check(self, events, expected="fail"):
        check_receipts(events, "pkg", {"TestProperty": "expected property"}, expected)

    def test_expected_assertion(self):
        self.check(self.events())

    def test_fixed_candidate(self):
        self.check(self.events("pass", ""), "pass")

    def test_rejects_vacuous_or_wrong_failures(self):
        for events in [[], self.events()[1:], self.events()[:-1],
                       self.events("skip"), self.events(output="different failure"),
                       self.events(output="expected property\npanic: broken fixture"),
                       self.events() + self.events(),
                       [{"Package": "pkg", "Action": "fail"}]]:
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.check(events)

    def test_existing_artifacts_cannot_substitute_stale_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            marker = Path(tmp) / "old-receipt.json"
            marker.write_text("preserve this")
            with patch("sys.argv", ["check.py", "--artifacts", tmp]), \
                    patch("check.materialize") as materialize, \
                    contextlib.redirect_stderr(io.StringIO()), \
                    self.assertRaises(SystemExit) as result:
                main()
            self.assertEqual(result.exception.code, 2)
            materialize.assert_not_called()
            self.assertEqual(marker.read_text(), "preserve this")

    def test_artifacts_cannot_be_written_inside_candidate(self):
        with tempfile.TemporaryDirectory() as tmp:
            artifacts = Path(tmp) / "artifacts"
            with patch("sys.argv", ["check.py", "--source", tmp, "--expect-fixed", "--artifacts", str(artifacts)]), \
                    patch("check.materialize") as materialize, \
                    contextlib.redirect_stderr(io.StringIO()), \
                    self.assertRaises(SystemExit) as result:
                main()
            self.assertEqual(result.exception.code, 2)
            materialize.assert_not_called()
            self.assertFalse(artifacts.exists())


if __name__ == "__main__":
    unittest.main()
