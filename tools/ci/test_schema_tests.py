"""Hosted regressions for schema-suite presence and database isolation."""
import unittest
from check_schema_tests import (EXPECTED_TESTS, SCHEMA_PACKAGE, NEGATIVE_TEST,
                                validate_tests, validate_expected_failure)


class SchemaTestsTest(unittest.TestCase):
    def negative_events(self):
        return [{'Action': action, 'Package': SCHEMA_PACKAGE, 'Test': NEGATIVE_TEST}
                for action in ('run', 'fail')] + [{'Action': 'fail', 'Package': SCHEMA_PACKAGE}]

    def test_expected_failure(self):
        validate_expected_failure(self.negative_events())

    def test_expected_failure_rejects_missing_skip_pass_and_unrelated_failure(self):
        valid = self.negative_events()
        for events in ([], valid[1:], valid[:1] + valid[2:],
                       [{**event, 'Action': 'pass'} if event['Action'] == 'fail' else event
                        for event in valid],
                       valid + [{'Action': 'skip', 'Package': SCHEMA_PACKAGE,
                                 'Test': NEGATIVE_TEST + '/nested'}],
                       valid + [{'Action': 'skip', 'Package': SCHEMA_PACKAGE}],
                       [{'Action': 'fail', 'Package': 'unrelated/package'}],
                       valid + [{'Action': 'fail', 'Package': 'unrelated/package'}],
                       valid + [valid[1]],
                       valid + [{'Action': 'run', 'Package': SCHEMA_PACKAGE, 'Test': 'TestOther'}]):
            with self.subTest(events=events), self.assertRaises(ValueError):
                validate_expected_failure(events)

    def test_complete_suite(self):
        events = [{'Action': 'pass', 'Test': name} for name in EXPECTED_TESTS]
        validate_tests(events, 'race', 'rollback')

    def test_missing_duplicate_unexpected_skip_and_failure(self):
        valid = [{'Action': 'pass', 'Test': name} for name in EXPECTED_TESTS]
        for events in ([], valid[:-1], valid + [valid[0]],
                       valid + [{'Action': 'pass', 'Test': 'TestUnexpected'}],
                       valid + [{'Action': 'skip', 'Test': EXPECTED_TESTS[0] + '/nested'}],
                       valid + [{'Action': 'skip'}], valid + [{'Action': 'fail'}]):
            with self.subTest(events=events), self.assertRaises(ValueError):
                validate_tests(events, 'race', 'rollback')

    def test_independent_urls(self):
        events = [{'Action': 'pass', 'Test': name} for name in EXPECTED_TESTS]
        for race, rollback in (('', 'rollback'), ('race', ''), ('same', 'same')):
            with self.subTest(race=race, rollback=rollback), self.assertRaises(ValueError):
                validate_tests(events, race, rollback)
