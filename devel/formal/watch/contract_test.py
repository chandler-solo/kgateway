import copy
import json
import unittest

from contract import HERE, validate
from check import compare


class ContractTests(unittest.TestCase):
    def seed(self):
        return json.loads((HERE / 'scenarios/watch-parking-initial.json').read_text())

    def test_all_frozen_scenarios(self):
        for path in (HERE / 'scenarios').glob('*.json'):
            with self.subTest(path=path):
                validate(json.loads(path.read_text()))

    def test_invalid_schema_and_ownership(self):
        mutations = [
            lambda r: r.update(unknown=True),
            lambda r: r['initialState'].update(unknown=True),
            lambda r: r['bounds'].update(keys=True),
            lambda r: r['bounds'].update(events=0),
            lambda r: r['events'][0].update(actor='observer'),
            lambda r: r['events'][0].update(happensAfter=['future']),
            lambda r: r['events'][1].update(id=r['events'][0]['id']),
            lambda r: r['events'][0]['arguments'].update(names=['A']),
            lambda r: r['expectations'][0].update(outcome='outside-profile'),
            lambda r: r['expectations'][0].update(witnessOperationIds=[]),
            lambda r: r['expectations'][0].update(witnessOperationIds=['absent']),
        ]
        for mutation in mutations:
            value = self.seed()
            mutation(value)
            with self.subTest(value=value), self.assertRaises(ValueError):
                validate(value)

    def test_comparator_does_not_trust_ok_alone(self):
        request = self.seed()
        state = {k: '' for k in ('version', 'phase', 'captured', 'lock', 'publication', 'timer', 'accepted', 'rejected', 'nonce', 'applied')}
        state.update(names=['A'], watch=True, interested=True, watchOwner=1, generation=1, timerOwner=1,
                     generations=[1], own=None, queued=None, sent=None, channel={})
        result = {'ok': True, 'outcomes': request['expectations'],
                  'observations': [{'id': e['id'], 'state': state} for e in request['events']]}
        compare(request, result)
        for mutation in (
            lambda r: r.update(ok=False), lambda r: r.update(unexpected=True),
            lambda r: r['observations'][0]['state'].pop('timer'), lambda r: r.update(outcomes=[]),
            lambda r: r.update(observations=r['observations'][:-1]),
            lambda r: r['outcomes'][0].update(outcome='violated'),
        ):
            wrong = copy.deepcopy(result)
            mutation(wrong)
            with self.assertRaises(ValueError):
                compare(request, wrong)
