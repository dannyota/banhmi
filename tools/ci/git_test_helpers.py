"""Shared setup for disposable Git repositories used only by hosted tests."""
import subprocess


def init_git_repository(root, *args, env=None):
    subprocess.run(['git', '-C', str(root), 'init', *args], env=env,
                   check=True, capture_output=True)
    # Persist these before any commits or fetches, including those in scripts under test.
    # Waiting for a Git command alone does not wait for detached maintenance writers.
    for key, value in (('gc.auto', '0'), ('maintenance.auto', 'false'),
                       ('gc.autoDetach', 'false'), ('maintenance.autoDetach', 'false')):
        subprocess.run(['git', '-C', str(root), 'config', '--local', key, value],
                       env=env, check=True, capture_output=True)
