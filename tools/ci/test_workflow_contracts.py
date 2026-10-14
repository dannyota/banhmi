from __future__ import annotations

import copy
import json
import subprocess
import unittest
from pathlib import Path

from check_workflow_contracts import check_required_jobs, check_workflow, check_security_workflows


ROOT = Path(__file__).resolve().parents[2]
PIN = "a" * 40


class WorkflowContractsTest(unittest.TestCase):
    def setUp(self) -> None:
        self.workflow = {"jobs": {
            "check": {"permissions": {"contents": "read"}, "steps": [{
                "uses": f"actions/checkout@{PIN}",
                "with": {"persist-credentials": False},
            }]},
            "ci": {
                "name": "CI", "if": "${{ always() }}", "needs": "check",
                "permissions": {"contents": "read"}, "steps": [{
                    "env": {"REQUIRED_JOBS": "check", "NEEDS_JSON": "${{ toJSON(needs) }}"},
                    "run": "python3 tools/ci/check_results.py",
                }],
            },
        }}

    def test_repository_workflows(self) -> None:
        paths = sorted((ROOT / ".github/workflows").glob("*.y*ml"))
        self.assertTrue(paths)
        workflows = {}
        for path in paths:
            with self.subTest(path=path.name):
                # Use the hosted runner's yq parser, not an indentation approximation.
                result = subprocess.run(
                    ["yq", "-o=json", ".", str(path)],
                    capture_output=True, text=True, check=True,
                )
                workflow = json.loads(result.stdout)
                workflows[path.name] = workflow
                if path.name in ("ci.yml", "reusable-checks.yml"):
                    self.assertIn("ci", workflow["jobs"])
                if path.name == "ci.yml":
                    self.assertEqual(set(workflow["on"]), {
                        "push", "pull_request", "workflow_dispatch",
                    })
                    self.assertEqual(workflow["on"]["push"], {"branches": ["master"]})
                    self.assertIsNone(workflow["on"]["pull_request"])
                if path.name == "ci.yml":
                    prefix = "ci-"
                    self.assertEqual(workflow["concurrency"], {
                        "group": prefix + "${{ github.event_name }}-${{ github.event.pull_request.number || github.run_id }}",
                        "cancel-in-progress": "${{ github.event_name == 'pull_request' }}",
                    })
                if path.name == "reusable-checks.yml":
                    self.assertNotIn("concurrency", workflow)
                self.assertEqual(check_workflow(workflow, ROOT), [])
        inventory = (ROOT / "tools/ci/required-jobs.txt").read_text(encoding="utf-8")
        for scanner in ("gitleaks", "semgrep-sast", "semgrep-sca"):
            self.assertIn(f"reusable-checks.yml {scanner}", inventory.splitlines())
        self.assertEqual(check_required_jobs(workflows, inventory), [])
        self.assertEqual(check_security_workflows(workflows), [])

    def test_security_workflow_mutations(self) -> None:
        workflows = {}
        for name in ("ci.yml", "reusable-checks.yml"):
            result = subprocess.run(["yq", "-o=json", ".", str(ROOT / ".github/workflows" / name)],
                                    capture_output=True, text=True, check=True)
            workflows[name] = json.loads(result.stdout)
        mutations = [
            lambda w: w["ci.yml"]["jobs"]["checks"].update(secrets="inherit"),
            lambda w: w["ci.yml"]["jobs"]["checks"]["secrets"].update(SEMGREP_APP_TOKEN="${{ secrets.SEMGREP_APP_TOKEN }}"),
            lambda w: w["ci.yml"]["on"].update(pull_request_target=None),
            lambda w: w["reusable-checks.yml"]["on"]["workflow_call"].update(inputs={"trusted": {"type": "boolean"}}),
            lambda w: w["reusable-checks.yml"]["on"]["workflow_call"]["secrets"]["SEMGREP_APP_TOKEN"].update(required=True),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sast"].update(env={"SEMGREP_APP_TOKEN": "${{ secrets.SEMGREP_APP_TOKEN }}"}),
            lambda w: w["reusable-checks.yml"]["jobs"]["hygiene"].update(env={"SEMGREP_APP_TOKEN": "${{ secrets.SEMGREP_APP_TOKEN }}"}),
            lambda w: w["reusable-checks.yml"]["jobs"]["gitleaks"]["steps"][0]["with"].update(**{"fetch-depth": 1}),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sca"].update(**{"if": "false"}),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sast"]["steps"][-1].update(**{"if": "false"}),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sca"]["steps"][-1].update(run="echo success"),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sca"].update(**{"timeout-minutes": 0}),
            lambda w: w["reusable-checks.yml"]["jobs"]["semgrep-sast"]["steps"].append({"run": "echo token", "env": {"SEMGREP_APP_TOKEN": "${{ secrets.SEMGREP_APP_TOKEN }}"}}),
        ]
        for mutate in mutations:
            with self.subTest(mutation=mutate):
                candidate = copy.deepcopy(workflows)
                mutate(candidate)
                self.assertTrue(check_security_workflows(candidate))

    def test_accepts_pinned_workflow(self) -> None:
        self.assertEqual(check_workflow(self.workflow, ROOT), [])

    def test_rejects_unpinned_uses_at_job_and_step_level(self) -> None:
        for reference in ("actions/checkout@v7", "actions/checkout@" + "a" * 39,
                          "docker://alpine:latest"):
            for job_level in (False, True):
                with self.subTest(reference=reference, job_level=job_level):
                    workflow = copy.deepcopy(self.workflow)
                    if job_level:
                        workflow["jobs"]["check"]["uses"] = reference
                    else:
                        workflow["jobs"]["check"]["steps"][0]["uses"] = reference
                    self.assertTrue(any("pin" in e for e in check_workflow(workflow, ROOT)))

    def test_rejects_job_without_permissions(self) -> None:
        del self.workflow["jobs"]["check"]["permissions"]
        self.assertTrue(any("permissions" in e for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_write_permissions(self) -> None:
        self.workflow["jobs"]["check"]["permissions"]["contents"] = "write"
        self.assertTrue(any("read-only" in e for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_persisted_or_default_checkout_credentials(self) -> None:
        for value in (True, None):
            with self.subTest(value=value):
                self.workflow["jobs"]["check"]["steps"][0]["with"] = {}
                if value is not None:
                    self.workflow["jobs"]["check"]["steps"][0]["with"]["persist-credentials"] = value
                self.assertTrue(any("persist-credentials" in e
                                    for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_aggregate_missing_another_job(self) -> None:
        self.workflow["jobs"]["added"] = {"permissions": {"contents": "read"}}
        self.assertTrue(any("every other job" in e
                            for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_required_list_drift(self) -> None:
        self.workflow["jobs"]["ci"]["steps"][0]["env"]["REQUIRED_JOBS"] = ""
        self.assertTrue(any("REQUIRED_JOBS" in e
                            for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_conditional_aggregate(self) -> None:
        self.workflow["jobs"]["ci"]["if"] = "success()"
        self.assertTrue(any("always()" in e for e in check_workflow(self.workflow, ROOT)))

    def test_local_reusable_reference_must_exist(self) -> None:
        self.workflow["jobs"]["check"]["uses"] = "./.github/workflows/missing.yml"
        self.assertTrue(any("local workflow" in e
                            for e in check_workflow(self.workflow, ROOT)))

    def test_rejects_conditional_gate(self) -> None:
        for condition in ("false", False, None, "success()", "${{ success() }}"):
            with self.subTest(condition=condition):
                self.workflow["jobs"]["ci"]["steps"][0]["if"] = condition
                self.assertTrue(any("gate condition" in e
                                    for e in check_workflow(self.workflow, ROOT)))

    def test_accepts_unconditional_gate(self) -> None:
        for condition in ("always()", "${{ always() }}"):
            with self.subTest(condition=condition):
                self.workflow["jobs"]["ci"]["steps"][0]["if"] = condition
                self.assertEqual(check_workflow(self.workflow, ROOT), [])

    def test_rejects_continue_on_error_bypasses(self) -> None:
        for location in ("child-job", "child-step", "aggregate", "gate"):
            for value in (True, "false", "${{ false }}", "${{ true }}", None, 0):
                with self.subTest(location=location, value=value):
                    workflow = copy.deepcopy(self.workflow)
                    job_id = "check" if location.startswith("child-") else "ci"
                    node = workflow["jobs"][job_id]
                    if location == "child-step":
                        node["steps"].append({"run": "exit 0"})
                        node = node["steps"][-1]
                    if location == "gate":
                        node = node["steps"][0]
                    node["continue-on-error"] = value
                    self.assertTrue(any("continue-on-error" in e
                                        for e in check_workflow(workflow, ROOT)))

    def test_accepts_literal_false_continue_on_error(self) -> None:
        for job in self.workflow["jobs"].values():
            for node in [job, *job.get("steps", [])]:
                node["continue-on-error"] = False
        self.assertEqual(check_workflow(self.workflow, ROOT), [])

    def test_rejects_continue_on_error_without_aggregate(self) -> None:
        for step_level in (False, True):
            with self.subTest(step_level=step_level):
                workflow = copy.deepcopy(self.workflow)
                del workflow["jobs"]["ci"]
                node = workflow["jobs"]["check"]
                if step_level:
                    node = node["steps"][0]
                node["continue-on-error"] = True
                self.assertTrue(any("continue-on-error" in e
                                    for e in check_workflow(workflow, ROOT)))

    def test_required_inventory_rejects_deleted_jobs_and_dependencies(self) -> None:
        inventory = "ci.yml check\nreusable-checks.yml check\n"
        baseline = {name: copy.deepcopy(self.workflow)
                    for name in ("ci.yml", "reusable-checks.yml")}
        self.assertEqual(check_required_jobs(baseline, inventory), [])
        for name in baseline:
            for mutation in ("job", "needs", "both", "workflow"):
                with self.subTest(workflow=name, mutation=mutation):
                    workflows = copy.deepcopy(baseline)
                    if mutation == "workflow":
                        del workflows[name]
                    else:
                        jobs = workflows[name]["jobs"]
                        if mutation in ("job", "both"):
                            del jobs["check"]
                        if mutation in ("needs", "both"):
                            jobs["ci"]["needs"] = []
                            jobs["ci"]["steps"][0]["env"]["REQUIRED_JOBS"] = ""
                    self.assertTrue(check_required_jobs(workflows, inventory))

    def test_required_inventory_rejects_invalid_records(self) -> None:
        workflows = {name: self.workflow for name in ("ci.yml", "reusable-checks.yml")}
        valid = "ci.yml check\nreusable-checks.yml check\n"
        for inventory in ("", "ci.yml check\n", valid + "ci.yml check\n",
                          valid + "unknown.yml check\n", valid + "ci.yml ci\n",
                          valid + "ci.yml\n"):
            with self.subTest(inventory=inventory):
                self.assertTrue(check_required_jobs(workflows, inventory))


class MigrationWorkflowContractsTest(unittest.TestCase):
    def test_required_generation_and_artifact_order(self):
        workflow = json.loads(subprocess.check_output(
            ['yq', '-o=json', '.', str(ROOT / '.github/workflows/reusable-checks.yml')], text=True))
        jobs = workflow['jobs']
        required = {'hygiene', 'generation', 'go', 'browser', 'auth-postgres',
                    'gitleaks', 'semgrep-sast', 'semgrep-sca'}
        self.assertEqual(set(jobs), required | {'ci'})
        self.assertEqual(set(jobs['ci']['needs']), required)
        inventory = (ROOT / 'tools/ci/required-jobs.txt').read_text().splitlines()
        self.assertEqual({line.split()[1] for line in inventory
                          if line.startswith('reusable-checks.yml ')}, required)
        self.assertFalse((ROOT / '.github/workflows/corpus-source.yml').exists())
        job = jobs['generation']
        self.assertNotIn('if', job)
        self.assertEqual(set(job['services']), {'postgres'})
        self.assertEqual(job['services']['postgres']['image'],
                         'pgvector/pgvector:0.8.2-pg17@sha256:feb68f4f15446397d8cac7f4fe48fe4586de83160d1fc48b46283312d1a33966')
        steps = job['steps']
        self.assertEqual(steps[0]['with'], {'fetch-depth': 0, 'persist-credentials': False})
        self.assertEqual([step['name'] for step in steps], [
            'Check out full repository history', 'Check trusted migration history',
            'Set up Go from go.mod', 'Set up sqlc', 'Clean every sqlc output directory',
            'Generate every sqlc entry', 'Stage regenerated regular files',
            'Upload reviewable generated trees',
            'Provision fresh corpus databases and roles as administrator',
            'Replay every migration chain on fresh databases',
            'Run retained corpus schema tests on independent databases',
            'Run fresh corpus chain and guard tests',
            'Reject changes to generated outputs during replay and tests',
            'Reject generated drift', 'Record generation scope'])
        self.assertEqual([step['run'] for step in steps if 'run' in step][:-1], [
            'python3 tools/ci/check_migration_history.py',
            'python3 tools/ci/sqlc_outputs.py clean', 'sqlc generate',
            'artifact_path="${RUNNER_TEMP}/generation-artifact"\n'
            'python3 tools/ci/sqlc_outputs.py stage --destination "${artifact_path}"\n'
            'python3 tools/ci/sqlc_outputs.py snapshot --destination "${RUNNER_TEMP}/generation-output-snapshot.json"\n'
            'printf \'path=%s\\n\' "${artifact_path}" >> "$GITHUB_OUTPUT"\n',
            'bash tools/ci/run_corpus_chain_tests.sh provision',
            'bash tools/ci/replay_generation.sh',
            'bash tools/ci/run_schema_tests.sh', 'bash tools/ci/run_corpus_chain_tests.sh',
            'python3 tools/ci/sqlc_outputs.py verify --destination "${RUNNER_TEMP}/generation-output-snapshot.json"',
            'bash tools/ci/reject_generation_drift.sh'])
        upload = next(i for i, step in enumerate(steps)
                      if step.get('uses', '').startswith('actions/upload-artifact@'))
        reject = next(i for i, step in enumerate(steps) if step.get('name') == 'Reject generated drift')
        self.assertLess(upload, reject)
        for command in ('bash tools/ci/run_schema_tests.sh',
                        'bash tools/ci/run_corpus_chain_tests.sh'):
            # The corpus helper runs both chain tests and every corpus store test.
            test = next(i for i, step in enumerate(steps) if step.get('run') == command)
            self.assertLess(upload, test)
            self.assertLess(test, reject)
            self.assertNotIn('if', steps[test])
        self.assertNotIn('if', steps[upload])
        self.assertNotIn('if', steps[reject])
        self.assertNotIn('continue-on-error', steps[reject])
        verify = next(i for i, step in enumerate(steps)
                      if step.get('name') == 'Reject changes to generated outputs during replay and tests')
        self.assertEqual(verify, reject - 1)
        self.assertEqual(steps[verify]['if'], '${{ always() }}')
        self.assertEqual(steps[upload]['with'], {
            'name': 'generation-${{ github.sha }}', 'path': '${{ steps.generated-artifact.outputs.path }}',
            'if-no-files-found': 'error', 'include-hidden-files': True, 'retention-days': 7})
        self.assertEqual(steps[upload - 1]['id'], 'generated-artifact')
        self.assertLess(next(i for i, step in enumerate(steps)
                             if step.get('name') == 'Clean every sqlc output directory'),
                        next(i for i, step in enumerate(steps)
                             if step.get('name') == 'Generate every sqlc entry'))
        corpus_tests = next(step for step in steps
                            if step.get('name') == 'Run fresh corpus chain and guard tests')
        self.assertEqual(corpus_tests['env'], {
            'BANHMI_CORPUS_TEST_POSTGRES_CONTAINER': '${{ job.services.postgres.id }}'})
        self.assertTrue((ROOT / 'tools/ci/run_corpus_chain_tests.sh').is_file())
        for chain in ('writer', 'release', 'control'):
            self.assertTrue((ROOT / f'deploy/corpusmigrations/bootstrap/{chain}.sql').is_file())
            self.assertTrue((ROOT / f'deploy/corpusmigrations/{chain}/00001_base.sql').is_file())
        self.assertEqual(steps[-1]['name'], 'Record generation scope')
        self.assertEqual(steps[-1]['if'], '${{ always() }}')

    def test_race_database_contract(self):
        replay = (ROOT / 'tools/ci/replay_generation.sh').read_text()
        script = (ROOT / 'tools/ci/run_schema_tests.sh').read_text()
        self.assertIn('readonly race_database=banhmi_corpus_race_ci', replay)
        self.assertIn('readonly rollback_database=banhmi_corpus_rollback_ci', replay)
        self.assertIn("-run '^TestReleaseActivationRace$'", script)
        self.assertIn('CORPUS_RACE_URL', script)
        self.assertIn('CORPUS_ROLLBACK_URL', script)
        self.assertIn('check_schema_tests.py', script)
        self.assertIn('createdb --template banhmi_corpus_rollback_ci', script)
        self.assertIn('DROP FUNCTION bronze.reject_immutable_change() CASCADE', script)
        self.assertIn('DROP TRIGGER observation_revision_immutable ON bronze.observation_revision', script)
        self.assertIn("-run '^TestImmutableEvidenceAndReviewConstraints$'", script)
        self.assertIn('--expected-failure', script)


if __name__ == "__main__":
    unittest.main()
