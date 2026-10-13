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


def validate_expected_failure(events):
    """Accept only execution and failure of the named mutation control."""
    runs = failures = package_failures = 0
    for event in events:
        if event.get('Package') != SCHEMA_PACKAGE:
            raise ValueError('negative control executed an unrelated package')
        action, test = event.get('Action'), event.get('Test')
        if action == 'skip':
            raise ValueError('negative control skipped')
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
