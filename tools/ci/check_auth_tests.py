"""Require named auth tests and complete, skip-free Go JSON test captures."""
import argparse
import json
import re
import sys
from pathlib import Path

PACKAGES = frozenset('danny.vn/banhmi/' + package for package in (
    'pkg/identity', 'cmd/auth-migrate', 'cmd/auth-cleanup', 'pkg/oauthserver'))
NO_TEST_FILES_PACKAGE = 'danny.vn/banhmi/cmd/auth-migrate'
REQUIRED_TESTS = Path(__file__).with_name('required-auth-tests.txt')
ACTIONS = frozenset(('start', 'run', 'pause', 'cont', 'pass', 'skip', 'fail', 'output'))


def load_required_tests(path):
    required = set()
    for line in path.read_text(encoding='utf-8').splitlines():
        if not line.strip() or line.lstrip().startswith('#'):
            continue
        fields = line.split()
        if (len(fields) != 2 or fields[0] not in PACKAGES
                or not re.fullmatch(r'Test[A-Za-z0-9_]+(?:/[^\s]+)*', fields[1])):
            raise ValueError('invalid required auth test entry')
        key = tuple(fields)
        if key in required:
            raise ValueError('duplicate required auth test entry')
        required.add(key)
    if not required:
        raise ValueError('required auth test list is empty')
    return required


def read_events(path):
    events = []
    for number, line in enumerate(path.read_text(encoding='utf-8').splitlines(), 1):
        try:
            event = json.loads(line)
        except ValueError as error:
            raise ValueError(f'invalid auth JSON event on line {number}') from error
        if not isinstance(event, dict):
            raise ValueError(f'auth JSON event on line {number} must be an object')
        events.append(event)
    return events


def validate_tests(events, required, packages=PACKAGES):
    active_packages = set()
    passed_packages = set()
    active_tests = set()
    passed_tests = set()
    no_test_files = set()
    packages_with_tests = set()
    runs = 0
    for event in events:
        if not isinstance(event, dict):
            raise ValueError('auth JSON event must be an object')
        package, action, test = event.get('Package'), event.get('Action'), event.get('Test')
        if not isinstance(package, str) or package not in packages:
            raise ValueError('auth capture contains an unexpected or missing package')
        if not isinstance(action, str) or action not in ACTIONS:
            raise ValueError('auth capture contains an invalid action')
        if test is not None and (not isinstance(test, str) or not test):
            raise ValueError('auth capture contains an invalid test name')
        empty_migration = (action == 'skip' and test is None
                           and package == NO_TEST_FILES_PACKAGE
                           and package in no_test_files
                           and package not in packages_with_tests)
        if action == 'fail' or (action == 'skip' and not empty_migration):
            raise ValueError(f'auth suite {"skipped" if action == "skip" else "failed"}: {package} {test or ""}')
        if action == 'start':
            if test or package in active_packages:
                raise ValueError('invalid or duplicate auth package start')
            active_packages.add(package)
            no_test_files.discard(package)
            continue
        if package not in active_packages:
            raise ValueError('auth event outside a started package')
        if action == 'output':
            output = event.get('Output')
            if not isinstance(output, str):
                raise ValueError('auth output must be text')
            if (test is None and package == NO_TEST_FILES_PACKAGE
                    and re.fullmatch(r'\?\s+' + re.escape(package)
                                     + r'\s+\[no test files\]\n?', output)):
                no_test_files.add(package)
        key = (package, test)
        if test:
            if action == 'run':
                if key in active_tests:
                    raise ValueError('duplicate active auth test')
                active_tests.add(key)
                packages_with_tests.add(package)
                runs += 1
            elif action == 'pass':
                if key not in active_tests:
                    raise ValueError('auth test passed without a matching run')
                active_tests.remove(key)
                passed_tests.add(key)
        elif action == 'pass' or empty_migration:
            if any(key[0] == package for key in active_tests):
                raise ValueError('auth package passed with unfinished tests')
            active_packages.remove(package)
            passed_packages.add(package)
        elif action in ('run', 'pause', 'cont'):
            raise ValueError('auth test action requires a test name')
    if not runs:
        raise ValueError('auth package list ran zero tests')
    if active_packages or active_tests or passed_packages != set(packages):
        raise ValueError('auth capture has missing or incomplete packages or tests')
    missing = required - passed_tests
    if missing:
        raise ValueError('missing required auth tests: ' + ', '.join(
            f'{package} {test}' for package, test in sorted(missing)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('events', type=Path)
    args = parser.parse_args()
    try:
        validate_tests(read_events(args.events), load_required_tests(REQUIRED_TESTS))
    except (ValueError, OSError) as error:
        sys.exit(str(error))
