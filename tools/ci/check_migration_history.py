"""Protect frozen corpus trees and append-only auth SQL against trusted event bases."""
import argparse
import json
import os
import re
import subprocess
from pathlib import Path

FROZEN_ROOTS = tuple(f'deploy/migrations/{name}' for name in
                     ('extensions', 'bronze', 'silver', 'gold', 'ingest', 'config'))
AUTH_ROOT = 'deploy/authmigrations'
ROOTS = FROZEN_ROOTS + (AUTH_ROOT,)


class HistoryError(ValueError):
    pass


def git(root, *args):
    result = subprocess.run(['git', '-C', str(root), *args], capture_output=True)
    if result.returncode:
        raise HistoryError('required Git history is unavailable')
    return result.stdout


def resolve(root, ref):
    if not re.fullmatch(r'[0-9a-f]{40}', ref):
        raise HistoryError('comparison refs must be full commit SHAs')
    return git(root, 'rev-parse', '--verify', ref + '^{commit}').decode().strip()


def inventory(root, ref):
    files = {}
    for record in git(root, 'ls-tree', '-rz', ref, '--', 'deploy/migrations', 'deploy/authmigrations').split(b'\0'):
        if not record:
            continue
        metadata, raw_path = record.split(b'\t', 1)
        path = raw_path.decode()
        parent, name = path.rsplit('/', 1)
        if not path.endswith('.sql'):
            if path.lower().endswith('.sql'):
                raise HistoryError(f'invalid SQL extension: {path}')
            continue
        bootstrap = parent == 'deploy/authmigrations/bootstrap'
        if parent not in ROOTS and not bootstrap:
            raise HistoryError(f'unrecognized migration location: {path}')
        mode, kind, blob = metadata.decode().split()
        if mode != '100644' or kind != 'blob':
            raise HistoryError(f'migration must be regular non-executable SQL: {path}')
        width = 14 if parent == 'deploy/authmigrations' else 5
        if not bootstrap and not re.fullmatch(rf'[0-9]{{{width}}}_[a-z0-9_]+\.sql', name):
            raise HistoryError(f'invalid migration filename: {path}')
        files[path] = blob
    for directory in ROOTS:
        names = sorted(path.rsplit('/', 1)[1] for path in files if path.rsplit('/', 1)[0] == directory)
        if not names:
            raise HistoryError(f'missing migration root: {directory}')
        versions = [int(name.split('_', 1)[0]) for name in names]
        if len(set(versions)) != len(versions) or versions != sorted(versions) or min(versions) <= 0:
            raise HistoryError(f'invalid migration version order: {directory}')
    return files


def compare(base, head):
    for path, blob in base.items():
        if head.get(path) != blob:
            raise HistoryError(f'inherited migration changed or removed: {path}')
    for path in head.keys() - base.keys():
        directory = path.rsplit('/', 1)[0]
        if directory in FROZEN_ROOTS:
            raise HistoryError(f'frozen legacy migration added: {path}')
        if directory != AUTH_ROOT:
            continue
        version = int(path.rsplit('/', 1)[1].split('_', 1)[0])
        maximum = max(int(old.rsplit('/', 1)[1].split('_', 1)[0]) for old in base if old.rsplit('/', 1)[0] == directory)
        if version <= maximum:
            raise HistoryError(f'migration must append after inherited versions: {path}')


def check(base, head, root=Path('.')):
    base, head = resolve(root, base), resolve(root, head)
    compare(inventory(root, base), inventory(root, head))
    return {'base': base, 'candidate': head}


def check_event(event, kind, expected_head, root=Path('.')):
    head = git(root, 'rev-parse', 'HEAD').decode().strip()
    resolve(root, expected_head)
    if head != expected_head:
        raise HistoryError('candidate checkout does not match event SHA')
    if kind == 'pull_request':
        base = event['pull_request']['base']['sha']
        pr_head = resolve(root, event['pull_request']['head']['sha'])
        git(root, 'merge-base', '--is-ancestor', pr_head, head)
    elif kind == 'push':
        base = event['before']
    elif kind == 'workflow_dispatch':
        if event['repository']['default_branch'] != 'master':
            raise HistoryError('manual comparison requires the protected master branch')
        git(root, 'fetch', '--no-tags', 'origin', 'refs/heads/master')
        base = git(root, 'rev-parse', 'FETCH_HEAD').decode().strip()
    else:
        raise HistoryError('unsupported migration comparison event')
    return check(base, head, root)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--base')
    parser.add_argument('--head')
    args = parser.parse_args()
    try:
        if args.base is not None or args.head is not None:
            if not args.base or not args.head:
                raise HistoryError('--base and --head must be supplied together')
            report = check(args.base, args.head)
        else:
            event = json.loads(Path(os.environ['GITHUB_EVENT_PATH']).read_text())
            report = check_event(event, os.environ['GITHUB_EVENT_NAME'], os.environ['GITHUB_SHA'])
        print(json.dumps(report, sort_keys=True))
    except (HistoryError, KeyError, OSError, ValueError) as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
