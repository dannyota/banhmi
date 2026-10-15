"""Hosted regression for background writers in disposable Git repositories."""
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

from git_test_helpers import init_git_repository


class GitFixtureTest(unittest.TestCase):
    def test_commits_and_fetch_do_not_start_automatic_maintenance(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            config = root / 'gitconfig'
            config.write_text('''[user]
    name = Fixture
    email = fixture@example.invalid
[commit]
    gpgsign = false
[gc]
    auto = 1
    autoDetach = true
[maintenance]
    auto = true
    autoDetach = true
''')
            trace = root / 'trace.json'
            env = {'PATH': os.defpath, 'GIT_CONFIG_GLOBAL': str(config),
                   'GIT_CONFIG_NOSYSTEM': '1', 'GIT_TRACE2_EVENT': str(trace)}
            source = root / 'source'
            target = root / 'target'
            for repository in (source, target):
                repository.mkdir()
                init_git_repository(repository, '-b', 'master', env=env)
                # Fresh Git processes must inherit the repository's persisted settings.
                subprocess.run(['git', '-C', str(repository), 'commit', '--allow-empty',
                                '-m', 'fixture'], env=env, check=True, capture_output=True)
            subprocess.run(['git', '-C', str(target), 'fetch', '--no-tags', str(source),
                            'refs/heads/master'], env=env, check=True, capture_output=True)
            events = [json.loads(line) for line in trace.read_text().splitlines()]
            self.assertTrue(any(event.get('event') == 'start' and 'fetch' in event['argv']
                                for event in events))
            children = [event['argv'] for event in events if event.get('event') == 'child_start']
            self.assertEqual([args for args in children if 'maintenance' in args or 'gc' in args], [])
