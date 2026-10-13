"""Hosted regressions for schema-suite presence and database isolation."""
import unittest
from check_schema_tests import EXPECTED_TESTS, validate_tests


class SchemaTestsTest(unittest.TestCase):
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
