import json
from pathlib import Path
import tempfile
import unittest

import check as regression
import pertype_composition as composition

PKG = composition.MODULE + "pkg/kgateway/setup"


def events(drop=None, fail=None):
    out = [{"Package": composition.MODULE + p, "Action": "pass"} for p in composition.PACKAGES]
    for names in composition.REQUIRED.values():
        for name in names:
            for case in [name] + [f"{name}/{mode}" for mode in composition.MODES]:
                if case == drop:
                    continue
                out.append({"Package": PKG, "Test": case, "Action": "fail" if case == fail else "pass"})
    return out


class OverlayTests(unittest.TestCase):
    def run_overlay(self, trees, changes):
        written, removed = {}, []
        result = composition.overlay(changes, lambda s, p: trees[s].get(p),
                                     written.__setitem__, removed.append, composition.diff3)
        return result, written, removed

    def test_untouched_upstream_takes_the_stack_version(self):
        trees = {"stack_base": {"a": b"x\n"}, "pr": {"a": b"x\n"}, "stack_tip": {"a": b"y\n"}}
        (taken, merged, conflicts), written, _ = self.run_overlay(trees, [("M", "a")])
        self.assertEqual((taken, merged, conflicts), (["a"], [], []))
        self.assertEqual(written["a"], b"y\n")

    def test_disjoint_edits_merge_and_overlapping_edits_conflict(self):
        base = b"1\n2\n3\n4\n5\n"
        trees = {"stack_base": {"a": base, "b": base}, "pr": {"a": b"1\nPR\n3\n4\n5\n", "b": b"1\n2\nPR\n4\n5\n"},
                 "stack_tip": {"a": b"1\n2\n3\n4\nSTACK\n", "b": b"1\n2\nSTACK\n4\n5\n"}}
        (_, merged, conflicts), written, _ = self.run_overlay(trees, [("M", "a"), ("M", "b")])
        self.assertEqual((merged, conflicts), (["a"], ["b"]))
        self.assertEqual(written["a"], b"1\nPR\n3\n4\nSTACK\n")

    def test_a_deletion_upstream_changed_is_a_conflict(self):
        trees = {"stack_base": {"a": b"x\n"}, "pr": {"a": b"changed\n"}, "stack_tip": {}}
        (_, _, conflicts), _, removed = self.run_overlay(trees, [("D", "a")])
        self.assertEqual((conflicts, removed), (["a"], []))


class SuiteTests(unittest.TestCase):
    def test_all_required_cases_pass(self):
        summary = composition.check_suite(0, events(), Path("suite.log"))
        self.assertEqual(summary["skipped"], [])

    def test_a_missing_mode_or_a_failure_is_rejected(self):
        name = composition.REQUIRED["pkg/kgateway/setup"][0]
        for broken in (events(drop=f"{name}/ordered"), events(fail=f"{name}/default")):
            with self.assertRaises(ValueError):
                composition.check_suite(0, broken, Path("suite.log"))

    def test_a_failing_package_or_make_is_rejected(self):
        broken = events()
        broken[0] = dict(broken[0], Action="fail")
        with self.assertRaises(ValueError):
            composition.check_suite(0, broken, Path("suite.log"))
        with self.assertRaises(ValueError):
            composition.check_suite(2, events(), Path("suite.log"))


class PinTests(unittest.TestCase):
    def test_fixture_and_profiles_are_pinned(self):
        self.assertEqual(regression.digest(composition.HERE / composition.FIXTURE["file"]), composition.FIXTURE["sha256"])
        profiles = json.loads((composition.HERE / "profiles.json").read_text())["profiles"]
        for name in composition.PROFILES:
            self.assertEqual(profiles[name]["revision"], composition.SUBJECTS["stack_tip"])
            self.assertEqual(profiles[name]["expected"], "pass")

    def test_the_adapter_must_address_one_test_site(self):
        with tempfile.TemporaryDirectory() as tmp:
            target = Path(tmp) / composition.ADAPTER["file"]
            target.parent.mkdir(parents=True)
            target.write_text("package setup\n")
            with self.assertRaises(ValueError):
                regression.apply_test_adapters(Path(tmp), [composition.ADAPTER])


if __name__ == "__main__":
    unittest.main()
