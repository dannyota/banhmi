import json
import hashlib
import copy
import tempfile
import unittest
from pathlib import Path
from corpus_generation import GenerationError, select, validate_manifest, validate_tests


def complete_manifest(files):
    return {'schema_version': 1, 'lane': 'corpus', 'base': 'a'*40, 'candidate': 'b'*40,
            'pr_head': None, 'event': 'push', 'run': '123', 'attempt': '1',
            'history_checked': False, 'same_tree': False, 'migration_additions': [],
            'go': 'go fixture', 'sqlc': 'sqlc fixture', 'goose': 'goose fixture',
            'verification_sha256': hashlib.sha256(b'verification').hexdigest(), 'files': files}


class CorpusGenerationTest(unittest.TestCase):
    def test_wrong_lane_and_sql_are_rejected(self):
        for path in ('pkg/store/auth/models.go', 'deploy/migrations/bronze/00002_x.sql', '../models.go'):
            with self.subTest(path=path), self.assertRaises(GenerationError):
                validate_manifest(complete_manifest([{'path': path, 'change': 'A', 'sha256': 'a'*64}]), 'corpus')

    def test_malformed_and_deleted_artifact_records(self):
        for record in ({'path': 'pkg/store/bronze/models.go', 'change': 'M', 'sha256': None},
                       {'path': 'pkg/store/bronze/models.go', 'change': 'D', 'sha256': 'a'*64}):
            with self.assertRaises(GenerationError):
                validate_manifest(complete_manifest([record]), 'corpus')
        validate_manifest(complete_manifest([{'path': 'pkg/store/bronze/models.go', 'change': 'D', 'sha256': None}]), 'corpus')

    def test_missing_and_nested_skipped_suites_fail(self):
        valid = [{'Action': 'pass', 'Test': name} for name in ('TestOne', 'TestTwo')]
        validate_tests(valid, ('TestOne', 'TestTwo'), 'race', 'rollback')
        for events in (valid[:1], valid + [{'Action': 'skip', 'Test': 'TestOne/nested'}], valid + [valid[0]], []):
            with self.assertRaises(GenerationError):
                validate_tests(events, ('TestOne', 'TestTwo'), 'race', 'rollback')
        with self.assertRaises(GenerationError):
            validate_tests(valid, ('TestOne', 'TestTwo'), 'same', 'same')

    def test_selection_uses_output_identity(self):
        config = 'version: "2"\nsql:\n  - engine: postgresql\n    schema: anything\n    gen:\n      go:\n        package: dbauth\n        out: "pkg/store/auth"\n'
        self.assertIn('schema: anything', select(config, 'auth'))
        with self.assertRaises(GenerationError):
            select(config.replace('pkg/store/auth', 'pkg/store/other'), 'auth')


