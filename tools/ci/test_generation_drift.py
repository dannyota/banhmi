"""Hosted regressions for generated, ignored, deleted, and dependency drift."""
import json
import os
import shutil
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('reject_generation_drift.sh').resolve()
OUTPUT_HELPER = SCRIPT.with_name('sqlc_outputs.py')


class GenerationDriftTest(unittest.TestCase):
    def test_repository_decision_ignores_former_capture_files(self):
        replacements = ('symlink', 'directory', 'regular', 'truncate')
        for mutation in ('clean', 'untracked',
                         'failure-status', 'failure-ls-files', 'failure-diff',
                         *(f'{capture}-{replacement}'
                           for capture in ('status', 'scopes', 'diff', 'all')
                           for replacement in replacements)):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary) / 'repository'
                runtime = Path(temporary) / 'runtime'
                binaries = Path(temporary) / 'bin'
                root.mkdir()
                runtime.mkdir()
                binaries.mkdir()
                real_git = shutil.which('git')
                self.assertIsNotNone(real_git)

                def git(*args):
                    subprocess.run([real_git, '-C', str(root), *args], check=True,
                                   capture_output=True)

                git('init', '-b', 'master')
                git('config', 'user.name', 'Fixture')
                git('config', 'user.email', 'fixture@example.invalid')
                (root / 'sqlc.yaml').write_text(json.dumps({'sql': [{
                    'schema': 'schema', 'queries': 'queries',
                    'gen': {'go': {'out': 'store'}},
                }]}))
                git('add', '--', 'sqlc.yaml')
                git('commit', '-m', 'fixture')
                # Reproduce the fixed-path exploit without changing the repository.
                for name in ('generation-drift.txt', 'sqlc-output-directories.txt'):
                    (runtime / name).symlink_to('/dev/null')
                former_captures = runtime / 'repository-drift.fixture'
                former_captures.mkdir(mode=0o700)
                for name in ('status', 'scopes', 'diff'):
                    (former_captures / f'{name}.txt').write_text('decoy\n')
                original_inodes = {name: (former_captures / f'{name}.txt').stat().st_ino
                                   for name in ('status', 'scopes', 'diff')}
                if mutation != 'clean' and not mutation.startswith('failure-'):
                    (root / 'unexpected.txt').write_text('drift\n')
                capture, _, replacement = mutation.partition('-')
                wrapper = binaries / 'git'
                wrapper.write_text('''#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == "${FAIL_COMMAND}" ]]; then
  exit 17
fi
# Mutate after status and ignored-addition output has been collected in memory.
if [[ "$1" == diff && -n "${CAPTURE_REPLACEMENT}" ]]; then
  for directory in "${RUNNER_TEMP}"/repository-drift.*; do
    test -d "${directory}"
    test "$(stat -c %a "${directory}")" = 700
    for name in status scopes diff; do
      if [[ "${CAPTURE_NAME}" != all && "${CAPTURE_NAME}" != "${name}" ]]; then
        continue
      fi
      path="${directory}/${name}.txt"
      test -f "${path}" && test ! -L "${path}"
      if [[ "${CAPTURE_REPLACEMENT}" != truncate ]]; then rm -- "${path}"; fi
      case "${CAPTURE_REPLACEMENT}" in
        symlink) ln -s /dev/null "${path}" ;;
        directory) mkdir "${path}" ;;
        regular|truncate) printf '' > "${path}" ;;
      esac
    done
  done
  # Replace both older fixed paths as well.
  for name in generation-drift.txt sqlc-output-directories.txt; do
    rm -- "${RUNNER_TEMP}/${name}"
    ln -s /dev/null "${RUNNER_TEMP}/${name}"
  done
  printf 'former capture paths mutated\n' >&2
fi
exec "${REAL_GIT}" "$@"
''')
                wrapper.chmod(0o755)
                summary = runtime / 'summary.md'
                result = subprocess.run(['bash', str(SCRIPT), '--repository'], cwd=root,
                                        capture_output=True, text=True, env={
                                            **os.environ, 'RUNNER_TEMP': str(runtime),
                                            'GITHUB_STEP_SUMMARY': str(summary),
                                            'PATH': str(binaries) + os.pathsep + os.environ['PATH'],
                                            'REAL_GIT': real_git,
                                            'FAIL_COMMAND': replacement if capture == 'failure' else '',
                                            'CAPTURE_NAME': capture,
                                            'CAPTURE_REPLACEMENT': (replacement
                                                                   if capture != 'failure' else ''),
                                        })
                self.assertEqual(result.returncode, 0 if mutation == 'clean' else 1,
                                 result.stdout + result.stderr)
                if capture == 'failure' and replacement != 'diff':
                    self.assertIn('failed to inspect', result.stderr)
                    self.assertFalse(summary.exists())
                else:
                    self.assertIn('Post-test repository integrity', summary.read_text())
                    if mutation != 'clean' and capture != 'failure':
                        self.assertIn('unexpected.txt', summary.read_text())
                        self.assertIn('unexpected.txt', result.stderr)
                    if replacement and capture != 'failure':
                        self.assertIn('former capture paths mutated', result.stderr)
                        for name in ('status', 'scopes', 'diff'):
                            if capture not in (name, 'all'):
                                continue
                            path = former_captures / f'{name}.txt'
                            if replacement == 'symlink':
                                self.assertTrue(path.is_symlink())
                            elif replacement == 'directory':
                                self.assertTrue(path.is_dir())
                            else:
                                self.assertEqual(path.read_text(), '')
                                if replacement == 'truncate':
                                    self.assertEqual(path.stat().st_ino, original_inodes[name])
                for name in ('generation-drift.txt', 'sqlc-output-directories.txt'):
                    self.assertTrue((runtime / name).is_symlink())
                    self.assertEqual(os.readlink(runtime / name), '/dev/null')
                self.assertEqual(list(runtime.glob('repository-drift.*')), [former_captures])

    def test_clean_and_drifted_trees(self):
        for mutation in ('clean', 'modified', 'deleted', 'untracked', 'ignored', 'go.mod', 'go.sum',
                         'internal', 'internal-untracked', 'internal-ignored',
                         'ignored-go.mod', 'ignored-go.sum', 'stale', 'non-go'):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)

                def git(*args):
                    subprocess.run(['git', '-C', str(root), *args], check=True,
                                   capture_output=True)

                git('init', '-b', 'master')
                git('config', 'user.name', 'Fixture')
                git('config', 'user.email', 'fixture@example.invalid')
                store = root / 'pkg/store/auth'
                store.mkdir(parents=True)
                model = store / 'models.go'
                model.write_text('// generated\n')
                (root / 'go.mod').write_text('module fixture\n')
                (root / 'go.sum').write_text('fixture\n')
                (root / '.gitignore').write_text('ignored.go\n')
                internal = root / 'internal/writer/db'
                internal.mkdir(parents=True)
                (internal / 'models.go').write_text('// generated\n')
                (root / 'sqlc.yaml').write_text(json.dumps({'sql': [
                    {'gen': {'go': {'out': out}}}
                    for out in ('pkg/store/auth', 'internal/writer/db')]}))
                if mutation in ('stale', 'non-go'):
                    (store / ('stale.go' if mutation == 'stale' else 'notes.txt')).write_text('stale\n')
                git('add', '--', 'pkg/store/auth', 'internal/writer/db', 'sqlc.yaml',
                    'go.mod', 'go.sum', '.gitignore')
                git('commit', '-m', 'fixture')
                if mutation == 'modified':
                    model.write_text('// altered\n')
                elif mutation == 'deleted':
                    model.unlink()
                elif mutation in ('untracked', 'ignored'):
                    (store / (mutation + '.go')).write_text('// new\n')
                elif mutation in ('go.mod', 'go.sum'):
                    (root / mutation).write_text('altered\n')
                elif mutation == 'internal':
                    (internal / 'models.go').write_text('// altered\n')
                elif mutation in ('internal-untracked', 'internal-ignored'):
                    (internal / ('ignored.go' if mutation == 'internal-ignored' else 'new.go')).write_text('// new\n')
                elif mutation in ('ignored-go.mod', 'ignored-go.sum'):
                    dependency = mutation.removeprefix('ignored-')
                    git('rm', '--cached', dependency)
                    (root / '.gitignore').write_text('ignored.go\n' + dependency + '\n')
                    git('add', '--', '.gitignore')
                    git('commit', '-m', 'ignore dependency fixture')
                elif mutation in ('stale', 'non-go'):
                    subprocess.run(['python3', str(OUTPUT_HELPER), 'clean'], cwd=root, check=True,
                                   capture_output=True)
                    model.write_text('// generated\n')
                    (internal / 'models.go').write_text('// generated\n')
                    artifact = root / 'artifact'
                    subprocess.run(['python3', str(OUTPUT_HELPER), 'stage', '--destination', str(artifact)],
                                   cwd=root, check=True, capture_output=True)
                    self.assertEqual(sorted(p.relative_to(artifact).as_posix()
                                            for p in artifact.rglob('*') if p.is_file()),
                                     ['internal/writer/db/models.go', 'pkg/store/auth/models.go'])
                summary = root / 'summary.md'
                result = subprocess.run(['bash', str(SCRIPT)], cwd=root, capture_output=True,
                                        text=True, env={**os.environ, 'RUNNER_TEMP': str(root),
                                                        'GITHUB_STEP_SUMMARY': str(summary)})
                self.assertEqual(result.returncode, 0 if mutation == 'clean' else 1)
                self.assertIn('Generation drift', summary.read_text())
                if mutation != 'clean':
                    expected = (mutation.removeprefix('ignored-') if mutation.startswith('ignored-go.') else
                                'internal/writer/db/' if mutation.startswith('internal') else
                                mutation if mutation in ('go.mod', 'go.sum') else 'pkg/store/auth/')
                    self.assertIn(expected, summary.read_text())
