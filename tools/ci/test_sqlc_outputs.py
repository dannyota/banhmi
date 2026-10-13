"""Hosted regressions for sqlc output containment, cleanup, and artifact scope."""
import tempfile
import unittest
from pathlib import Path

from sqlc_outputs import discover_outputs, clean_outputs, stage_outputs


def config(*outputs):
    return {'sql': [{'gen': {'go': {'out': out}}} for out in outputs]}


class SqlcOutputsTest(unittest.TestCase):
    def test_all_entries_including_internal_and_duplicates(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            self.assertEqual(discover_outputs(config('pkg/store/auth', 'internal/writer/db',
                                                     'pkg/store/auth'), root),
                             ['internal/writer/db', 'pkg/store/auth'])

    def test_unsafe_outputs_fail_before_cleanup(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / 'link').symlink_to(root / 'elsewhere', target_is_directory=True)
            (root / 'file').write_text('not a directory\n')
            for out in ('', '.', '..', '../outside', '/absolute', 'pkg/../sql',
                        '.git/objects', 'link/db', 'file/db', 'pkg/store\nother', 'pkg/*', 42):
                with self.subTest(out=out), self.assertRaises(ValueError):
                    discover_outputs(config(out), root)
            for value in ({}, {'sql': []}, {'sql': [{'gen': {'go': {}}}]}):
                with self.subTest(value=value), self.assertRaises(ValueError):
                    discover_outputs(value, root)

    def test_unsafe_custom_filenames_fail_during_discovery(self):
        fields = ('output_db_file_name', 'output_models_file_name',
                  'output_querier_file_name', 'output_copyfrom_file_name',
                  'output_batch_file_name')
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for field in fields:
                for filename in ('../db-extra/models.go', '/absolute/models.go',
                                 r'db-extra\models.go', '.', '..', '', 'models.txt',
                                 'models*.go', 42):
                    value = config('internal/writer/db')
                    value['sql'][0]['gen']['go'][field] = filename
                    with self.subTest(field=field, filename=filename), self.assertRaises(ValueError):
                        discover_outputs(value, root)

    def test_unsafe_custom_suffixes_fail_during_discovery(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for suffix in ('/extra', r'\extra', '..', 'custom..suffix', 42):
                value = config('internal/writer/db')
                value['sql'][0]['gen']['go']['output_files_suffix'] = suffix
                with self.subTest(suffix=suffix), self.assertRaises(ValueError):
                    discover_outputs(value, root)

    def test_valid_custom_filenames_and_suffix_pass(self):
        with tempfile.TemporaryDirectory() as temporary:
            value = config('internal/writer/db')
            value['sql'][0]['gen']['go'].update({
                'output_db_file_name': 'custom-db.go',
                'output_models_file_name': 'custom_models.go',
                'output_querier_file_name': 'custom.querier.go',
                'output_copyfrom_file_name': 'custom-copyfrom.go',
                'output_batch_file_name': 'custom-batch.go',
                'output_files_suffix': '_custom',
            })
            self.assertEqual(discover_outputs(value, Path(temporary)), ['internal/writer/db'])

    def test_cleanup_removes_stale_and_unrelated_files_and_stages_only_regenerated_files(self):
        with tempfile.TemporaryDirectory() as temporary, tempfile.TemporaryDirectory() as artifacts:
            root = Path(temporary)
            out = root / 'internal/writer/db'
            out.mkdir(parents=True)
            (out / 'stale.go').write_text('// obsolete committed output\n')
            (out / 'README.txt').write_text('unrelated committed file\n')
            outputs = discover_outputs(config('internal/writer/db'), root)
            clean_outputs(root, outputs)
            self.assertEqual(list(out.iterdir()), [])
            (out / 'models.go').write_text('// regenerated\n')
            (out / '.hidden.go').write_text('// regenerated hidden output\n')
            stage_outputs(root, outputs, Path(artifacts) / 'tree')
            self.assertEqual(sorted(p.relative_to(Path(artifacts) / 'tree').as_posix()
                                    for p in (Path(artifacts) / 'tree').rglob('*') if p.is_file()),
                             ['internal/writer/db/.hidden.go', 'internal/writer/db/models.go'])

    def test_artifact_rejects_symlinks_and_empty_output(self):
        with tempfile.TemporaryDirectory() as temporary, tempfile.TemporaryDirectory() as artifacts:
            root = Path(temporary)
            out = root / 'pkg/store/auth'
            out.mkdir(parents=True)
            outputs = discover_outputs(config('pkg/store/auth'), root)
            with self.assertRaises(ValueError):
                stage_outputs(root, outputs, Path(artifacts) / 'empty')
            (out / 'models.go').symlink_to(root / 'outside.go')
            with self.assertRaises(ValueError):
                stage_outputs(root, outputs, Path(artifacts) / 'symlink')
