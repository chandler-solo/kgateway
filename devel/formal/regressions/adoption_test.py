import unittest
from unittest.mock import patch

from adoption import check_suite


class AdoptionReceipts(unittest.TestCase):
    def test_requires_all_selected_tests_and_package_pass(self):
        package = "github.com/solo-io/gloo-gateway/v2/internal/xdsclients"
        events = [{"Package": package, "Test": "TestIdentity", "Action": action} for action in ("run", "pass")]
        events.append({"Package": package, "Action": "pass"})
        required = {("internal/xdsclients", "TestIdentity")}
        with patch("adoption.PACKAGES", ["internal/xdsclients"]):
            check_suite(events, required)
            for bad in [[], events[1:], events[:-1], events + events,
                        events + [{"Package": package, "Test": "TestIdentity/auth", "Action": "skip"}]]:
                with self.subTest(events=bad), self.assertRaises(ValueError):
                    check_suite(bad, required)
