import unittest

from check import complete


class RuntimeReceipts(unittest.TestCase):
    def test_exit_and_every_required_observation_are_necessary(self):
        observation = {"event": "observation", "phase": "applied"}
        self.assertTrue(complete(0, [observation], ["applied"]))
        for code, events, required in [(1, [observation], ["applied"]),
                                       (0, [], ["applied"]), (0, [observation], []),
                                       (0, [observation, observation], ["applied"]),
                                       (0, [{"event": "request", "phase": "applied"}], ["applied"])]:
            with self.subTest(code=code, events=events):
                self.assertFalse(complete(code, events, required))
