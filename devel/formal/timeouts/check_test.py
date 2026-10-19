"""Boundary tests; every real capture also undergoes semantic mutation checks."""
import copy
import json
from pathlib import Path
import unittest

from check import PHASES, check_bootstrap, validate
from run import ROOT, outside_repository


def bootstrap():
    source = {'ads': {}, 'resource_api_version': 'V3', 'initial_fetch_timeout': '0s'}
    return {'dynamic_resources': {'cds_config': copy.deepcopy(source), 'lds_config': copy.deepcopy(source)},
            'cluster_manager': {'local_cluster_name': 'formal.formal'},
            'static_resources': {'clusters': [{'name': 'formal.formal', 'eds_cluster_config': {
                'eds_config': {**source, 'initial_fetch_timeout': '1s'}}}]},
            'layered_runtime': {'layers': [{'static_layer': {'envoy.restart_features.use_eds_cache_for_ads': True}}]}}


class TimeoutHarnessTest(unittest.TestCase):
    def test_source_specific_profile(self):
        self.assertEqual(check_bootstrap(bootstrap(), 'missing-eds')['local_eds']['initial_fetch_timeout'], '1s')

    def test_unset_parent_does_not_mean_disabled(self):
        b = bootstrap()
        del b['dynamic_resources']['cds_config']['initial_fetch_timeout']
        with self.assertRaisesRegex(ValueError, 'CDS/LDS'):
            check_bootstrap(b, 'missing-eds')

    def test_finite_parent_does_not_match_production(self):
        b = bootstrap()
        b['dynamic_resources']['lds_config']['initial_fetch_timeout'] = '15s'
        with self.assertRaisesRegex(ValueError, 'CDS/LDS'):
            check_bootstrap(b, 'missing-rds')

    def test_disabled_local_is_only_a_declared_control(self):
        b = bootstrap()
        b['static_resources']['clusters'][0]['eds_cluster_config']['eds_config']['initial_fetch_timeout'] = '0s'
        check_bootstrap(b, 'local-cycle')
        with self.assertRaisesRegex(ValueError, 'local EDS'):
            check_bootstrap(b, 'missing-eds')

    def test_cache_configuration_is_required(self):
        b = bootstrap()
        b['layered_runtime']['layers'] = []
        with self.assertRaisesRegex(ValueError, 'EDS cache'):
            check_bootstrap(b, 'cached-rewarm')

    def test_assignment_identity_is_required(self):
        b = bootstrap()
        b['cluster_manager']['local_cluster_name'] = 'different'
        b['static_resources']['clusters'][0]['name'] = 'different'
        with self.assertRaisesRegex(ValueError, 'assignment identity'):
            check_bootstrap(b, 'missing-eds')

    def test_empty_capture_cannot_pass(self):
        with self.assertRaisesRegex(ValueError, 'empty trace'):
            validate([], Path('/tmp'), 'missing-eds', 1)

    def test_ack_or_wire_time_cannot_invent_timer_arm(self):
        with self.assertRaisesRegex(ValueError, 'unknown event'):
            validate([{'event': 'arm-from-wire'}], Path('/tmp'), 'missing-eds', 1)

    def test_outputs_stay_outside_git(self):
        self.assertFalse(outside_repository(ROOT))
        self.assertFalse(outside_repository(ROOT / 'devel/formal/receipts/timeouts'))
        self.assertTrue(outside_repository(Path('/tmp/timeouts')))

    def test_all_profiles_have_pinned_images_and_source_contracts(self):
        p = json.loads((Path(__file__).parent / 'profiles.json').read_text())
        self.assertEqual(set(p['cases']), set(PHASES))
        self.assertTrue(all('@sha256:' in image['image'] for image in p['images'].values()))
        self.assertEqual(len(p['source']['sha256']), 3)
        self.assertTrue(all(len(value) == 64 for value in p['source']['sha256'].values()))
        self.assertEqual(p['timeouts']['generated_eds'], {'kind': 'unset', 'effective_seconds': 15})


if __name__ == '__main__':
    unittest.main()
