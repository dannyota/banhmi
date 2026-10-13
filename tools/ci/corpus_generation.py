"""Select the six known sqlc outputs and package narrow generated artifacts."""
import argparse
import hashlib
import json
import os
import re
import subprocess
from pathlib import Path

SCHEMAS = ('bronze', 'silver', 'gold', 'ingest', 'config')
EXPECTED_TESTS = ('TestCorpusSchemaBoundaries', 'TestImmutableEvidenceAndReviewConstraints',
                  'TestStageIdempotencyCompletionAndFence', 'TestReleaseActivationCompareAndSet',
                  'TestReleaseActivationRace', 'TestReleaseManifestRequiresEvidence')


class GenerationError(ValueError):
    pass


def select(text, lane):
    blocks = re.split(r'(?m)(?=^  - engine:)', text)[1:]
    outputs = ('auth',) if lane == 'auth' else SCHEMAS
    selected = []
    for name in outputs:
        matches = [block for block in blocks if re.search(rf'(?m)^        out: "?pkg/store/{name}"?\s*$', block)]
        if len(matches) != 1 or not re.search(rf'(?m)^        package: "?db{name}"?\s*$', matches[0]):
            raise GenerationError(f'expected one sqlc output/package for {name}')
        selected.append(matches[0])
    return 'version: "2"\nsql:\n' + ''.join(selected)


def allowed(path, lane):
    names = ('auth',) if lane == 'auth' else SCHEMAS
    return any(re.fullmatch(rf'pkg/store/{name}/[a-zA-Z0-9_.]+\.go', path) for name in names) or (
        lane == 'auth' and path in ('go.mod', 'go.sum', 'gofmt.patch'))


def expected_provenance():
    event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
    kind = os.environ['GITHUB_EVENT_NAME']
    pr_head = event['pull_request']['head']['sha'] if kind == 'pull_request' else None
    if os.environ['CANDIDATE_SHA'] != os.environ['GITHUB_SHA']:
        raise GenerationError('resolved candidate does not match event checkout SHA')
    if kind == 'pull_request':
        event_base = event['pull_request']['base']['sha']
    elif kind == 'push':
        event_base = event['before']
    elif kind == 'workflow_dispatch' and event['repository']['default_branch'] == 'master':
        event_base = git('rev-parse', 'FETCH_HEAD').decode().strip()
    else:
        raise GenerationError('artifact event has no trusted comparison base')
    if os.environ['BASE_SHA'] != event_base:
        raise GenerationError('resolved base does not match trusted event comparison')
    return {'base': os.environ['BASE_SHA'], 'candidate': os.environ['CANDIDATE_SHA'],
            'event': kind, 'run': os.environ['GITHUB_RUN_ID'],
            'attempt': os.environ['GITHUB_RUN_ATTEMPT'], 'pr_head': pr_head,
            'history_checked': kind == 'pull_request'}