class ArtifactRegressionTest(unittest.TestCase):
    def test_untracked_and_deleted_output_are_detected(self):
        from unittest.mock import patch
        from corpus_generation import changes
        with patch('corpus_generation.git', side_effect=[b'pkg/store/bronze/deleted.go\0', b'pkg/store/bronze/new.go\0', b'']):
            self.assertEqual(changes('corpus'), {'pkg/store/bronze/deleted.go', 'pkg/store/bronze/new.go'})
        for path in ('pkg/store/auth/untracked.go', 'sql/bronze/queries.sql', 'deploy/migrations/bronze/00002_x.sql'):
            with patch('corpus_generation.git', side_effect=[b'', path.encode() + b'\0', b'']):
                with self.assertRaises(GenerationError):
                    changes('corpus')

    def test_deleted_and_added_manifest_round_trip(self):
        import os
        import subprocess
        from unittest.mock import patch
        from corpus_generation import SCHEMAS, stage
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            for name in SCHEMAS:
                directory = root / 'pkg/store' / name
                directory.mkdir(parents=True)
                (directory / 'models.go').write_text('// old\n')
            subprocess.run(['git', 'init', '-b', 'master', str(root)], check=True, capture_output=True)
            subprocess.run(['git', '-C', str(root), 'add', '--',
                            *(f'pkg/store/{name}/models.go' for name in SCHEMAS)], check=True)
            subprocess.run(['git', '-C', str(root), '-c', 'user.name=Fixture', '-c', 'user.email=fixture@example.invalid',
                            'commit', '-m', 'fixture'], check=True, capture_output=True)
            (root / 'pkg/store/bronze/models.go').unlink()
            (root / 'pkg/store/bronze/new.go').write_text('// new\n')
            validation = root / 'validation'
            validation.mkdir()
            head = subprocess.check_output(['git', '-C', str(root), 'rev-parse', 'HEAD'], text=True).strip()
            history = complete_manifest([])
            for field in ('schema_version', 'lane', 'go', 'sqlc', 'goose', 'verification_sha256', 'files'):
                history.pop(field)
            history['candidate'] = head
            (validation / 'history.json').write_text(json.dumps(history))
            (validation / 'event.json').write_text(json.dumps({'before': 'a'*40}))
            (validation / 'corpus-verification.md').write_text('verification')
            previous = Path.cwd()
            try:
                os.chdir(root)
                # Exclude synthetic validation output from the Git output fixture.
                (root / '.git/info/exclude').write_text('validation/\nartifact/\nartifact-mismatch/\n')
                with patch.dict(os.environ, {'VALIDATION_DIR': str(validation), 'BASE_SHA': 'a'*40,
                                            'CANDIDATE_SHA': head, 'GITHUB_SHA': head,
                                            'GITHUB_EVENT_PATH': str(validation / 'event.json'),
                                            'GITHUB_EVENT_NAME': 'push', 'GITHUB_RUN_ID': '123',
                                            'GITHUB_RUN_ATTEMPT': '1'}), \
                     patch('corpus_generation.subprocess.check_output', wraps=subprocess.check_output) as command:
                    def dispatch(args, **kwargs):
                        if args in (['sqlc', 'version'], ['go', 'version'], ['go', 'list', '-m', 'github.com/pressly/goose/v3']):
                            return 'fixture-version\n'
                        return subprocess.run(args, capture_output=True, check=True).stdout
                    command.side_effect = dispatch
                    stage('corpus', root / 'artifact')
                manifest = json.loads((root / 'artifact/manifest.json').read_text())
                records = {entry['path']: entry for entry in manifest['files']}
                self.assertEqual(records['pkg/store/bronze/models.go']['change'], 'D')
                self.assertIsNone(records['pkg/store/bronze/models.go']['sha256'])
                self.assertEqual(records['pkg/store/bronze/new.go']['change'], 'A')
                self.assertFalse((root / 'artifact/pkg/store/bronze/models.go').exists())
                # A saved history file cannot substitute another candidate SHA.
                history['candidate'] = 'b'*40
                (validation / 'history.json').write_text(json.dumps(history))
                with patch.dict(os.environ, {'VALIDATION_DIR': str(validation)}), self.assertRaisesRegex(GenerationError, 'candidate'):
                    stage('corpus', root / 'artifact-mismatch')
            finally:
                os.chdir(previous)


class DatabaseNegativeContractsTest(unittest.TestCase):
    def test_omitted_function_or_trigger_requires_actual_test_failure(self):
        from corpus_generation import validate_negative_tests
        name = 'TestImmutableEvidenceAndReviewConstraints'
        validate_negative_tests([{'Action': 'fail', 'Test': name}])
        for events in ([], [{'Action': 'fail'}], [{'Action': 'pass', 'Test': name}],
                       [{'Action': 'skip', 'Test': name}], [{'Action': 'fail', 'Test': name}, {'Action': 'skip', 'Test': name + '/nested'}]):
            with self.assertRaises(GenerationError):
                validate_negative_tests(events)


