"""Require all six retained schema tests without skips on independent databases."""
import argparse
import json
import os
import sys
from pathlib import Path

EXPECTED_TESTS = ('TestCorpusSchemaBoundaries', 'TestImmutableEvidenceAndReviewConstraints',
                  'TestStageIdempotencyCompletionAndFence', 'TestReleaseActivationCompareAndSet',
                  'TestReleaseActivationRace', 'TestReleaseManifestRequiresEvidence')
SCHEMA_PACKAGE = 'danny.vn/banhmi/internal/corpus/schematest'
NEGATIVE_TEST = 'TestImmutableEvidenceAndReviewConstraints'
NEGATIVE_DETECTION = 'operation succeeded, want PostgreSQL state 55000'
SETUP_ERROR_MARKERS = ('connection refused', 'failed to connect', 'fixture')


def validate_expected_failure(events):
    """Require the named mutation failure's detection output without setup errors."""
    runs = failures = package_failures = 0
    test_output = []
    all_output = []
    for event in events:
        if event.get('Package') != SCHEMA_PACKAGE:
            raise ValueError('negative control executed an unrelated package')
        action, test = event.get('Action'), event.get('Test')
        if action == 'skip':
            raise ValueError('negative control skipped')
        if action == 'output':
            output = event.get('Output')
            if not isinstance(output, str):
                raise ValueError('negative control output must be text')
            all_output.append(output)
            if test == NEGATIVE_TEST:
                test_output.append(output)
        if test:
            if test != NEGATIVE_TEST:
                raise ValueError('negative control executed an unexpected test')
            if action == 'pass':
                raise ValueError('negative control passed where failure was required')
            runs += action == 'run'
            failures += action == 'fail'
        elif action == 'fail':
            package_failures += 1
        elif action == 'pass':
            raise ValueError('negative control package passed')
    if (runs, failures, package_failures) != (1, 1, 1):
        raise ValueError('negative control requires exactly one test run and failure and its package failure')
    if any(marker in ''.join(all_output).lower() for marker in SETUP_ERROR_MARKERS):
        raise ValueError('negative control contains connection or fixture errors')
    if NEGATIVE_DETECTION not in ''.join(test_output):
        raise ValueError('negative control missing expected detection: ' + NEGATIVE_DETECTION)


def validate_tests(events, race_url, rollback_url):
    if not race_url or not rollback_url or race_url == rollback_url:
        raise ValueError('race and rollback tests require independent database URLs')
    passes = []
    for event in events:
        if event.get('Action') in ('skip', 'fail'):
            raise ValueError('schema suite skipped or failed')
        if event.get('Action') == 'pass' and event.get('Test') and '/' not in event['Test']:
            passes.append(event['Test'])
    if sorted(passes) != sorted(EXPECTED_TESTS):
        raise ValueError('schema suite missing, duplicated, or unexpected tests')


if __name__ == '__main__':
    parser = argparse.ArgumentParser()
    parser.add_argument('events', type=Path)
    parser.add_argument('--expected-failure', action='store_true')
    args = parser.parse_args()
    try:
        events = [json.loads(line) for line in args.events.read_text().splitlines()]
        if args.expected_failure:
            validate_expected_failure(events)
        else:
            validate_tests(events, os.environ['CORPUS_RACE_URL'], os.environ['CORPUS_ROLLBACK_URL'])
    except (ValueError, KeyError, OSError) as error:
        sys.exit(str(error))
