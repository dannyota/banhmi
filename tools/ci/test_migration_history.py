"""Hosted Git fixture regressions for immutable inherited SQL."""
import subprocess
import tempfile
import unittest
from pathlib import Path
from check_migration_history import ROOTS, HistoryError, check


class MigrationHistoryTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        self.git('init', '-b', 'master')
        self.git('config', 'user.name', 'Fixture')
        self.git('config', 'user.email', 'fixture@example.invalid')
        for root in ROOTS:
            path = self.root / root
            path.mkdir(parents=True)
            (path / self.name(root, 1)).write_text('SELECT 1;\n')
        self.base = self.commit()

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args], text=True).strip()

    def name(self, root, version):
        return f'{version:014d}_init.sql' if root.endswith('authmigrations') else f'{version:05d}_init.sql'

    def commit(self):
        paths = [path for path in self.git('ls-files', '-co', '--exclude-standard', '-z').split('\0') if path]
        self.git('add', '--', *paths)
        self.git('commit', '-m', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def test_old_modification_deletion_rename_and_restored_edit(self):
        for root in ROOTS:
            for mutation in ('edit', 'delete', 'rename', 'restore'):
                with self.subTest(root=root, mutation=mutation):
                    self.git('reset', '--hard', self.base)
                    path = self.root / root / self.name(root, 1)
                    if mutation == 'delete':
                        path.unlink()
                    elif mutation == 'rename':
                        path.rename(path.with_name(self.name(root, 2)))
                    else:
                        path.write_text('SELECT 9;\n')
                    head = self.commit()
                    if mutation == 'restore':
                        path.write_text('SELECT 1;\n')
                        head = self.commit()
                    with self.assertRaises(HistoryError):
                        check(self.base, head, True, self.root)

    def test_new_unmerged_files_are_editable(self):
        for root in ROOTS:
            (self.root / root / self.name(root, 2)).write_text('SELECT 2;\n')
        self.commit()
        for root in ROOTS:
            (self.root / root / self.name(root, 2)).write_text('SELECT 3;\n')
        check(self.base, self.commit(), True, self.root)

    def test_invalid_versions_and_padding(self):
        for name in ('00001_collision.sql', '00000_earlier.sql', '100000_higher.sql'):
            self.git('reset', '--hard', self.base)
            (self.root / ROOTS[0] / name).write_text('SELECT 2;\n')
            with self.assertRaises(HistoryError):
                check(self.base, self.commit(), True, self.root)

    def test_missing_history(self):
        with self.assertRaises(HistoryError):
            check('0' * 40, self.base, True, self.root)

    def test_same_tree(self):
        check(self.base, self.base, True, self.root)

    def test_divergence_and_advancing_base(self):
        self.git('checkout', '-b', 'feature')
        (self.root / ROOTS[0] / self.name(ROOTS[0], 2)).write_text('SELECT 2;\n')
        feature = self.commit()
        self.git('checkout', 'master')
        (self.root / ROOTS[1] / self.name(ROOTS[1], 2)).write_text('SELECT 2;\n')
        advanced = self.commit()
        self.git('checkout', 'feature')
        self.git('merge', '--no-edit', 'master')
        check(advanced, self.git('rev-parse', 'HEAD'), True, self.root)
        with self.assertRaises(HistoryError):
            check(advanced, feature, True, self.root)

    def test_earlier_nonzero_append(self):
        (self.root / ROOTS[0] / self.name(ROOTS[0], 3)).write_text('SELECT 3;\n')
        base = self.commit()
        (self.root / ROOTS[0] / self.name(ROOTS[0], 2)).write_text('SELECT 2;\n')
        with self.assertRaises(HistoryError):
            check(base, self.commit(), True, self.root)

    def test_unrecognized_sql_location_and_file_types(self):
        for mutation in ('nested', 'symlink', 'executable'):
            with self.subTest(mutation=mutation):
                self.git('reset', '--hard', self.base)
                path = self.root / ROOTS[0] / self.name(ROOTS[0], 1)
                if mutation == 'nested':
                    path = self.root / 'deploy/migrations/unknown/00001_init.sql'
                    path.parent.mkdir()
                    path.write_text('SELECT 1;\n')
                elif mutation == 'symlink':
                    path.unlink()
                    path.symlink_to('/missing-fixture.sql')
                else:
                    path.chmod(0o755)
                with self.assertRaises(HistoryError):
                    check(self.base, self.commit(), True, self.root)

    def test_diverged_history_with_preserved_sql(self):
        self.git('checkout', '-b', 'feature')
        (self.root / ROOTS[0] / self.name(ROOTS[0], 2)).write_text('SELECT 2;\n')
        feature = self.commit()
        self.git('checkout', 'master')
        (self.root / 'unrelated.txt').write_text('base-only change\n')
        advanced = self.commit()
        check(advanced, feature, True, self.root)

    def test_inherited_base_addition_is_frozen(self):
        self.git('checkout', '-b', 'feature')
        (self.root / 'unrelated.txt').write_text('feature change\n')
        self.commit()
        self.git('checkout', 'master')
        path = self.root / ROOTS[0] / self.name(ROOTS[0], 2)
        path.write_text('SELECT 2;\n')
        advanced = self.commit()
        self.git('checkout', 'feature')
        self.git('merge', '--no-edit', 'master')
        path.write_text('SELECT 9;\n')
        self.commit()
        path.write_text('SELECT 2;\n')
        with self.assertRaises(HistoryError):
            check(advanced, self.commit(), True, self.root)

    def test_missing_and_ambiguous_ancestry(self):
        tree = self.git('rev-parse', self.base + '^{tree}')
        orphan = self.git('commit-tree', tree, '-m', 'orphan')
        with self.assertRaises(HistoryError):
            check(self.base, orphan, True, self.root)
        left = self.git('commit-tree', tree, '-p', self.base, '-m', 'left')
        right = self.git('commit-tree', tree, '-p', self.base, '-m', 'right')
        merge_left = self.git('commit-tree', tree, '-p', left, '-p', right, '-m', 'merge-left')
        merge_right = self.git('commit-tree', tree, '-p', right, '-p', left, '-m', 'merge-right')
        with self.assertRaises(HistoryError):
            check(merge_left, merge_right, True, self.root)

    def test_missing_root_and_invalid_extension(self):
        path = self.root / ROOTS[0] / self.name(ROOTS[0], 1)
        path.rename(path.with_suffix('.SQL'))
        with self.assertRaises(HistoryError):
            check(self.base, self.commit(), False, self.root)