def validate_manifest(manifest, lane, output=None, expected=None):
    required = {'schema_version', 'lane', 'base', 'candidate', 'pr_head', 'event', 'run',
                'attempt', 'history_checked', 'same_tree', 'migration_additions',
                'sqlc', 'go', 'goose', 'verification_sha256', 'files'}
    if not isinstance(manifest, dict) or set(manifest) != required:
        raise GenerationError('artifact provenance fields are incomplete or unexpected')
    if type(manifest['schema_version']) is not int or manifest['schema_version'] != 1 or manifest['lane'] != lane:
        raise GenerationError('artifact schema or lane mismatch')
    for field in ('base', 'candidate', 'verification_sha256'):
        width = 64 if field == 'verification_sha256' else 40
        if not isinstance(manifest[field], str) or not re.fullmatch(rf'[0-9a-f]{{{width}}}', manifest[field]):
            raise GenerationError('artifact ref or verification digest is malformed')
    kind = manifest['event']
    if kind not in ('pull_request', 'push', 'workflow_dispatch'):
        raise GenerationError('artifact event is unsupported')
    for field in ('run', 'attempt'):
        if not isinstance(manifest[field], str) or not re.fullmatch('[1-9][0-9]*', manifest[field]):
            raise GenerationError('artifact run provenance is malformed')
    if type(manifest['same_tree']) is not bool or type(manifest['history_checked']) is not bool:
        raise GenerationError('artifact history status is malformed')
    if manifest['history_checked'] != (kind == 'pull_request'):
        raise GenerationError('artifact history status does not match event')
    if kind == 'pull_request':
        if not isinstance(manifest['pr_head'], str) or not re.fullmatch('[0-9a-f]{40}', manifest['pr_head']):
            raise GenerationError('artifact PR head is malformed')
    elif manifest['pr_head'] is not None:
        raise GenerationError('non-PR artifact contains a PR head')
    additions = manifest['migration_additions']
    if not isinstance(additions, list) or any(not isinstance(path, str) or not re.fullmatch(
            r'deploy/(?:migrations/(?:extensions|bronze|silver|gold|ingest|config)/[0-9]{5}|authmigrations/[0-9]{14})_[a-z0-9_]+\.sql', path)
            for path in additions) or additions != sorted(set(additions)):
        raise GenerationError('artifact migration additions are malformed')
    for field in ('sqlc', 'go', 'goose'):
        if not isinstance(manifest[field], str) or not manifest[field].strip():
            raise GenerationError('artifact tool version is missing')
    if expected is not None and any(manifest[field] != value for field, value in expected.items()):
        raise GenerationError('artifact provenance does not match resolved validation inputs')
    records = manifest.get('files')
    if not isinstance(records, list) or not records:
        raise GenerationError('artifact manifest has no files')
    seen = set()
    for record in records:
        if not isinstance(record, dict) or set(record) != {'path', 'change', 'sha256'}:
            raise GenerationError('malformed artifact record')
        path, change, digest = record.get('path'), record.get('change'), record.get('sha256')
        if not isinstance(path, str) or not allowed(path, lane) or path in seen:
            raise GenerationError('unexpected or duplicate artifact path')
        seen.add(path)
        if change not in ('A', 'M', 'D', '='):
            raise GenerationError('invalid artifact change')
        if (change == 'D' and digest is not None) or (change != 'D' and (
                not isinstance(digest, str) or not re.fullmatch('[0-9a-f]{64}', digest))):
            raise GenerationError('invalid artifact digest')
        if output is not None:
            file = output / path
            if change == 'D':
                if file.exists() or file.is_symlink():
                    raise GenerationError('deleted artifact file is still present')
            elif not file.is_file() or file.is_symlink() or hashlib.sha256(file.read_bytes()).hexdigest() != digest:
                raise GenerationError('artifact file does not match its manifest')
    if output is not None:
        report = output / 'verification.md'
        if not report.is_file() or report.is_symlink() or hashlib.sha256(report.read_bytes()).hexdigest() != manifest['verification_sha256']:
            raise GenerationError('artifact verification report is missing or altered')
        entries = list(output.rglob('*'))
        if any(file.is_symlink() for file in entries):
            raise GenerationError('artifact contains a symlink')
        actual = {file.relative_to(output).as_posix() for file in entries if file.is_file()}
        expected_files = {record['path'] for record in records if record['change'] != 'D'}
        if actual - {'manifest.json', 'verification.md'} != expected_files:
            raise GenerationError('artifact contains unlisted or missing files')


def validate_tests(events, expected, race_url, rollback_url):
    if not race_url or not rollback_url or race_url == rollback_url:
        raise GenerationError('race and rollback tests require independent database URLs')
    passes = []
    for event in events:
        if event.get('Action') in ('skip', 'fail'):
            raise GenerationError('schema suite skipped or failed')
        if event.get('Action') == 'pass' and event.get('Test') and '/' not in event['Test']:
            passes.append(event['Test'])
    if sorted(passes) != sorted(expected):
        raise GenerationError('schema suite missing, duplicated, or unexpected tests')


def validate_negative_tests(events):
    name = 'TestImmutableEvidenceAndReviewConstraints'
    if any(event.get('Action') == 'skip' for event in events):
        raise GenerationError('negative schema suite skipped')
    outcomes = [event['Action'] for event in events if event.get('Test') == name
                and event.get('Action') in ('pass', 'fail')]
    if outcomes != ['fail']:
        raise GenerationError('omitted required DDL did not fail the behavior suite')


def git(*args):
    return subprocess.check_output(['git', *args])


