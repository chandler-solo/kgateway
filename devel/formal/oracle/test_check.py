"""Negative controls for evidence that otherwise could pass vacuously."""
import json
from pathlib import Path
import tempfile
import unittest

from check import check_unit_receipt


class ReceiptTests(unittest.TestCase):
    def setUp(self):
        names = ["Examples", "KRT", "RandomHistories", "Protocol", "VersionCollision",
                 "ComparatorRejectsCorruption", "CacheExamples", "CacheProtocol"]
        self.records = [dict(Action=action, Test="TestPublicationOracle" + name)
                        for name in names for action in ["run", "pass"]]
        self.records.append(dict(Action="pass", Package="proxy_syncer"))

    def check(self, records):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "unit.jsonl"
            path.write_text("".join(json.dumps(r) + "\n" for r in records))
            return check_unit_receipt(path)

    def test_complete(self):
        self.assertEqual(self.check(self.records), 9)  # includes package pass

    def test_missing_execution(self):
        with self.assertRaises(RuntimeError):
            self.check(self.records[1:])

    def test_missing_package(self):
        with self.assertRaises(RuntimeError):
            self.check(self.records[:-1])

    def test_incomplete_child(self):
        with self.assertRaises(RuntimeError):
            self.check(self.records + [dict(Action="run", Test="TestPublicationOracleExamples/lost")])

    def test_skip_or_failure(self):
        for action in ["skip", "fail"]:
            with self.subTest(action=action), self.assertRaises(RuntimeError):
                self.check(self.records + [dict(Action=action, Test="TestPublicationOracleExamples/child")])

    def test_truncated_json(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "unit.jsonl"
            path.write_text('{"Action":')
            with self.assertRaises(ValueError):
                check_unit_receipt(path)


if __name__ == "__main__":
    unittest.main()
