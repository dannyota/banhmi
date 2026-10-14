"""Hosted regressions for schema-suite presence and database isolation."""
import unittest
from check_schema_tests import (EXPECTED_TESTS, SCHEMA_PACKAGE, NEGATIVE_TEST,
                                validate_tests, validate_expected_failure)


class SchemaTestsTest(unittest.TestCase):
    def negative_events(self, output='operation succeeded, want PostgreSQL state 55000',
                        result='fail', package=SCHEMA_PACKAGE):
        # go test -json emits package start, test run/output/result, then package output/result.
        def event(action, test=None, **fields):
            value = {'Time': '2026-01-01T00:00:00Z', 'Action': action, 'Package': package}
            if test is not None:
                value['Test'] = test
            return {**value, **fields}

        package_result = 'pass' if result in ('pass', 'skip') else 'fail'
        return [event('start'), event('run', NEGATIVE_TEST),
                event('output', NEGATIVE_TEST, Output='=== RUN   ' + NEGATIVE_TEST + '\n')] + [
                event('output', NEGATIVE_TEST,
                      Output='    schema_integration_test.go:131: ' + line + '\n')
                for line in output.splitlines()] + [
                event('output', NEGATIVE_TEST,
                      Output='--- ' + result.upper() + ': ' + NEGATIVE_TEST + ' (0.00s)\n'),
                event(result, NEGATIVE_TEST, Elapsed=0.0),
                event('output', Output=package_result.upper() + '\n'),
                event('output', Output=('ok' if package_result == 'pass' else 'FAIL') +
                      '\t' + package + '\t0.001s\n'),
                event(package_result, Elapsed=0.001)]

    def test_expected_failure(self):
        validate_expected_failure(self.negative_events())

    def test_expected_failure_rejects_connection_and_setup_errors(self):
        detection = 'operation succeeded, want PostgreSQL state 55000'
        for marker in ('connection refused', 'failed to connect', 'fixture insert failed',
                       'Failed to Connect', 'Fixture insert failed'):
            for output in (marker, detection + '\n' + marker):
                with self.subTest(output=output), self.assertRaises(ValueError):
                    validate_expected_failure(self.negative_events(output))
            events = self.negative_events()
            events.insert(-1, {**events[-2], 'Output': marker + '\n'})
            with self.subTest(package_output=marker), self.assertRaises(ValueError):
                validate_expected_failure(events)

    def test_expected_failure_rejects_unrelated_assertion(self):
        with self.assertRaises(ValueError):
            validate_expected_failure(self.negative_events('revision count = 0, want 1'))

    def test_expected_failure_requires_literal_test_output(self):
        for output in ('operation succeeded, want PostgreSQL state 23514',
                       'operation succeeded, want PostgreSQL state 5500X', ''):
            with self.subTest(output=output), self.assertRaises(ValueError):
                validate_expected_failure(self.negative_events(output))
        events = self.negative_events('unrelated assertion')
        events.insert(-1, {**events[-2],
                           'Output': 'operation succeeded, want PostgreSQL state 55000\n'})
        with self.assertRaises(ValueError):
            validate_expected_failure(events)

    def test_expected_failure_rejects_missing_skip_pass_and_unrelated_failure(self):
        valid = self.negative_events()
        for events in ([], [event for event in valid if event['Action'] != 'run'],
                       [event for event in valid if event.get('Test') != NEGATIVE_TEST
                        or event['Action'] != 'fail'],
                       self.negative_events('checks passed', result='pass'),
                       self.negative_events('database URL is not set', result='skip'),
                       valid + [{'Action': 'skip', 'Package': SCHEMA_PACKAGE,
                                 'Test': NEGATIVE_TEST + '/nested'}],
                       valid + [{'Action': 'skip', 'Package': SCHEMA_PACKAGE}],
                       self.negative_events(package='unrelated/package'),
                       valid + [{'Action': 'fail', 'Package': 'unrelated/package'}],
                       valid + valid, valid + [valid[5]],
                       valid + [valid[1]], valid + [valid[-1]],
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
