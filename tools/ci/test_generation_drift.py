"""Hosted regressions for generated, ignored, deleted, and dependency drift."""
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name('reject_generation_drift.sh').resolve()


class GenerationDriftTest(unittest.TestCase):
    def test_clean_and_drifted_trees(self):
        for mutation in ('clean', 'modified', 'deleted', 'untracked', 'ignored', 'go.mod', 'go.sum'):
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
                git('add', '--', 'pkg/store/auth/models.go', 'go.mod', 'go.sum', '.gitignore')
                git('commit', '-m', 'fixture')
                if mutation == 'modified':
                    model.write_text('// altered\n')
                elif mutation == 'deleted':
                    model.unlink()
                elif mutation in ('untracked', 'ignored'):
                    (store / (mutation + '.go')).write_text('// new\n')
                elif mutation in ('go.mod', 'go.sum'):
                    (root / mutation).write_text('altered\n')
                summary = root / 'summary.md'
                result = subprocess.run(['bash', str(SCRIPT)], cwd=root, capture_output=True,
                                        text=True, env={**os.environ, 'RUNNER_TEMP': str(root),
                                                        'GITHUB_STEP_SUMMARY': str(summary)})
                self.assertEqual(result.returncode, 0 if mutation == 'clean' else 1)
                self.assertIn('Generation drift', summary.read_text())
                if mutation != 'clean':
                    expected = mutation if mutation in ('go.mod', 'go.sum') else 'pkg/store/auth/'
                    self.assertIn(expected, summary.read_text())
