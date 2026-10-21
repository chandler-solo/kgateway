import copy
import unittest

from watch_lifecycle import PACKAGE, check_receipts


class WatchReceipts(unittest.TestCase):
    def events(self):
        events = []
        test = "TestFormalWatchInitialParking"
        for name in (test, test + "/default", test + "/ordered"):
            events += [{"Package": PACKAGE, "Test": name, "Action": "run"},
                       {"Package": PACKAGE, "Test": name, "Action": "output",
                        "Output": "WATCH-INITIAL-ACCOUNTING: lost\n--- FAIL: " + test},
                       {"Package": PACKAGE, "Test": name, "Action": "fail"}]
        events.append({"Package": PACKAGE, "Action": "fail"})
        return events

    def check(self, events, code=2, log=""):
        check_receipts(events, {"TestFormalWatchInitialParking": "fail"}, code, log)

    def test_both_modes_and_exact_failures(self):
        self.check(self.events())

    def test_missing_mode_skip_duplicate_and_wrong_failure(self):
        for mutate in (
            lambda e: [v for v in e if not v.get("Test", "").endswith("ordered")],
            lambda e: e[:-1], lambda e: e + e,
            lambda e: [dict(v, Action="skip") if v.get("Action") == "fail" else v for v in e],
            lambda e: [dict(v, Output="unrelated") if v.get("Action") == "output" else v for v in e],
        ):
            with self.assertRaises(ValueError):
                self.check(mutate(copy.deepcopy(self.events())))

    def test_execution_failure_cannot_count_as_counterexample(self):
        for log in ("HARNESS: stalled", "DATA RACE", "panic: oops", "test timed out", "[build failed]"):
            with self.subTest(log=log), self.assertRaises(ValueError):
                self.check(self.events(), log=log)
        with self.assertRaises(ValueError):
            self.check(self.events(), code=0)
