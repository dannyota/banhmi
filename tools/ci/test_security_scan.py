from __future__ import annotations

import contextlib
import io
import json
import os
import subprocess
import tempfile
import unittest
import uuid
from pathlib import Path
from unittest.mock import patch

import security_scan as scan
import semgrep_inventory
from git_test_helpers import init_git_repository


EXPECTED_GITLEAKS_CHECKSUM_FINGERPRINTS = (
    "eada71491b8224dca2981955a0fbc792c384b302:deploy/containerfiles/Containerfile.ecs.onnx:generic-api-key:25",
    "eada71491b8224dca2981955a0fbc792c384b302:deploy/containerfiles/Containerfile.ecs.onnx:generic-api-key:27",
    "622392a97f3e70219db128e6c4324379f2cd079a:deploy/containerfiles/Containerfile.ecs.embedder:generic-api-key:26",
    "622392a97f3e70219db128e6c4324379f2cd079a:deploy/containerfiles/Containerfile.ecs.embedder:generic-api-key:28",
)


def proof(product: str) -> tuple[dict, dict]:
    rule = {"id": "test.go", "languages": ["go"], "pattern": "$X"}
    if product == "sca":
        rule["r2c-internal-project-depends-on"] = {"namespace": "gomod"}
    return ({"version": "1.180.0", "results": [], "errors": [],
             "skipped_rules": [], "paths": {"scanned": ["main.go"]},
             "subprojects": [{"dependency_sources": ["go.mod"], "resolved": True,
                              "resolved_stats": {"ecosystem": "gomod",
                                                 "resolution_method": "LockfileParsing",
                                                 "dependency_count": 2}}]},
            {"info": {"enabled_products": [product]},
             "config": {"rules": {"rules": [rule]}}, "engine_params": {}})