def changes(lane):
    tracked = git('diff', '--name-only', '--no-renames', '-z', 'HEAD').decode().split('\0')
    untracked = git('ls-files', '--others', '--exclude-standard', '-z').decode().split('\0')
    stores = ('auth',) if lane == 'auth' else SCHEMAS
    untracked += git('ls-files', '--others', '--ignored', '--exclude-standard', '-z', '--',
                     *(f'pkg/store/{name}' for name in stores)).decode().split('\0')
    paths = set(tracked + untracked) - {''}
    for path in paths:
        formatting = lane == 'auth' and path.endswith('.go') and not path.startswith('pkg/store/')
        if formatting:
            source = Path(path)
            if path in untracked or not source.is_file() or source.is_symlink():
                raise GenerationError('formatting artifact must preserve tracked Go files')
            before = git('show', f'HEAD:{path}')
            formatted = subprocess.run(['gofmt'], input=before, capture_output=True, check=True).stdout
            if formatted != source.read_bytes():
                raise GenerationError('Go changes outside auth output must be formatting only')
        elif not allowed(path, lane):
            raise GenerationError(f'generation wrote outside its lane: {path}')
    return paths


def stage(lane, output):
    changes(lane)
    validation = Path(os.environ['VALIDATION_DIR'])
    report = validation / f'{lane}-verification.md'
    if not report.is_file() or report.is_symlink():
        raise GenerationError('verification source must be a regular file')
    report_bytes = report.read_bytes()
    output.mkdir()
    files = {}
    names = ('auth',) if lane == 'auth' else SCHEMAS
    for name in names:
        directory = Path('pkg/store') / name
        generated = list(directory.glob('*.go'))
        if not generated:
            raise GenerationError(f'no generated output for {name}')
        for path in generated:
            if not path.is_file() or path.is_symlink():
                raise GenerationError('generated output must be regular files')
            files[path.as_posix()] = path.read_bytes()
    if lane == 'auth':
        for name in ('go.mod', 'go.sum'):
            files[name] = Path(name).read_bytes()
        files['gofmt.patch'] = git('diff', '--binary', '--', '*.go', ':(exclude)pkg/store/auth/**')
    tracked = git('ls-files', '-z', '--', *(f'pkg/store/{name}' for name in names)).decode().split('\0')
    if lane == 'auth':
        tracked += ['go.mod', 'go.sum']
    records = []
    for path in sorted(set(files) | (set(tracked) - {''})):
        if not allowed(path, lane):
            raise GenerationError('unexpected generated output')
        data = files.get(path)
        before = subprocess.run(['git', 'show', f'HEAD:{path}'], capture_output=True)
        if data is None:
            change = 'D'
        elif before.returncode:
            change = 'A'
        elif data == before.stdout:
            change = '='
        else:
            change = 'M'
        records.append({'path': path, 'change': change,
                        'sha256': hashlib.sha256(data).hexdigest() if data is not None else None})
        if data is not None:
            destination = output / path
            destination.parent.mkdir(parents=True, exist_ok=True)
            destination.write_bytes(data)
    manifest = json.loads((validation / 'history.json').read_text())
    if manifest.get('candidate') != git('rev-parse', 'HEAD').decode().strip():
        raise GenerationError('artifact candidate does not match tested checkout')
    manifest.update(schema_version=1, lane=lane, files=records,
                    verification_sha256=hashlib.sha256(report_bytes).hexdigest(), sqlc=subprocess.check_output(['sqlc', 'version'], text=True).strip(),
                    go=subprocess.check_output(['go', 'version'], text=True).strip(),
                    goose=subprocess.check_output(['go', 'list', '-m', 'github.com/pressly/goose/v3'], text=True).strip())
    (output / 'verification.md').write_bytes(report_bytes)
    (output / 'manifest.json').write_text(json.dumps(manifest, indent=2) + '\n')
    validate_manifest(manifest, lane, output, expected_provenance())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=('select', 'stage', 'tests', 'negative-tests'))
    parser.add_argument('lane', choices=('auth', 'corpus'))
    parser.add_argument('--output', type=Path)
    parser.add_argument('--events', type=Path)
    args = parser.parse_args()
    try:
        if args.command == 'select':
            print(select(Path('sqlc.yaml').read_text(), args.lane), end='')
        elif args.command == 'stage':
            stage(args.lane, args.output)
        else:
            events = [json.loads(line) for line in args.events.read_text().splitlines()]
            if args.command == 'negative-tests':
                validate_negative_tests(events)
            else:
                validate_tests(events, EXPECTED_TESTS, os.environ['CORPUS_RACE_URL'], os.environ['CORPUS_ROLLBACK_URL'])
    except (GenerationError, KeyError) as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
