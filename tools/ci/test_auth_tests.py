"""Auth JSON guard cases; execution belongs to hosted hygiene."""
import json
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path

from check_auth_tests import PACKAGES, REQUIRED_TESTS, load_required_tests, read_events, validate_tests


PACKAGE = 'danny.vn/banhmi/pkg/identity'
REQUIRED = {(PACKAGE, 'TestLogin')}


def suite(test='TestLogin'):
    return [
        {'Action': 'start', 'Package': PACKAGE},
        {'Action': 'run', 'Package': PACKAGE, 'Test': test},
        {'Action': 'pass', 'Package': PACKAGE, 'Test': test},
        {'Action': 'pass', 'Package': PACKAGE},
    ]


class AuthTestsTest(unittest.TestCase):
    def validate(self, events):
        validate_tests(events, REQUIRED, {PACKAGE})

    def test_required_test_and_new_test_pass(self):
        self.validate(suite()[:-1] + suite('TestNew')[1:])

    def test_absent_or_renamed_test_fails(self):
        with self.assertRaisesRegex(ValueError, 'missing required'):
            self.validate(suite('TestRenamed'))

    def test_skipped_test_subtest_or_package_fails(self):
        for test in ('TestLogin', 'TestLogin/nested/skip', 'TestNew/subtest', None):
            with self.subTest(test=test), self.assertRaisesRegex(ValueError, 'skipped'):
                self.validate(suite() + [{'Action': 'skip', 'Package': PACKAGE, 'Test': test}])

    def test_failed_test_or_package_fails(self):
        for test in ('TestLogin', None):
            with self.subTest(test=test), self.assertRaisesRegex(ValueError, 'failed'):
                self.validate(suite() + [{'Action': 'fail', 'Package': PACKAGE, 'Test': test}])

    def test_zero_tests_and_empty_capture_fail(self):
        for events in ([], [suite()[0], suite()[-1]]):
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.validate(events)

    def test_missing_package_and_incomplete_package_fail(self):
        with self.assertRaises(ValueError):
            validate_tests(suite(), REQUIRED, {PACKAGE, 'danny.vn/banhmi/pkg/oauthserver'})
        with self.assertRaises(ValueError):
            self.validate(suite()[:-1])

    def test_missing_run_pass_and_duplicate_terminal_fail(self):
        for events in (suite()[:1] + suite()[2:], suite()[:2] + suite()[3:],
                       suite()[:3] + suite()[2:]):
            with self.subTest(events=events), self.assertRaises(ValueError):
                self.validate(events)

    def test_sequential_parent_runs_for_isolated_subtests_pass(self):
        events = suite() + suite()
        validate_tests(events, REQUIRED, {PACKAGE})

    def test_required_subtest_must_run(self):
        required = REQUIRED | {(PACKAGE, 'TestLogin/race')}
        with self.assertRaisesRegex(ValueError, 'missing required'):
            validate_tests(suite(), required, {PACKAGE})
        events = suite()[:2] + [
            {'Action': 'run', 'Package': PACKAGE, 'Test': 'TestLogin/race'},
            {'Action': 'pass', 'Package': PACKAGE, 'Test': 'TestLogin/race'},
        ] + suite()[2:]
        validate_tests(events, required, {PACKAGE})

    def test_unexpected_package_and_malformed_events_fail(self):
        for event in ([], None, {}, {'Action': 'pass', 'Package': 'other'},
                      {'Action': 'bogus', 'Package': PACKAGE},
                      {'Action': 'run', 'Package': PACKAGE, 'Test': 42}):
            with self.subTest(event=event), self.assertRaises(ValueError):
                self.validate(suite() + [event])

    def test_package_without_test_files_is_allowed_alongside_tests(self):
        migration = 'danny.vn/banhmi/cmd/auth-migrate'
        events = suite() + [
            {'Action': 'start', 'Package': migration},
            {'Action': 'output', 'Package': migration,
             'Output': f'?   \t{migration}\t[no test files]\n'},
            {'Action': 'skip', 'Package': migration},
        ]
        validate_tests(events, REQUIRED, {PACKAGE, migration})

    def test_migration_skip_without_no_test_files_output_fails(self):
        migration = 'danny.vn/banhmi/cmd/auth-migrate'
        events = suite() + [
            {'Action': 'start', 'Package': migration},
            {'Action': 'skip', 'Package': migration},
        ]
        with self.assertRaisesRegex(ValueError, 'skipped'):
            validate_tests(events, REQUIRED, {PACKAGE, migration})

    def test_other_package_no_test_files_still_fails(self):
        events = [
            {'Action': 'start', 'Package': PACKAGE},
            {'Action': 'output', 'Package': PACKAGE,
             'Output': f'?   \t{PACKAGE}\t[no test files]\n'},
            {'Action': 'skip', 'Package': PACKAGE},
        ]
        with self.assertRaisesRegex(ValueError, 'skipped'):
            self.validate(events)

    def test_inventory_rejects_empty_duplicate_and_invalid_entries(self):
        for content in ('# empty\n', f'{PACKAGE} TestLogin\n{PACKAGE} TestLogin\n',
                        f'{PACKAGE}\n', 'other TestLogin\n', f'{PACKAGE} Login\n'):
            with tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / 'required.txt'
                path.write_text(content)
                with self.subTest(content=content), self.assertRaises(ValueError):
                    load_required_tests(path)

    def test_json_capture_rejects_malformed_or_blank_lines(self):
        for content in ('{', '\n', '[]\n'):
            with tempfile.TemporaryDirectory() as temporary:
                path = Path(temporary) / 'events.jsonl'
                path.write_text(content)
                with self.subTest(content=content), self.assertRaises(ValueError):
                    read_events(path)

    def test_cli_accepts_complete_checked_in_inventory(self):
        required = load_required_tests(REQUIRED_TESTS)
        events = []
        for package in sorted(PACKAGES):
            events.append({'Action': 'start', 'Package': package})
            tests = sorted(test for owner, test in required if owner == package)
            for test in tests:
                events.extend({'Action': action, 'Package': package, 'Test': test}
                              for action in ('run', 'pass'))
            if package == 'danny.vn/banhmi/cmd/auth-migrate':
                self.assertEqual(tests, [])
                events.append({'Action': 'output', 'Package': package,
                               'Output': f'?   \t{package}\t[no test files]\n'})
                action = 'skip'
            else:
                self.assertTrue(tests)
                action = 'pass'
            events.append({'Action': action, 'Package': package})
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'events.jsonl'
            path.write_text('\n'.join(json.dumps(event) for event in events))
            result = subprocess.run([sys.executable, str(Path(__file__).with_name('check_auth_tests.py')),
                                     str(path)], capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)

    def test_missing_capture_fails(self):
        with tempfile.TemporaryDirectory() as temporary:
            with self.assertRaises(OSError):
                read_events(Path(temporary) / 'missing.jsonl')

    def test_cli_missing_required_test_exits_nonzero(self):
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / 'events.jsonl'
            path.write_text('\n'.join(json.dumps(event) for event in suite()))
            result = subprocess.run([sys.executable, str(Path(__file__).with_name('check_auth_tests.py')),
                                     str(path)], capture_output=True, text=True)
            self.assertNotEqual(result.returncode, 0)


if __name__ == '__main__':
    unittest.main()