class SecurityScanTest(unittest.TestCase):
    def test_reviewed_executable_pins(self) -> None:
        self.assertEqual(scan.GITLEAKS_VERSION, "8.30.1")
        self.assertEqual(scan.GITLEAKS_ARCHIVE, "gitleaks_8.30.1_linux_x64.tar.gz")
        self.assertEqual(scan.GITLEAKS_SHA256, "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb")
        self.assertEqual(scan.SEMGREP_IMAGE, "semgrep/semgrep:1.180.0@sha256:529ee8a277ec8adc5b534d7c74eea0a47e9de21d62852b6ba7ac6ba9566845c3")

    def test_trust_table_and_reruns(self) -> None:
        env = {"GITHUB_REPOSITORY": "owner/repo", "GITHUB_ACTOR": "writer"}
        for event_name in ("push", "workflow_dispatch", "pull_request"):
            event = {"pull_request": {"head": {"repo": {"full_name": "owner/repo"}},
                                      "user": {"login": "writer"}}}
            env["GITHUB_EVENT_NAME"] = event_name
            self.assertTrue(scan.eligible(env, event))
            env["GITHUB_ACTOR"] = "dependabot[bot]"
            self.assertFalse(scan.eligible(env, event))
            env["GITHUB_ACTOR"] = "maintainer"
        env["GITHUB_EVENT_NAME"] = "pull_request"
        for field in ("fork", "author"):
            event = {"pull_request": {"head": {"repo": {"full_name": "owner/repo"}},
                                      "user": {"login": "writer"}}}
            if field == "fork":
                event["pull_request"]["head"]["repo"]["full_name"] = "fork/repo"
            else:
                event["pull_request"]["user"]["login"] = "dependabot[bot]"
            self.assertFalse(scan.eligible(env, event))
        for name in ("pull_request_target", "workflow_call", "", "schedule"):
            env["GITHUB_EVENT_NAME"] = name
            self.assertFalse(scan.eligible(env, {}))
        env["GITHUB_EVENT_NAME"] = "pull_request"
        self.assertFalse(scan.eligible(env, {}))

    def test_valid_product_proof(self) -> None:
        for product in ("sast", "sca"):
            self.assertEqual(scan.validate_semgrep(*proof(product), product), (1, 1))

    def test_transition_proves_full_scan_before_classification(self) -> None:
        report, response = proof("sast")
        report["results"] = [{"extra": {"message": "PRIVATE-CANARY"}}]
        mutations = [lambda r, c: r.update(version="wrong"),
                     lambda r, c: r.update(skipped_rules=[{}]),
                     lambda r, c: c["info"].update(enabled_products=[]),
                     lambda r, c: c["engine_params"].update(always_suppress_errors=True),
                     lambda r, c: c["config"].update(project_merge_base="baseline"),
                     lambda r, c: c["config"]["rules"].update(rules=[]),
                     lambda r, c: r["paths"].update(scanned=[])]
        import copy
        for mutation in mutations:
            r, c = copy.deepcopy(report), copy.deepcopy(response)
            mutation(r, c)
            with patch.object(semgrep_inventory, "classify_sast") as classify:
                with self.assertRaises(scan.ScanFailure):
                    scan.validate_sast_transition(r, c, Path("/fixture"))
                classify.assert_not_called()
        summary = {"total_findings": 1}
        with patch.object(semgrep_inventory, "classify_sast", return_value=summary) as classify:
            self.assertEqual(scan.validate_sast_transition(report, response, Path("/fixture")),
                             ((1, 1), summary))
            classify.assert_called_once_with(report, response, Path("/fixture"),
                                            scan.SEMGREP_VERSION, scan.SEMGREP_IMAGE)
        with self.assertRaises(scan.ScanFailure):
            scan.validate_semgrep(report, response, "sast")

    def test_successful_transition_main_never_reports_zero_accepted_findings(self) -> None:
        report, response = proof("sast")
        report["results"] = [{"path": "main.go", "check_id": "test.go",
                              "start": {"line": 1}, "end": {"line": 1},
                              "extra": {"message": "PRIVATE-CANARY"}}]
        with tempfile.TemporaryDirectory() as temp:
            event = Path(temp) / "event.json"
            event.write_text("{}")

            def fake(args, directory, env=None, timeout=900):
                if args[0] == "git":
                    return "main.go\0" if "ls-tree" in args else "a" * 40
                (directory / "stdout").write_text(json.dumps(report))
                (directory / "scan-config.json").write_text(json.dumps(response))
                return ""

            env = {"GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "writer",
                   "GITHUB_EVENT_PATH": str(event), "GITHUB_REPOSITORY": "owner/repo",
                   "SEMGREP_APP_TOKEN": "PRIVATE-CANARY", "RUNNER_TEMP": temp}
            output = io.StringIO()
            with patch.dict(os.environ, env, clear=True), \
                    patch.object(scan, "run_private", side_effect=fake), \
                    patch.object(semgrep_inventory, "classify_sast", return_value={"total_findings": 1}), \
                    contextlib.redirect_stdout(output):
                self.assertEqual(scan.main(["semgrep-sast"]), 0)
            self.assertIn("findings 1\n", output.getvalue())
            self.assertNotIn("findings 0", output.getvalue())
            self.assertNotIn("PRIVATE-CANARY", output.getvalue())

    def test_semgrep_failure_counts_never_print_report_values(self) -> None:
        canary = uuid.uuid4().hex
        for product in ("sast", "sca"):
            for field, value, expected in (
                    ("results", [{"lines": canary}], "findings 1; errors 0; skipped rules 0; version match true"),
                    ("errors", [{"message": canary}, {"message": canary}],
                     "findings 0; errors 2; skipped rules 0; version match true"),
                    ("skipped_rules", [{"details": canary}],
                     "findings 0; errors 0; skipped rules 1; version match true"),
                    ("version", canary, "findings 0; errors 0; skipped rules 0; version match false")):
                with self.subTest(product=product, field=field):
                    report, config = proof(product)
                    report[field] = value
                    with self.assertRaises(scan.ScanFailure) as failure:
                        scan.validate_semgrep(report, config, product)
                    self.assertEqual(str(failure.exception), "Semgrep report rejected; " + expected)
                    self.assertNotIn(canary, str(failure.exception))
            for field in ("results", "errors", "skipped_rules", "version"):
                for bad in (None, {}, 1, True):
                    with self.subTest(product=product, field=field, value=bad):
                        report, config = proof(product)
                        report[field] = bad
                        with self.assertRaisesRegex(scan.ScanFailure, "Semgrep report or configuration proof malformed"):
                            scan.validate_semgrep(report, config, product)

    def test_semgrep_diagnostics_only_print_proven_metadata(self) -> None:
        canary = uuid.uuid4().hex
        report, config = proof("sast")
        location = {"path": "main.go", "start": {"line": 3}, "end": {"line": 5}}
        report["results"] = [dict(location, check_id="test.go", extra={"lines": canary,
                              "message": canary, "metadata": canary, "fix": canary}),
                             dict(location, path="/src/main.go", check_id=canary)]
        report["errors"] = [{"type": "Syntax error", "path": "main.go", "message": canary},
                            {"type": ["PartialParsing", [dict(location, content=canary)]],
                             "rule_id": "test.go", "long_msg": canary},
                            {"type": "InvalidRuleSchemaError", "rule_id": "test.go",
                             "spans": [{"file": "main.go", "start": {"line": 3},
                                        "end": {"line": 5}, "source_hash": canary}]},
                            {"type": canary, "path": canary, "message": canary}]
        report["skipped_rules"] = [{"details": canary}]
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(scan, "run_private", return_value="main.go\0") as run:
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
        self.assertEqual(output.getvalue(),
                         "Semgrep location: path main.go; lines 3-5; rule test.go\n"
                         "Semgrep location: path main.go; lines 3-5; rule withheld\n"
                         "Semgrep finding diagnostics: locations 2; withheld 0; omitted 0\n"
                         "Semgrep error: type Syntax error; path main.go; lines withheld; rule withheld\n"
                         "Semgrep error: type PartialParsing; path main.go; lines 3-5; rule test.go\n"
                         "Semgrep error: type InvalidRuleSchemaError; path main.go; lines 3-5; rule test.go\n"
                         "Semgrep error: type withheld; path withheld; lines withheld; rule withheld\n"
                         "Semgrep error diagnostics: errors 4; withheld types 1; omitted 0\n")
        self.assertNotIn(canary, output.getvalue())
        self.assertEqual(run.call_args.args[0],
                         ["git", "-C", "/checkout", "ls-tree", "-r", "--name-only", "-z", "HEAD"])

    def test_semgrep_diagnostics_withhold_unsafe_paths_lines_and_rule_ids(self) -> None:
        canary = uuid.uuid4().hex
        location = {"path": "main.go", "start": {"line": 1}, "end": {"line": 2},
                    "check_id": "test.go"}
        bad_paths = [canary, "/outside/main.go", "../main.go", "dir/../main.go", "./main.go",
                     "dir//main.go", "main.go\n", "main.go\r", "main.go\x1b[31m",
                     "dir\\main.go", "a" * 241, None, []]
        findings = [dict(location, path=value) for value in bad_paths]
        findings += [dict(location, **{field: {"line": value}})
                     for field in ("start", "end")
                     for value in (0, -1, 1_000_001, True, "1", None)]
        findings += [dict(location, start={"line": 3}, end={"line": 2}),
                     dict(location, start=None), dict(location, end=[]), None, []]
        git_paths = "\0".join(["main.go", *(path for path in bad_paths
                                           if isinstance(path, str) and path != canary)]) + "\0"
        for finding in findings:
            report, config = proof("sast")
            report["results"] = [finding]
            with tempfile.TemporaryDirectory() as temp, \
                    patch.object(scan, "run_private", return_value=git_paths):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
            self.assertNotIn("Semgrep location:", output.getvalue())
            self.assertIn("locations 0; withheld 1; omitted 0", output.getvalue())
            self.assertNotIn(canary, output.getvalue())
        for rule in (canary, "invalid/" + canary, "a" * 241, "test..go", "_test.go",
                     "test.go\n", None, []):
            report, config = proof("sast")
            report["results"] = [dict(location, check_id=rule)]
            if rule != canary:
                config["config"]["rules"]["rules"][0]["id"] = rule
            with tempfile.TemporaryDirectory() as temp, \
                    patch.object(scan, "run_private", return_value="main.go\0"):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
            self.assertIn("Semgrep location: path main.go; lines 1-2; rule withheld", output.getvalue())
            self.assertNotIn(canary, output.getvalue())

    def test_semgrep_diagnostics_bound_output_and_withhold_unknown_report_proof(self) -> None:
        canary = uuid.uuid4().hex
        report, config = proof("sca")
        report["results"] = [{"path": "go.mod", "start": {"line": 1}, "end": {"line": 1},
                              "check_id": "test.go"}] * 41
        report["errors"] = [{"type": ["DependencyResolutionError", {"message": canary}]}] * 21
        with tempfile.TemporaryDirectory() as temp, \
                patch.object(scan, "run_private", return_value="go.mod\0"):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
        self.assertEqual(output.getvalue().count("Semgrep location:"), 40)
        self.assertEqual(output.getvalue().count("Semgrep error:"), 20)
        self.assertIn("locations 40; withheld 0; omitted 1", output.getvalue())
        self.assertIn("errors 20; withheld types 0; omitted 1", output.getvalue())
        self.assertNotIn(canary, output.getvalue())
        for field, value in (("version", uuid.uuid4().hex), ("results", {}),
                             ("errors", None), ("skipped_rules", "unexpected")):
            report, config = proof("sast")
            report[field] = value
            with tempfile.TemporaryDirectory() as temp, patch.object(scan, "run_private") as run:
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
                run.assert_not_called()
            self.assertEqual(output.getvalue(), "Semgrep diagnostic report proof unavailable; details withheld\n")

    def test_semgrep_malformed_error_metadata_and_config_never_leak(self) -> None:
        canary = uuid.uuid4().hex
        span = {"file": "main.go", "start": {"line": 1}, "end": {"line": 2}}
        bad_types = [None, [], {}, [canary, span], ["PartialParsing"],
                     ["PartialParsing", [span], canary], [[canary], span]]
        bad_spans = [None, [], canary, [None], [dict(span, file=canary)],
                     [dict(span, file="../main.go")], [dict(span, end={"line": True})]]
        report, _ = proof("sast")
        report["errors"] = [{"type": value, "message": canary, "rule_id": canary}
                            for value in bad_types]
        report["errors"] += [{"type": ["PartialParsing", [dict(span, path=canary)]],
                              "spans": value, "long_msg": canary, "rule_id": "test.go"}
                             for value in bad_spans]
        for config in (None, [], {}, {"config": None}, {"config": {"rules": None}},
                       {"config": {"rules": {"rules": [None]}}}):
            with tempfile.TemporaryDirectory() as temp, \
                    patch.object(scan, "run_private", return_value="main.go\0"):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
            self.assertNotIn(canary, output.getvalue())
            self.assertNotIn("path main.go", output.getvalue())
            self.assertNotIn("lines 1-2", output.getvalue())
            self.assertNotIn("rule test.go", output.getvalue())

    def test_semgrep_diagnostic_proof_failure_withholds_locations_and_raw_errors(self) -> None:
        canary = uuid.uuid4().hex
        report, config = proof("sast")
        report["results"] = [{"path": "main.go", "start": {"line": 1}, "end": {"line": 1},
                              "check_id": "test.go", "extra": {"lines": canary}}]
        report["errors"] = [{"type": "Timeout", "path": "main.go", "message": canary}]
        for failure in (scan.ScanFailure("scanner process failed"), OSError(canary), ValueError(canary),
                        subprocess.TimeoutExpired(["git"], 10, output=canary, stderr=canary)):
            with tempfile.TemporaryDirectory() as temp, \
                    patch.object(scan, "run_private", side_effect=failure):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.semgrep_diagnostics(report, config, Path("/checkout"), Path(temp))
            self.assertIn("Semgrep location proof unavailable; locations withheld", output.getvalue())
            self.assertNotIn("Semgrep location:", output.getvalue())
            self.assertIn("Semgrep error: type Timeout; path withheld; lines withheld; rule withheld",
                          output.getvalue())
            self.assertNotIn(canary, output.getvalue())

    def test_semgrep_nonzero_exit_never_passes_even_with_clean_report(self) -> None:
        canary = uuid.uuid4().hex
        for behavior in ("finding", "clean", "malformed", "missing-config"):
            with self.subTest(behavior=behavior), tempfile.TemporaryDirectory() as temp:
                event = Path(temp) / "event.json"
                event.write_text("{}")
                private_paths = []

                def fake(args, directory, env=None, timeout=900):
                    private_paths.append(directory)
                    if args[0] == "git":
                        return "main.go\0" if "ls-tree" in args else "a" * 40
                    report, config = proof("sast")
                    if behavior in ("finding", "missing-config"):
                        report["results"] = [{"path": "main.go", "start": {"line": 1},
                                              "end": {"line": 2}, "check_id": "test.go",
                                              "extra": {"lines": canary}}]
                    if behavior != "missing-config":
                        (directory / "scan-config.json").write_text(json.dumps(config))
                    (directory / "stdout").write_text(canary if behavior == "malformed" else json.dumps(report))
                    (directory / "stderr").write_text(canary)
                    raise scan.ScanFailure("scanner process failed")

                env = {"GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "writer",
                       "GITHUB_EVENT_PATH": str(event), "GITHUB_REPOSITORY": "owner/repo",
                       "SEMGREP_APP_TOKEN": canary, "RUNNER_TEMP": temp}
                output = io.StringIO()
                with patch.dict(os.environ, env, clear=True), \
                        patch.object(scan, "run_private", side_effect=fake), \
                        patch.object(semgrep_inventory, "classify_sast") as classify, \
                        contextlib.redirect_stdout(output):
                    self.assertEqual(scan.main(["semgrep-sast"]), 1)
                classify.assert_not_called()
                self.assertNotIn(canary, output.getvalue())
                self.assertIn("Semgrep execution failed", output.getvalue())
                self.assertNotIn("findings 0\n", output.getvalue())
                if behavior in ("finding", "missing-config"):
                    self.assertIn("Semgrep location: path main.go; lines 1-2; rule "
                                  + ("withheld" if behavior == "missing-config" else "test.go"),
                                  output.getvalue())
                    self.assertIn("Semgrep report rejected; findings 1; errors 0; skipped rules 0", output.getvalue())
                for path in private_paths:
                    self.assertFalse(path.exists())

    def test_sca_requires_gomod_rules_even_with_resolved_root(self) -> None:
        for policy in ({"namespace": "npm"}, {"namespace": "pypi"},
                       {"depends-on-either": [{"namespace": "npm"}, {"namespace": "pypi"}]}):
            with self.subTest(policy=policy):
                report, config = proof("sca")
                config["config"]["rules"]["rules"][0]["r2c-internal-project-depends-on"] = policy
                with self.assertRaises(scan.ScanFailure):
                    scan.validate_semgrep(report, config, "sca")

    def test_sca_depends_on_either_can_target_gomod(self) -> None:
        report, config = proof("sca")
        config["config"]["rules"]["rules"][0]["r2c-internal-project-depends-on"] = {
            "depends-on-either": [{"namespace": "npm"}, {"namespace": "GOMOD"}],
        }
        self.assertEqual(scan.validate_semgrep(report, config, "sca"), (1, 1))

    def test_sca_rejects_malformed_ecosystem_policy(self) -> None:
        policies = [None, "gomod", [], {}, {"namespace": ["gomod"]},
                    {"depends-on-either": "gomod"}, {"depends-on-either": {}},
                    {"depends-on-either": []}, {"depends-on-either": ["gomod"]},
                    {"depends-on-either": [{"namespace": "gomod"}, {}]},
                    {"depends-on-either": [{"namespace": "gomod"}, {"namespace": None}]}]
        for policy in policies:
            with self.subTest(policy=policy):
                report, config = proof("sca")
                config["config"]["rules"]["rules"][0]["r2c-internal-project-depends-on"] = policy
                with self.assertRaises(scan.ScanFailure):
                    scan.validate_semgrep(report, config, "sca")

    def test_missing_malformed_and_negative_proof(self) -> None:
        for product in ("sast", "sca"):
            mutations = [lambda r, c: r.pop("version"),
                         lambda r, c: r.update(version="1.179.0"),
                         lambda r, c: r.update(results=[{"extra": {"is_ignored": True}}]),
                         lambda r, c: r.update(errors=[{}]),
                         lambda r, c: r.update(skipped_rules=[{}]),
                         lambda r, c: r.pop("errors"),
                         lambda r, c: c["info"].update(enabled_products=[]),
                         lambda r, c: c["info"].update(enabled_products=product),
                         lambda r, c: c["config"].update(rules={"rules": []}),
                         lambda r, c: c["engine_params"].update(always_suppress_errors=True),
                         lambda r, c: c["config"].update(project_merge_base="abc")]
            if product == "sast":
                mutations += [lambda r, c: r["paths"].update(scanned=["README.md"]),
                              lambda r, c: c["config"]["rules"]["rules"][0].update(languages="go"),
                              lambda r, c: c["config"]["rules"]["rules"][0].update(languages=["python"])]
            else:
                mutations += [lambda r, c: r.update(subprojects=[]),
                              lambda r, c: r["subprojects"][0].update(resolved=False),
                              lambda r, c: r["subprojects"][0].update(dependency_sources=["sub/go.mod"]),
                              lambda r, c: r["subprojects"][0]["resolved_stats"].update(dependency_count=0),
                              lambda r, c: r["subprojects"][0]["resolved_stats"].update(ecosystem="npm"),
                              lambda r, c: r["subprojects"][0]["resolved_stats"].update(resolution_method="DynamicResolution"),
                              lambda r, c: r["subprojects"].append({"resolved": False}),
                              lambda r, c: c["config"]["rules"]["rules"][0].pop("r2c-internal-project-depends-on")]
            for mutate in mutations:
                with self.subTest(product=product, mutation=mutate):
                    report, config = proof(product)
                    mutate(report, config)
                    with self.assertRaises(scan.ScanFailure):
                        scan.validate_semgrep(report, config, product)
            for bad in (None, [], {}, "unexpected"):
                with self.assertRaises(scan.ScanFailure):
                    scan.validate_semgrep(bad, {}, product)

    def test_container_boundary_and_product_flags(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            env = {"SEMGREP_APP_TOKEN": uuid.uuid4().hex, "GITHUB_TOKEN": uuid.uuid4().hex,
                   "GITHUB_REPOSITORY": "owner/repo", "GITHUB_REF_NAME": "branch",
                   "GITHUB_RUN_ID": "123", "GITHUB_SERVER_URL": "https://github.com"}
            for product, flag in (("sast", "--code"), ("sca", "--supply-chain")):
                args, child_env = scan.semgrep_command(product, Path(temp), Path(temp), "a" * 40, env)
                self.assertIn(scan.SEMGREP_IMAGE, args)
                self.assertIn("linux/amd64", args)
                self.assertEqual(args[args.index("--user") + 1], f"{os.getuid()}:{os.getgid()}")
                self.assertEqual(args[args.index(scan.SEMGREP_IMAGE) + 1:],
                                 ["semgrep", "ci", flag, "--oss-only", "--no-suppress-errors",
                                  "--disable-nosem", "--json", "--timeout=60",
                                  "--x-dump-scan-config-path", "/private/scan-config.json"])
                self.assertTrue(any("/src,readonly" in arg for arg in args))
                self.assertNotIn(env["SEMGREP_APP_TOKEN"], args)
                self.assertEqual(child_env["SEMGREP_APP_TOKEN"], env["SEMGREP_APP_TOKEN"])
                for forbidden in ("GITHUB_TOKEN", "GITHUB_ACTIONS", "SEMGREP_PR_ID", "SEMGREP_BASELINE_COMMIT"):
                    self.assertNotIn(forbidden, child_env)
                for forbidden in ("--strict", "--allow-local-builds", "--pro", "--secrets", "--historical-secrets",
                                  "/var/run/docker.sock"):
                    self.assertNotIn(forbidden, " ".join(args))

    def test_missing_token_or_untrusted_event_never_starts_scanner(self) -> None:
        for event, token in (("push", ""), ("schedule", uuid.uuid4().hex)):
            with tempfile.TemporaryDirectory() as temp:
                path = Path(temp) / "event.json"
                path.write_text("{}")
                env = {"GITHUB_EVENT_NAME": event, "GITHUB_ACTOR": "writer",
                       "GITHUB_EVENT_PATH": str(path), "GITHUB_REPOSITORY": "owner/repo",
                       "SEMGREP_APP_TOKEN": token}
                output = io.StringIO()
                with patch.dict(os.environ, env, clear=True), patch.object(scan, "run_private") as run:
                    with contextlib.redirect_stdout(output):
                        self.assertEqual(scan.main(["semgrep-sast"]), 1)
                    run.assert_not_called()
                if token:
                    self.assertNotIn(token, output.getvalue())

    def test_private_process_failure_timeout_and_malformed_output_redact(self) -> None:
        canary = uuid.uuid4().hex
        for behavior in ("failure", "timeout", "engine-timeout", "malformed", "finding", "success"):
            with self.subTest(behavior=behavior), tempfile.TemporaryDirectory() as temp:
                event = Path(temp) / "event.json"
                event.write_text("{}")
                private_paths = []
                # Match the empty scanner fixture while retaining real classification.
                inventory = json.dumps({"schema_version": 1, "semgrep_version": scan.SEMGREP_VERSION,
                                        "semgrep_image": scan.SEMGREP_IMAGE,
                                        "findings": [], "partial_parsing": []}).encode("utf-8")

                def read_inventory(path):
                    if path != semgrep_inventory.INVENTORY_PATH:
                        raise semgrep_inventory.InventoryFailure("Semgrep transition source proof unavailable")
                    return inventory

                def fake(args, directory, env=None, timeout=900):
                    private_paths.append(directory)
                    if args[0] == "git":
                        return "a" * 40
                    if behavior == "timeout":
                        raise subprocess.TimeoutExpired(args, timeout, output=canary, stderr=canary)
                    if behavior == "failure":
                        raise scan.ScanFailure("scanner process failed")
                    report, config = proof("sast")
                    if behavior == "finding":
                        report["results"] = [{"lines": canary}]
                    if behavior == "engine-timeout":
                        report["errors"] = [{"type": "Timeout", "code": 2, "level": "warn",
                                             "path": "main.go", "rule_id": "test.go",
                                             "message": canary}]
                    (directory / "scan-config.json").write_text(json.dumps(config))
                    (directory / "stdout").write_text(canary if behavior == "malformed" else json.dumps(report))
                    (directory / "stderr").write_text(canary)
                    return ""

                env = {"GITHUB_EVENT_NAME": "push", "GITHUB_ACTOR": "writer",
                       "GITHUB_EVENT_PATH": str(event), "GITHUB_REPOSITORY": "owner/repo",
                       "SEMGREP_APP_TOKEN": canary, "RUNNER_TEMP": temp}
                output = io.StringIO()
                with patch.dict(os.environ, env, clear=True), \
                        patch.object(scan, "run_private", side_effect=fake), \
                        patch.object(semgrep_inventory.GitSource, "read", side_effect=read_inventory):
                    with contextlib.redirect_stdout(output):
                        self.assertEqual(scan.main(["semgrep-sast"]), 0 if behavior == "success" else 1)
                self.assertNotIn(canary, output.getvalue())
                if behavior == "engine-timeout":
                    self.assertIn("unmatched records 1; stale entries 0", output.getvalue())
                    self.assertIn("Semgrep error: type Timeout;", output.getvalue())
                    self.assertIn("Security scan failed: Semgrep transition has unmatched records or stale entries",
                                  output.getvalue())
                for path in private_paths:
                    self.assertFalse(path.exists())

    def test_corrupt_gitleaks_archive_rejected_before_extraction(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            with patch.object(scan.urllib.request, "urlopen") as download:
                download.return_value.__enter__.return_value = io.BytesIO(uuid.uuid4().bytes)
                with self.assertRaises(scan.ScanFailure):
                    scan.install_gitleaks(Path(temp))
            self.assertFalse((Path(temp) / "gitleaks").exists())

    def test_wrong_gitleaks_version_never_scans(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp)
            with patch.object(scan, "install_gitleaks", return_value=directory / "gitleaks"), \
                    patch.object(scan, "run_private", return_value="8.29.0") as run:
                with self.assertRaises(scan.ScanFailure):
                    scan.gitleaks(directory, directory)
                self.assertEqual(run.call_count, 1)

    def test_download_failure_redacts_underlying_error(self) -> None:
        canary = uuid.uuid4().hex
        with patch.object(scan.urllib.request, "urlopen", side_effect=OSError(canary)), \
                patch.object(scan, "run_private", return_value="a" * 40):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                self.assertEqual(scan.main(["gitleaks"]), 1)
            self.assertNotIn(canary, output.getvalue())

    def test_process_captures_both_streams_and_rejects_nonzero(self) -> None:
        canary = uuid.uuid4().hex

        def fake(args, **kwargs):
            kwargs["stdout"].write(canary.encode())
            kwargs["stderr"].write(canary.encode())
            return subprocess.CompletedProcess(args, 1)

        with tempfile.TemporaryDirectory() as temp, patch.object(scan.subprocess, "run", side_effect=fake):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                with self.assertRaises(scan.ScanFailure) as failure:
                    scan.run_private(["fake-scanner"], Path(temp))
            self.assertNotIn(canary, str(failure.exception))
            self.assertNotIn(canary, output.getvalue())
            self.assertTrue((Path(temp) / "stdout").stat().st_size > 0)
            self.assertTrue((Path(temp) / "stderr").stat().st_size > 0)

    def test_hosted_real_gitleaks_finds_removed_history_secret(self) -> None:
        # GitHub hygiene runs this disposable input. No fixture or report is tracked.
        with tempfile.TemporaryDirectory() as temp:
            source = Path(temp) / "source"
            source.mkdir()
            directory = Path(temp) / "private"
            directory.mkdir(mode=0o700)
            env = {"PATH": os.defpath, "HOME": temp,
                   "GIT_AUTHOR_NAME": "CI", "GIT_AUTHOR_EMAIL": "ci@example.invalid",
                   "GIT_COMMITTER_NAME": "CI", "GIT_COMMITTER_EMAIL": "ci@example.invalid",
                   "GIT_CONFIG_NOSYSTEM": "1"}

            def git(*args):
                result = subprocess.run(["git", "-C", str(source), *args],
                                        env=env, capture_output=True, timeout=30)
                self.assertEqual(result.returncode, 0, "disposable Git input setup failed")

            init_git_repository(source, env=env)
            payload = source / "sample.txt"
            payload.write_text('api_key = "' + uuid.uuid4().hex + uuid.uuid4().hex + '"\n')
            git("add", "sample.txt")
            git("-c", "commit.gpgsign=false", "commit", "-m", "add disposable input")
            payload.write_text("removed\n")
            git("add", "sample.txt")
            git("-c", "commit.gpgsign=false", "commit", "-m", "remove disposable input")
            with self.assertRaises(scan.ScanFailure):
                scan.gitleaks(source, directory)
            self.assertTrue(bool(json.loads((directory / "gitleaks.json").read_text())),
                            "removed historical secret must produce findings")

    def test_gitleaks_history_and_private_policy(self) -> None:
        with tempfile.TemporaryDirectory() as temp:
            directory = Path(temp) / "private"
            directory.mkdir(mode=0o700)
            source = Path(temp) / "source"
            source.mkdir()
            repository_config = source / ".gitleaks.toml"
            repository_config.write_text("[[allowlists]]\nregexes = ['.*']\n")
            repository_ignore = source / ".gitleaksignore"
            repository_ignore.write_text("sample.txt:generic-api-key:1\n")
            commands = []
            environments = []

            def fake(args, directory, env=None, timeout=900):
                commands.append(args)
                environments.append(env)
                if "version" in args:
                    return "8.30.1"
                Path(args[args.index("--report-path") + 1]).write_text("[]")
                return ""

            with patch.dict(os.environ, {"GITLEAKS_CONFIG": str(repository_config),
                                         "GITLEAKS_CONFIG_TOML": repository_config.read_text(),
                                         "GITLEAKS_IGNORE_PATH": str(repository_ignore)}, clear=True), \
                    patch.object(scan, "install_gitleaks", return_value=directory / "gitleaks"), \
                    patch.object(scan, "run_private", side_effect=fake):
                scan.gitleaks(source, directory)
            args = commands[-1]
            for flag in ("git", "--log-opts=--all", "--redact=100", "--ignore-gitleaks-allow", "--exit-code=1"):
                self.assertIn(flag, args)
            self.assertEqual(args[args.index("--config") + 1], str(directory / "gitleaks.toml"))
            self.assertEqual(args[args.index("--gitleaks-ignore-path") + 1], str(directory / "gitleaksignore"))
            for env in environments:
                self.assertEqual(env, {"PATH": os.defpath, "HOME": str(directory)})
            self.assertEqual((directory / "gitleaks.toml").read_text(), "[extend]\nuseDefault = true\n")
            ignore_text = (directory / "gitleaksignore").read_text()
            self.assertEqual(ignore_text, "\n".join(EXPECTED_GITLEAKS_CHECKSUM_FINGERPRINTS) + "\n")
            inventory = ignore_text.splitlines()
            self.assertEqual(len(inventory), 4)
            self.assertEqual(len(set(inventory)), 4)
            for fingerprint in inventory:
                self.assertRegex(fingerprint, r"^[0-9a-f]{40}:[^:]+:generic-api-key:[0-9]+$")

    def test_gitleaks_diagnostics_require_git_proof_and_fixed_rules(self) -> None:
        canary = uuid.uuid4().hex
        commit = "a" * 40
        location = {"File": "removed/sample.txt", "StartLine": 3, "EndLine": 5,
                    "Commit": commit, "RuleID": "generic-api-key", "Secret": canary,
                    "Match": canary, "Description": canary, "Fingerprint": canary}
        findings = [location, dict(location, File="/checkout/removed/sample.txt", RuleID=canary)]
        commands = []

        def fake(args, directory, env=None, timeout=900):
            commands.append(args)
            return commit + "\n" if "rev-list" in args else "removed/sample.txt\0"

        with tempfile.TemporaryDirectory() as temp, patch.object(scan, "run_private", side_effect=fake):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                scan.gitleaks_diagnostics(findings, Path("/checkout"), Path(temp))
        self.assertEqual(output.getvalue(),
                         f"Gitleaks location: path removed/sample.txt; lines 3-5; commit {commit}; rule generic-api-key\n"
                         f"Gitleaks location: path removed/sample.txt; lines 3-5; commit {commit}; rule withheld\n"
                         "Gitleaks diagnostics: locations 2; withheld 0; omitted 0\n")
        self.assertNotIn(canary, output.getvalue())
        self.assertIn("--all", commands[0])
        self.assertEqual(sum("ls-tree" in args for args in commands), 1)

    def test_gitleaks_diagnostics_withhold_unsafe_or_unproved_locations(self) -> None:
        canary = uuid.uuid4().hex
        commit = "a" * 40
        location = {"File": "sample.txt", "StartLine": 1, "EndLine": 2,
                    "Commit": commit, "RuleID": "generic-api-key"}
        bad_paths = [canary, "/outside/sample.txt", "../sample.txt", "dir/../sample.txt",
                     "./sample.txt", "dir//sample.txt", "sample.txt\n", "sample.txt\r",
                     "sample.txt\x1b[31m", "dir\\sample.txt", "a" * 241, None, []]
        findings = [dict(location, File=value) for value in bad_paths]
        findings += [dict(location, Commit=value) for value in (canary, "b" * 40, None, [])]
        findings += [dict(location, **{field: value})
                     for field in ("StartLine", "EndLine")
                     for value in (0, -1, 1_000_001, True, "1", None)]
        findings.append(dict(location, StartLine=3, EndLine=2))

        def fake(args, directory, env=None, timeout=900):
            # Even a Git path with unsafe characters cannot enter the public summary.
            paths = [value for value in bad_paths if isinstance(value, str) and value != canary]
            return commit + "\n" if "rev-list" in args else "\0".join(["sample.txt", *paths]) + "\0"

        with tempfile.TemporaryDirectory() as temp, patch.object(scan, "run_private", side_effect=fake):
            for finding in findings:
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.gitleaks_diagnostics([finding], Path("/checkout"), Path(temp))
                self.assertEqual(output.getvalue(), "Gitleaks diagnostics: locations 0; withheld 1; omitted 0\n")
                self.assertNotIn(canary, output.getvalue())

    def test_gitleaks_diagnostics_limit_output_and_preserve_failure(self) -> None:
        commit = "a" * 40
        location = {"File": "sample.txt", "StartLine": 1, "EndLine": 1,
                    "Commit": commit, "RuleID": "private-key"}

        def fake(args, directory, env=None, timeout=900):
            if "rev-list" in args:
                return commit + "\n"
            if "ls-tree" in args:
                return "sample.txt\0"
            if "version" in args:
                return scan.GITLEAKS_VERSION
            (directory / "gitleaks.json").write_text(json.dumps([location] * 21))
            raise scan.ScanFailure("scanner process failed")

        with tempfile.TemporaryDirectory() as temp, \
                patch.object(scan, "install_gitleaks", side_effect=lambda path: path / "gitleaks"), \
                patch.object(scan, "run_private", side_effect=fake):
            output = io.StringIO()
            with contextlib.redirect_stdout(output):
                with self.assertRaisesRegex(scan.ScanFailure, "Gitleaks findings; count 21"):
                    scan.gitleaks(Path("/checkout"), Path(temp))
            self.assertEqual(output.getvalue().count("Gitleaks location:"), 20)
            self.assertIn("Gitleaks diagnostics: locations 20; withheld 0; omitted 1", output.getvalue())

    def test_gitleaks_diagnostic_proof_failure_never_prints_process_output(self) -> None:
        canary = uuid.uuid4().hex
        for failure in (scan.ScanFailure("scanner process failed"), OSError(canary), ValueError(canary),
                        subprocess.TimeoutExpired(["git"], 10, output=canary, stderr=canary)):
            with tempfile.TemporaryDirectory() as temp, \
                    patch.object(scan, "run_private", side_effect=failure):
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    scan.gitleaks_diagnostics([{"Secret": canary}], Path("/checkout"), Path(temp))
                self.assertEqual(output.getvalue(), "Gitleaks location proof unavailable; finding details withheld\n")
                self.assertNotIn(canary, output.getvalue())

    def test_gitleaks_findings_and_operational_failures_are_private(self) -> None:
        canary = uuid.uuid4().hex
        for behavior in ("findings", "four", "empty", "missing", "malformed", "wrong-shape"):
            with self.subTest(behavior=behavior), tempfile.TemporaryDirectory() as temp:
                private_paths = []

                def fake(args, directory, env=None, timeout=900):
                    private_paths.append(directory)
                    if args[0] == "git":
                        return "a" * 40
                    if "version" in args:
                        return scan.GITLEAKS_VERSION
                    (directory / "stdout").write_text(canary)
                    (directory / "stderr").write_text(canary)
                    report = directory / "gitleaks.json"
                    if behavior == "findings":
                        report.write_text(json.dumps([{"Secret": canary, "File": canary},
                                                      {"Secret": canary, "File": canary}]))
                    elif behavior == "four":
                        report.write_text(json.dumps([{"Secret": canary, "File": canary, "Fingerprint": fingerprint}
                                                      for fingerprint in EXPECTED_GITLEAKS_CHECKSUM_FINGERPRINTS]))
                    elif behavior == "empty":
                        report.write_text("[]")
                    elif behavior == "malformed":
                        report.write_text(canary)
                    elif behavior == "wrong-shape":
                        report.write_text(json.dumps({"error": canary}))
                    raise scan.ScanFailure("scanner process failed")

                output = io.StringIO()
                with patch.dict(os.environ, {"RUNNER_TEMP": temp}, clear=True), \
                        patch.object(scan, "install_gitleaks", side_effect=lambda path: path / "gitleaks"), \
                        patch.object(scan, "run_private", side_effect=fake), \
                        contextlib.redirect_stdout(output):
                    self.assertEqual(scan.main(["gitleaks"]), 1)
                self.assertNotIn(canary, output.getvalue())
                if behavior in ("findings", "four"):
                    count = 2 if behavior == "findings" else 4
                    self.assertIn(f"Gitleaks findings; count {count}", output.getvalue())
                    self.assertNotIn("authentication", output.getvalue())
                else:
                    self.assertIn("Gitleaks execution failed", output.getvalue())
                for path in private_paths:
                    self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
