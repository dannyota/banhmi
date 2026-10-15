"""Hosted Git fixture regressions for frozen corpus and append-only auth SQL."""
import subprocess
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch
from check_migration_history import AUTH_ROOT, FROZEN_ROOTS, ROOTS, HistoryError, check, check_event
from git_test_helpers import init_git_repository


class MigrationHistoryTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.root = Path(self.temp.name)
        init_git_repository(self.root, '-b', 'master')
        self.git('config', 'user.name', 'Fixture')
        self.git('config', 'user.email', 'fixture@example.invalid')
        for root in ROOTS:
            path = self.root / root
            path.mkdir(parents=True)
            (path / self.name(root, 1)).write_text('SELECT 1;\n')
        bootstrap = self.root / AUTH_ROOT / 'bootstrap/grant_roles.sql'
        bootstrap.parent.mkdir()
        bootstrap.write_text('SELECT 1;\n')
        self.base = self.commit()
        self.git('remote', 'add', 'origin', str(self.root))
        # Keep the protected baseline independent of subsequent fixture commits.
        self.git('checkout', '-b', 'candidate')

    def tearDown(self):
        self.temp.cleanup()

    def git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args], text=True).strip()

    def name(self, root, version):
        return f'{version:014d}_init.sql' if root == AUTH_ROOT else f'{version:05d}_init.sql'

    def commit(self):
        paths = [path for path in self.git('ls-files', '-co', '--exclude-standard', '-z').split('\0') if path]
        self.git('add', '--', *paths)
        self.git('commit', '-m', 'fixture')
        return self.git('rev-parse', 'HEAD')

    def test_existing_modification_deletion_and_rename_fail(self):
        paths = [self.root / root / self.name(root, 1) for root in ROOTS]
        paths.append(self.root / AUTH_ROOT / 'bootstrap/grant_roles.sql')
        for path in paths:
            for mutation in ('edit', 'delete', 'rename'):
                with self.subTest(path=path, mutation=mutation):
                    self.git('reset', '--hard', self.base)
                    if mutation == 'delete':
                        path.unlink()
                    elif mutation == 'rename':
                        path.rename(path.with_name('renamed.sql'))
                    else:
                        path.write_text('SELECT 9;\n')
                    with self.assertRaises(HistoryError):
                        check(self.base, self.commit(), self.root)

    def test_frozen_additions_fail_in_every_root(self):
        for root in FROZEN_ROOTS:
            self.git('reset', '--hard', self.base)
            (self.root / root / self.name(root, 2)).write_text('SELECT 2;\n')
            with self.subTest(root=root), self.assertRaises(HistoryError):
                check(self.base, self.commit(), self.root)

    def test_complete_frozen_subtree_is_immutable(self):
        for mutation in ('added', 'symlink', 'mode', 'deleted'):
            with self.subTest(mutation=mutation):
                self.git('reset', '--hard', self.base)
                path = self.root / FROZEN_ROOTS[0] / 'notes.txt'
                path.write_text('frozen non-SQL bytes\n')
                base = self.commit()
                if mutation == 'added':
                    path.with_name('extra.txt').write_text('extra\n')
                elif mutation == 'symlink':
                    path.unlink()
                    path.symlink_to('00001_init.sql')
                elif mutation == 'mode':
                    path.chmod(0o755)
                else:
                    path.unlink()
                with self.assertRaises(HistoryError):
                    check(base, self.commit(), self.root)

    def test_non_master_base_cannot_authorize_inherited_changes(self):
        for directory in (FROZEN_ROOTS[0], AUTH_ROOT):
            with self.subTest(directory=directory):
                self.git('reset', '--hard', self.base)
                (self.root / directory / self.name(directory, 1)).write_text('SELECT 9;\n')
                base = self.commit()
                (self.root / 'unrelated.txt').write_text('candidate\n')
                head = self.commit()
                event = {'pull_request': {'base': {'sha': base}, 'head': {'sha': head}}}
                with self.assertRaises(HistoryError):
                    check_event(event, 'pull_request', head, self.root)

    def test_non_master_base_agrees_with_master(self):
        (self.root / 'unrelated.txt').write_text('base\n')
        base = self.commit()
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 2)).write_text('SELECT 2;\n')
        head = self.commit()
        event = {'pull_request': {'base': {'sha': base}, 'head': {'sha': head}}}
        report = check_event(event, 'pull_request', head, self.root)
        self.assertEqual(report['base'], base)
        self.assertEqual(report['master'], self.base)

    def test_existing_auth_non_sql_file_is_immutable(self):
        path = self.root / AUTH_ROOT / 'notes.txt'
        path.write_text('inherited\n')
        base = self.commit()
        path.write_text('altered\n')
        with self.assertRaises(HistoryError):
            check(base, self.commit(), self.root)

    def test_event_base_auth_append_order_still_applies(self):
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 3)).write_text('SELECT 3;\n')
        base = self.commit()
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 2)).write_text('SELECT 2;\n')
        head = self.commit()
        event = {'pull_request': {'base': {'sha': base}, 'head': {'sha': head}}}
        with self.assertRaises(HistoryError):
            check_event(event, 'pull_request', head, self.root)

    def test_auth_addition_can_be_edited_before_acceptance(self):
        path = self.root / AUTH_ROOT / self.name(AUTH_ROOT, 2)
        path.write_text('SELECT 2;\n')
        self.commit()
        path.write_text('SELECT 3;\n')
        check(self.base, self.commit(), self.root)

    def test_restored_edit_and_same_tree_pass(self):
        for root in ROOTS:
            path = self.root / root / self.name(root, 1)
            path.write_text('SELECT 9;\n')
        self.commit()
        for root in ROOTS:
            (self.root / root / self.name(root, 1)).write_text('SELECT 1;\n')
        check(self.base, self.commit(), self.root)
        check(self.base, self.base, self.root)

    def test_invalid_auth_versions_and_padding(self):
        for name in ('00000000000001_collision.sql', '00000000000000_earlier.sql',
                     '100000000000000_higher.sql'):
            self.git('reset', '--hard', self.base)
            (self.root / AUTH_ROOT / name).write_text('SELECT 2;\n')
            with self.subTest(name=name), self.assertRaises(HistoryError):
                check(self.base, self.commit(), self.root)

    def test_earlier_nonzero_auth_append_fails(self):
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 3)).write_text('SELECT 3;\n')
        base = self.commit()
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 2)).write_text('SELECT 2;\n')
        with self.assertRaises(HistoryError):
            check(base, self.commit(), self.root)

    def test_missing_history_and_short_refs_fail(self):
        for ref in ('0' * 40, self.base[:7], 'master', 'A' * 40):
            with self.subTest(ref=ref), self.assertRaises(HistoryError):
                check(ref, self.base, self.root)

    def test_unrecognized_location_file_type_and_extension_fail(self):
        for mutation in ('nested', 'symlink', 'executable', 'extension'):
            self.git('reset', '--hard', self.base)
            path = self.root / ROOTS[0] / self.name(ROOTS[0], 1)
            if mutation == 'nested':
                path = self.root / 'deploy/migrations/unknown/00001_init.sql'
                path.parent.mkdir()
                path.write_text('SELECT 1;\n')
            elif mutation == 'symlink':
                path.unlink()
                path.symlink_to('/missing-fixture.sql')
            elif mutation == 'executable':
                path.chmod(0o755)
            else:
                path.rename(path.with_suffix('.SQL'))
            with self.subTest(mutation=mutation), self.assertRaises(HistoryError):
                check(self.base, self.commit(), self.root)

    def test_final_trusted_base_not_merge_base(self):
        self.git('checkout', '-b', 'feature')
        (self.root / 'unrelated.txt').write_text('feature\n')
        feature = self.commit()
        self.git('checkout', 'master')
        (self.root / AUTH_ROOT / self.name(AUTH_ROOT, 2)).write_text('SELECT 2;\n')
        advanced = self.commit()
        with self.assertRaises(HistoryError):
            check(advanced, feature, self.root)
        self.git('checkout', 'feature')
        self.git('merge', '--no-edit', 'master')
        check(advanced, self.git('rev-parse', 'HEAD'), self.root)

    def test_push_and_pr_base_and_checkout_binding(self):
        event = {'before': self.base, 'pull_request': {
            'base': {'sha': self.base}, 'head': {'sha': self.base}}}
        for kind in ('push', 'pull_request'):
            self.assertEqual(check_event(event, kind, self.base, self.root)['base'], self.base)
            with self.assertRaises(HistoryError):
                check_event(event, kind, '0' * 40, self.root)
        event['before'] = 'master'
        with self.assertRaises(HistoryError):
            check_event(event, 'push', self.base, self.root)

    def test_manual_fetches_master_and_unknown_events_fail(self):
        from check_migration_history import git as actual_git
        calls = []

        def fake_git(root, *args):
            calls.append(args)
            if args[0] == 'fetch':
                return b''
            if args == ('rev-parse', 'FETCH_HEAD'):
                return self.base.encode()
            return actual_git(root, *args)

        event = {'repository': {'default_branch': 'master'}}
        with patch('check_migration_history.git', side_effect=fake_git):
            check_event(event, 'workflow_dispatch', self.base, self.root)
        self.assertIn(('fetch', '--no-tags', 'origin', 'refs/heads/master'), calls)
        event['repository']['default_branch'] = 'other'
        with self.assertRaises(HistoryError):
            check_event(event, 'workflow_dispatch', self.base, self.root)
        with self.assertRaises(HistoryError):
            check_event({}, 'workflow_call', self.base, self.root)