class ManifestFileContractsTest(unittest.TestCase):
    def test_mismatched_missing_and_unlisted_files_fail(self):
        import hashlib
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            path = output / 'pkg/store/bronze/models.go'
            path.parent.mkdir(parents=True)
            path.write_bytes(b'// generated\n')
            manifest = complete_manifest([{'path': 'pkg/store/bronze/models.go', 'change': 'A',
                                          'sha256': hashlib.sha256(path.read_bytes()).hexdigest()}])
            (output / 'verification.md').write_bytes(b'verification')
            validate_manifest(manifest, 'corpus', output)
            path.write_bytes(b'// substituted\n')
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)
            path.unlink()
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)
            path.write_bytes(b'// generated\n')
            (output / 'unexpected.sql').write_bytes(b'SELECT 1;')
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)


class ProvenanceContractsTest(unittest.TestCase):
    def setUp(self):
        self.manifest = complete_manifest([{'path': 'pkg/store/bronze/models.go', 'change': 'A', 'sha256': 'c'*64}])
        self.expected = {field: self.manifest[field] for field in
                         ('base', 'candidate', 'event', 'run', 'attempt', 'pr_head', 'history_checked')}

    def test_missing_and_altered_provenance_fails(self):
        validate_manifest(self.manifest, 'corpus', expected=self.expected)
        for field in self.manifest:
            candidate = copy.deepcopy(self.manifest)
            candidate.pop(field)
            with self.subTest(missing=field), self.assertRaises(GenerationError):
                validate_manifest(candidate, 'corpus', expected=self.expected)
        mutations = {'base': 'd'*40, 'candidate': 'd'*40, 'run': '456', 'attempt': '2',
                     'event': 'workflow_dispatch', 'lane': 'auth', 'schema_version': 2,
                     'pr_head': 'd'*40, 'history_checked': True, 'same_tree': 'false',
                     'go': '', 'sqlc': '', 'goose': '', 'verification_sha256': None}
        for field, value in mutations.items():
            candidate = copy.deepcopy(self.manifest)
            candidate[field] = value
            with self.subTest(altered=field), self.assertRaises(GenerationError):
                validate_manifest(candidate, 'corpus', expected=self.expected)

    def test_report_bytes_presence_and_regular_file_are_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            output = Path(temporary)
            file = output / 'pkg/store/bronze/models.go'
            file.parent.mkdir(parents=True)
            file.write_bytes(b'// generated\n')
            manifest = complete_manifest([{'path': 'pkg/store/bronze/models.go', 'change': 'A',
                                          'sha256': hashlib.sha256(file.read_bytes()).hexdigest()}])
            report = output / 'verification.md'
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)
            report.write_bytes(b'verification')
            validate_manifest(manifest, 'corpus', output)
            report.write_bytes(b'altered report')
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)
            report.unlink()
            report.symlink_to(file)
            with self.assertRaises(GenerationError):
                validate_manifest(manifest, 'corpus', output)


class ResolvedEventProvenanceTest(unittest.TestCase):
    def test_event_base_and_checkout_binding(self):
        import os
        from unittest.mock import patch
        from corpus_generation import expected_provenance
        with tempfile.TemporaryDirectory() as temporary:
            event = Path(temporary) / 'event.json'
            event.write_text(json.dumps({'before': 'a'*40}))
            env = {'BASE_SHA': 'a'*40, 'CANDIDATE_SHA': 'b'*40, 'GITHUB_SHA': 'b'*40,
                   'GITHUB_EVENT_NAME': 'push', 'GITHUB_EVENT_PATH': str(event),
                   'GITHUB_RUN_ID': '123', 'GITHUB_RUN_ATTEMPT': '1'}
            with patch.dict(os.environ, env):
                self.assertEqual(expected_provenance()['base'], 'a'*40)
                for field in ('BASE_SHA', 'CANDIDATE_SHA'):
                    with patch.dict(os.environ, {field: 'c'*40}), self.assertRaises(GenerationError):
                        expected_provenance()
