"""Hosted regressions for generated, ignored, deleted, and dependency drift."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('reject_generation_drift.sh').resolve()
OUTPUT_HELPER = SCRIPT.with_name('sqlc_outputs.py')


class GenerationDriftTest(unittest.TestCase):
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
