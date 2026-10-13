"""Compare SQL Git blobs, including inherited history in every PR commit."""
import argparse
import re
import subprocess
from pathlib import Path

ROOTS = tuple(f'deploy/migrations/{name}' for name in
              ('extensions', 'bronze', 'silver', 'gold', 'ingest', 'config')) + ('deploy/authmigrations',)


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
        # Bootstrap is intentionally outside the migrator's SQL chain.
        if parent == 'deploy/authmigrations/bootstrap':
            continue
        if parent not in ROOTS:
            raise HistoryError(f'unrecognized migration location: {path}')
        mode, kind, blob = metadata.decode().split()
        if mode != '100644' or kind != 'blob':
            raise HistoryError(f'migration must be regular non-executable SQL: {path}')
        width = 14 if parent == 'deploy/authmigrations' else 5
        if not re.fullmatch(rf'[0-9]{{{width}}}_[a-z0-9_]+\.sql', name):
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
        version = int(path.rsplit('/', 1)[1].split('_', 1)[0])
        maximum = max(int(old.rsplit('/', 1)[1].split('_', 1)[0]) for old in base if old.rsplit('/', 1)[0] == directory)
        if version <= maximum:
            raise HistoryError(f'migration must append after inherited versions: {path}')


def check(base, head, history=False, root=Path('.')):
    base, head = resolve(root, base), resolve(root, head)
    base_files, head_files = inventory(root, base), inventory(root, head)
    compare(base_files, head_files)
    if history:
        commits = git(root, 'rev-list', head, '--not', base).decode().splitlines()
        for commit in commits:
            ancestors = git(root, 'merge-base', '--all', base, commit).decode().splitlines()
            if len(ancestors) != 1:
                raise HistoryError('missing or ambiguous common ancestry')
            compare(inventory(root, ancestors[0]), inventory(root, commit))
    return {'base': base, 'candidate': head,
            'same_tree': git(root, 'rev-parse', base + '^{tree}') == git(root, 'rev-parse', head + '^{tree}'),
            'history_checked': history, 'migration_additions': sorted(head_files.keys() - base_files.keys())}


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--base', required=True)
    parser.add_argument('--head', required=True)
    parser.add_argument('--check-pr-history', action='store_true')
    args = parser.parse_args()
    try:
        check(args.base, args.head, args.check_pr_history)
    except HistoryError as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
