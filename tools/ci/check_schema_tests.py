"""Require all six retained schema tests without skips on independent databases."""
import json
import os
import sys
from pathlib import Path

EXPECTED_TESTS = ('TestCorpusSchemaBoundaries', 'TestImmutableEvidenceAndReviewConstraints',
                  'TestStageIdempotencyCompletionAndFence', 'TestReleaseActivationCompareAndSet',
                  'TestReleaseActivationRace', 'TestReleaseManifestRequiresEvidence')


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
    try:
        events = [json.loads(line) for line in Path(sys.argv[1]).read_text().splitlines()]
        validate_tests(events, os.environ['CORPUS_RACE_URL'], os.environ['CORPUS_ROLLBACK_URL'])
    except (ValueError, KeyError, OSError) as error:
        sys.exit(str(error))
