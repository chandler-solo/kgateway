"""Negative controls for evidence that otherwise could pass vacuously."""
import json
from pathlib import Path
import tempfile
import unittest

from check import check_fuzz_receipt, check_unit_receipt, fuzz_observation


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


class FuzzReceiptTests(unittest.TestCase):
    completion = "PASS\nok  \tgithub.com/kgateway-dev/kgateway/v2/pkg/kgateway/proxy_syncer\t1.812s\n"
    baseline = "fuzz: elapsed: 0s, gathering baseline coverage: 0/447 completed\n"
    baseline_done = "fuzz: elapsed: 1s, gathering baseline coverage: 447/447 completed, now fuzzing with 2 workers\n"

    def test_count_budget_exhausted_during_baseline(self):
        # Exact progress from the reported --fuzztime 20x failure.
        output = self.baseline + "fuzz: elapsed: 1s, gathering baseline coverage: 20/447 completed\n" + self.completion
        observation = fuzz_observation(output)
        self.assertTrue(observation["go_passed"])
        self.assertEqual(observation["baseline_completed"], 20)
        self.assertEqual(observation["baseline_total"], 447)
        self.assertIsNone(observation["executions"])
        self.assertEqual(observation["generated_executions"], 0)
        self.assertEqual(len(observation["progress"]), 2)
        with self.assertRaises(RuntimeError) as error:
            check_fuzz_receipt(observation, "FuzzPublicationOracle", "20x", "fuzz.log")
        for detail in ["FuzzPublicationOracle", "20x", "20/447", "baseline", "20000x", "20s", "fuzz.log"]:
            self.assertIn(detail, str(error.exception))

    def test_baseline_completion_or_execs_line_is_not_generated_execution(self):
        for progress in ["", "fuzz: elapsed: 1s, execs: 447 (0/sec), new interesting: 0 (total: 447)\n"]:
            with self.subTest(progress=progress):
                observation = fuzz_observation(self.baseline + self.baseline_done + progress + self.completion)
                self.assertEqual(observation["generated_executions"], 0)
                with self.assertRaisesRegex(RuntimeError, "no generated fuzz execution"):
                    check_fuzz_receipt(observation, "FuzzPublicationOracle", "447x", "fuzz.log")

    def test_generated_execution(self):
        observation = fuzz_observation(self.baseline + self.baseline_done +
            "fuzz: elapsed: 2s, execs: 448 (1/sec), new interesting: 0 (total: 447)\n" + self.completion)
        self.assertEqual(observation["generated_executions"], 1)
        check_fuzz_receipt(observation, "FuzzPublicationOracle", "448x", "fuzz.log")

    def test_missing_completion_or_baseline(self):
        progress = "fuzz: elapsed: 2s, execs: 448 (1/sec)\n"
        for output in [self.baseline + self.baseline_done + progress,
                       self.baseline + self.baseline_done + progress + "PASS\n",
                       progress + self.completion]:
            with self.subTest(output=output), self.assertRaises(RuntimeError):
                check_fuzz_receipt(fuzz_observation(output), "FuzzPublicationOracle", "448x", "fuzz.log")


if __name__ == "__main__":
    unittest.main()
