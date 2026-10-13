"""Discover, clean, and stage every gen.go.out directory in sqlc.yaml."""
import argparse
import json
import re
import shutil
import stat
import subprocess
from pathlib import Path


def discover_outputs(config, root):
    root = root.resolve()
    entries = config.get('sql') if isinstance(config, dict) else None
    if not isinstance(entries, list) or not entries:
        raise ValueError('sqlc configuration requires SQL entries')
    outputs = set()
    for entry in entries:
        if not isinstance(entry, dict):
            raise ValueError('invalid sqlc SQL entry')
        gen = entry.get('gen', {})
        if not isinstance(gen, dict):
            raise ValueError('invalid sqlc generator configuration')
        if 'go' not in gen:
            continue
        go = gen['go']
        out = go.get('out') if isinstance(go, dict) else None
        if not isinstance(out, str) or not re.fullmatch(r'[A-Za-z0-9_.-]+(?:/[A-Za-z0-9_.-]+)*', out):
            raise ValueError('sqlc Go output must be a relative repository path')
        parts = out.split('/')
        if any(part in ('.', '..', '.git') for part in parts):
            raise ValueError('unsafe sqlc output directory: ' + out)
        path = root
        for part in parts:
            path = path / part
            if path.is_symlink() or (path.exists() and not path.is_dir()):
                raise ValueError('sqlc output must use real directories: ' + out)
        if not path.resolve().is_relative_to(root):
            raise ValueError('sqlc output escapes repository: ' + out)
        for key, value in go.items():
            if key.startswith('output_') and key.endswith('_file_name'):
                if (not isinstance(value, str) or value.startswith(('.', '_'))
                        or not re.fullmatch(r'[A-Za-z0-9_.-]+', value)
                        or not value.endswith('.go')):
                    raise ValueError('sqlc ' + key + ' must be a safe .go basename')
            elif key == 'output_files_suffix':
                if (not isinstance(value, str) or '/' in value or '\\' in value
                        or '..' in value):
                    raise ValueError('sqlc output_files_suffix must not contain path separators or ..')
        outputs.add(out)
    if not outputs:
        raise ValueError('sqlc configuration has no Go output directories')
    return sorted(outputs)


def clean_outputs(root, outputs):
    # Discovery validates the complete set before any deletion starts.
    for out in outputs:
        directory = root / out
        directory.mkdir(parents=True, exist_ok=True)
        for path in directory.iterdir():
            if path.is_dir() and not path.is_symlink():
                shutil.rmtree(path)
            else:
                path.unlink()


def stage_outputs(root, outputs, destination):
    files = set()
    for out in outputs:
        for path in (root / out).rglob('*'):
            mode = path.lstat().st_mode
            if stat.S_ISREG(mode):
                files.add(path.relative_to(root))
            elif not stat.S_ISDIR(mode):
                raise ValueError('generated artifact contains a non-regular file: ' + str(path))
    if not files:
        raise ValueError('no regenerated regular files to upload')
    destination.mkdir(parents=True, exist_ok=False)
    for relative in sorted(files):
        target = destination / relative
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(root / relative, target)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('mode', choices=('dirs', 'clean', 'stage'))
    parser.add_argument('--destination', type=Path)
    args = parser.parse_args()
    try:
        root = Path.cwd()
        result = subprocess.run(['yq', '-o=json', '.', 'sqlc.yaml'], check=True,
                                capture_output=True, text=True)
        outputs = discover_outputs(json.loads(result.stdout), root)
        if args.mode == 'dirs':
            print('\n'.join(outputs))
        elif args.mode == 'clean':
            clean_outputs(root, outputs)
        elif args.destination is None:
            raise ValueError('stage requires --destination')
        else:
            stage_outputs(root, outputs, args.destination)
    except (ValueError, OSError, subprocess.CalledProcessError) as error:
        parser.exit(1, str(error) + '\n')


if __name__ == '__main__':
    main()
