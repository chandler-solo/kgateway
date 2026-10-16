import contextlib
import hashlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from check import apply_test_adapters, check_build_result, check_receipts, main, production_digest


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

    def test_build_failure_is_not_a_behavioral_counterexample(self):
        check_build_result(2, "undefined: oldAPI", "undefined: oldAPI")
        check_build_result(0, "")
        for code, output, expected in [(0, "undefined: oldAPI", "undefined: oldAPI"),
                                       (2, "network failed", "undefined: oldAPI"),
                                       (2, "undefined: oldAPI", None)]:
            with self.subTest(code=code, output=output), self.assertRaises(ValueError):
                check_build_result(code, output, expected)

    def test_adapters_cannot_change_production_or_drifted_test_source(self):
        with tempfile.TemporaryDirectory() as tmp:
            source = Path(tmp)
            for filename in ["subject.go", "subject_test.go"]:
                (source / filename).write_text("oldAPI")
            before = production_digest(source)
            adapter = {"file": "subject.go", "sha256": hashlib.sha256(b"oldAPI").hexdigest(),
                       "before": "oldAPI", "after": "newAPI"}
            with self.assertRaises(ValueError):
                apply_test_adapters(source, [adapter])
            adapter["file"] = "subject_test.go"
            apply_test_adapters(source, [adapter])
            self.assertEqual(before, production_digest(source))
            self.assertEqual((source / "subject_test.go").read_text(), "newAPI")
            with self.assertRaises(ValueError):
                apply_test_adapters(source, [adapter])


if __name__ == "__main__":
    unittest.main()
