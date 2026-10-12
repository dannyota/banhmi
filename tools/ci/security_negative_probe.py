"""Temporary hosted real-scanner controls. Never merge this probe branch."""

from __future__ import annotations

import contextlib
import copy
import json
import os
import re
import secrets
import signal
import subprocess
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True
import security_scan


SAST_RULES = frozenset({
    "problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification",
})
SCA_RULES = frozenset({
    "ssc-5a433d7d-7c1b-39cf-6232-29a7bb435234",
    "ssc-83304292-ed75-420a-8e83-74868263fc5f",
    "ssc-parity-268bc2bf6cbd163d6eb351581e73e56d91c07efe",
})
PROBE_STAGES = frozenset({
    "initial-trust", "argument-selection", "runner-temp", "gitleaks-controls",
    "semgrep-trust", "fixture-inputs", "checkout-revision", "scanner-command",
    "scanner-execution", "scanner-exit", "report-output-availability", "report-output-size",
    "report-config-availability", "report-config-size", "report-decode",
    "report-shape", "report-findings", "configured-rules", "expected-findings",
    "finding-paths", "actual-rejection", "proof-copy", "container-cleanup",
})
DIAGNOSTICS = {
    "stage": "initial-trust", "scanner_exit": "unavailable", "findings": "unavailable",
    "expected": "unavailable", "unexpected": "unavailable", "configured_expected": "unavailable",
    "errors": "unavailable", "skipped_rules": "unavailable", "version_match": "unavailable",
    "actual_rejected": "unavailable", "validator": "unavailable", "cleanup": "unavailable",
    "report_bytes": "unavailable", "config_bytes": "unavailable",
}


class ProbeFailure(Exception):
    """Probe failures expose no process or scanner text."""


def require(condition: bool) -> None:
    if not condition:
        raise ProbeFailure()


def stage(value: str) -> None:
    require(value in PROBE_STAGES)
    DIAGNOSTICS["stage"] = value


def count(name: str, value: object) -> None:
    require(name in ("findings", "expected", "unexpected", "configured_expected", "errors", "skipped_rules"))
    if type(value) is int and value >= 0:
        DIAGNOSTICS[name] = min(value, 1_000_000)
    elif isinstance(value, list):
        DIAGNOSTICS[name] = min(len(value), 1_000_000)


def validator_category(error: security_scan.ScanFailure) -> str:
    # Compare privately. Never print an exception or any report-controlled text.
    message = str(error)
    if message.startswith("Semgrep report rejected;"):
        return "report-rejected"
    return {
        "Semgrep report or configuration proof malformed": "malformed-proof",
        "Semgrep product or full-scan proof missing": "product-or-full-scan",
        "Semgrep applicable product rules missing": "applicable-rules",
        "Semgrep dependency rule ecosystem proof malformed": "dependency-rule",
        "Semgrep scanned source proof missing": "source-proof",
        "Semgrep scanned Go source missing": "go-source",
        "Semgrep root Go dependency resolution missing": "root-resolution",
        "Semgrep unresolved dependency subproject": "unresolved-subproject",
        "Semgrep dependency source proof missing": "dependency-source",
    }.get(message, "other-fixed-category")


def private_process(args: list[str], directory: Path, env: dict) -> tuple[int, str]:
    with (directory / "process-stdout").open("wb") as stdout, (directory / "process-stderr").open("wb") as stderr:
        result = subprocess.run(args, env=env, stdout=stdout, stderr=stderr,
                                timeout=90, check=False)
    return result.returncode, (directory / "process-stdout").read_text()


def git(repo: Path, directory: Path, env: dict, *args: str) -> str:
    code, output = private_process(
        ["git", "-c", "core.hooksPath=/dev/null", "-C", str(repo), *args], directory, env)
    require(code == 0)
    return output.strip()


def fixture(directory: Path, name: str) -> tuple[Path, dict]:
    repo = directory / name
    repo.mkdir(mode=0o700)
    home = directory / (name + "-home")
    home.mkdir(mode=0o700)
    env = {
        "PATH": os.defpath,
        "HOME": str(home),
        "GIT_CONFIG_NOSYSTEM": "1",
        "GIT_CONFIG_GLOBAL": os.devnull,
        "GIT_TERMINAL_PROMPT": "0",
        "GIT_AUTHOR_NAME": "Synthetic probe",
        "GIT_AUTHOR_EMAIL": "probe@example.invalid",
        "GIT_COMMITTER_NAME": "Synthetic probe",
        "GIT_COMMITTER_EMAIL": "probe@example.invalid",
    }
    git(repo, directory, env, "init", "--initial-branch=probe")
    return repo, env


def add_key(repo: Path, path: str, line: int) -> None:
    target = repo / path
    target.parent.mkdir(parents=True, exist_ok=True)
    # Only disposable runner files receive generated values. Never execute them.
    target.write_text("# synthetic probe\n" * (line - 1)
                      + 'api_key = "' + secrets.token_hex(32) + '"\n')


def read_findings(report: Path) -> list[dict]:
    require(report.is_file() and report.stat().st_size <= 2_000_000)
    findings = json.loads(report.read_text())
    require(isinstance(findings, list) and all(isinstance(item, dict) for item in findings))
    return findings


def locations(findings: list[dict]) -> set[tuple]:
    return {(item.get("Commit"), item.get("File"), item.get("RuleID"), item.get("StartLine"))
            for item in findings}


def scanner(binary: Path, repo: Path, directory: Path, env: dict,
            ignore_entries: list[str]) -> list[dict]:
    directory.mkdir(mode=0o700)
    config = directory / "gitleaks.toml"
    config.write_text("[extend]\nuseDefault = true\n")
    ignore = directory / "gitleaksignore"
    ignore.write_text("\n".join(ignore_entries) + ("\n" if ignore_entries else ""))
    report = directory / "gitleaks.json"
    code, _ = private_process(
        [str(binary), "git", str(repo), "--log-opts=--all", "--redact=100",
         "--ignore-gitleaks-allow", "--exit-code=1", "--config", str(config),
         "--gitleaks-ignore-path", str(ignore), "--report-format=json", "--report-path", str(report)],
        directory, env)
    findings = read_findings(report)
    require(code == (1 if findings else 0))
    return findings


def production_history(binary: Path, directory: Path) -> int:
    inventory = security_scan.GITLEAKS_CHECKSUM_FINGERPRINTS
    require(isinstance(inventory, tuple) and len(inventory) == len(set(inventory)) == 4)
    require(all(isinstance(entry, str)
                and re.fullmatch(r"[0-9a-f]{40}:deploy/containerfiles/Containerfile\.ecs\.(onnx|embedder):generic-api-key:[0-9]+", entry)
                for entry in inventory))
    _, path, rule, line_text = inventory[0].split(":")
    line = int(line_text)
    require(1 <= line <= 100)
    repo, env = fixture(directory, "removed-history")
    add_key(repo, path, line)
    git(repo, directory, env, "add", "--", path)
    git(repo, directory, env, "commit", "-m", "Add synthetic probe input")
    introduced = git(repo, directory, env, "rev-parse", "HEAD")
    git(repo, directory, env, "rm", "--", path)
    git(repo, directory, env, "commit", "-m", "Remove synthetic probe input")
    require(git(repo, directory, env, "status", "--porcelain") == "")
    require(git(repo, directory, env, "ls-files") == "")
    work = directory / "production-wrapper"
    work.mkdir(mode=0o700)
    installer = security_scan.install_gitleaks
    try:
        # Reuse the already verified binary. All other production wrapper logic runs.
        security_scan.install_gitleaks = lambda unused: binary
        failed = False
        with (work / "wrapper-output").open("w") as output, contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
            try:
                security_scan.gitleaks(repo, work)
            except security_scan.ScanFailure as error:
                failed = True
                require(re.fullmatch(r"Gitleaks findings; count [1-9][0-9]*; review and remediate secrets", str(error)) is not None)
        require(failed)
    finally:
        security_scan.install_gitleaks = installer
    require((work / "gitleaksignore").read_text().splitlines() == list(inventory))
    findings = read_findings(work / "gitleaks.json")
    require(bool(findings) and (introduced, path, rule, line) in locations(findings))
    require(git(repo, directory, env, "status", "--porcelain") == "")
    return len(findings)


def fingerprint_controls(binary: Path, directory: Path) -> tuple[int, int, list[int]]:
    repo, env = fixture(directory, "fingerprint-contract")
    target, other = "chosen.txt", "control.txt"
    add_key(repo, target, 1)
    add_key(repo, other, 1)
    git(repo, directory, env, "add", "--", target, other)
    git(repo, directory, env, "commit", "-m", "Add isolated scanner contract inputs")
    commit = git(repo, directory, env, "rev-parse", "HEAD")
    chosen = (commit, target, "generic-api-key", 1)
    control = (commit, other, "generic-api-key", 1)
    baseline = scanner(binary, repo, directory / "baseline", env, [])
    require(len(baseline) == 2 and locations(baseline) == {chosen, control})
    exact = f"{commit}:{target}:generic-api-key:1"
    suppressed = scanner(binary, repo, directory / "exact", env, [exact])
    require(len(suppressed) == 1 and locations(suppressed) == {control})
    mutations = [
        f"{'0' * 40}:{target}:generic-api-key:1",
        f"{commit}:different.txt:generic-api-key:1",
        f"{commit}:{target}:private-key:1",
        f"{commit}:{target}:generic-api-key:2",
    ]
    counts = []
    for index, fingerprint in enumerate(mutations):
        restored = scanner(binary, repo, directory / f"near-miss-{index}", env, [fingerprint])
        require(len(restored) == 2 and locations(restored) == {chosen, control})
        counts.append(len(restored))
    return len(baseline), len(suppressed), counts


def semgrep_fixture(directory: Path, product: str, checkout: Path) -> tuple[Path, dict, str]:
    repo, env = fixture(directory, "semgrep-" + product)
    if product == "sast":
        (repo / "probe.go").write_text(
            'package probe\n\nimport "crypto/tls"\n\n'
            'func UnsafeConfig() *tls.Config {\n'
            '\treturn &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true}\n}\n')
        paths = ["probe.go"]
    else:
        (repo / "go.mod").write_text(
            "module example.invalid/security-sca-negative\n\ngo 1.26.3\n\n"
            "require github.com/go-jose/go-jose/v3 v3.0.3\n")
        sums = [line for line in (checkout / "go.sum").read_text().splitlines()
                if line.startswith("github.com/go-jose/go-jose/v3 v3.0.3 ")
                or line.startswith("github.com/go-jose/go-jose/v3 v3.0.3/go.mod ")]
        require(len(sums) == 2)
        (repo / "go.sum").write_text("\n".join(sums) + "\n")
        (repo / "probe.go").write_text(
            'package probe\n\nimport jose "github.com/go-jose/go-jose/v3"\n\n'
            'func Parse(input string) (*jose.JSONWebSignature, error) {\n'
            '\treturn jose.ParseSigned(input)\n}\n')
        paths = ["go.mod", "go.sum", "probe.go"]
    # Commit only scanner inputs. No compiler, package manager, or fixture code runs.
    git(repo, directory, env, "add", "--", *paths)
    git(repo, directory, env, "commit", "-m", "Add isolated Semgrep negative input")
    revision = git(repo, directory, env, "rev-parse", "HEAD")
    require(re.fullmatch(r"[0-9a-f]{40}", revision) is not None)
    return repo, env, revision


def stop_semgrep(directory: Path) -> None:
    cidfile = directory / "container-id"
    if not cidfile.exists():
        return
    cid = cidfile.read_text().strip()
    require(re.fullmatch(r"[0-9a-f]{64}", cid) is not None)
    # --rm removes completed containers. On failure, remove any survivor too.
    cleanup = directory / "cleanup"
    cleanup.mkdir(mode=0o700)
    try:
        security_scan.run_private(["docker", "rm", "--force", cid], cleanup,
                                  {"PATH": os.defpath}, timeout=30)
    except security_scan.ScanFailure:
        pass
    remaining = security_scan.run_private(
        ["docker", "ps", "--all", "--no-trunc", "--filter", f"id={cid}",
         "--format", "{{.ID}}"], cleanup, {"PATH": os.defpath}, timeout=30)
    require(remaining.strip() == "")


def semgrep_control(directory: Path, product: str) -> tuple[str, str, int, int, int]:
    stage("semgrep-trust")
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    require(security_scan.eligible(os.environ, event))
    require(bool(os.environ.get("SEMGREP_APP_TOKEN")))
    checkout = Path.cwd().resolve()
    stage("fixture-inputs")
    repo, git_env, revision = semgrep_fixture(directory, product, checkout)
    stage("checkout-revision")
    checkout_revision = git(checkout, directory, git_env, "rev-parse", "HEAD")
    require(re.fullmatch(r"[0-9a-f]{40}", checkout_revision) is not None)
    work = directory / "semgrep-private"
    work.mkdir(mode=0o700)
    (work / "home").mkdir(mode=0o700)
    stage("scanner-command")
    args, child_env = security_scan.semgrep_command(product, repo, work, revision, os.environ)
    try:
        # Retain the exit code because run_private discards nonzero outcomes.
        # The pinned CLI may return 1 for findings. Other exits cannot pass even
        # when the scanner wrote a report before an operational failure.
        stage("scanner-execution")
        with (work / "stdout").open("wb") as stdout, (work / "stderr").open("wb") as stderr:
            process = subprocess.run(args, env=child_env, stdout=stdout, stderr=stderr,
                                     timeout=600, check=False)
        DIAGNOSTICS["scanner_exit"] = str(process.returncode) if process.returncode in (0, 1) else "unexpected"
        stage("scanner-exit")
        require(process.returncode in (0, 1))
        report_path, config_path = work / "stdout", work / "scan-config.json"
        stage("report-output-availability")
        require(report_path.is_file())
        report_size = report_path.stat().st_size
        DIAGNOSTICS["report_bytes"] = report_size if report_size <= 1_000_000_000 else "over-1000000000"
        stage("report-output-size")
        require(report_size <= 32_000_000)
        stage("report-config-availability")
        require(config_path.is_file())
        config_size = config_path.stat().st_size
        DIAGNOSTICS["config_bytes"] = config_size if config_size <= 1_000_000_000 else "over-1000000000"
        stage("report-config-size")
        require(config_size <= 64_000_000)
        # Capture both reports before cleanup can overwrite any process output.
        stage("report-decode")
        report = json.loads(report_path.read_text())
        response = json.loads(config_path.read_text())
        stage("report-shape")
        require(isinstance(report, dict) and isinstance(response, dict))
        DIAGNOSTICS["version_match"] = "true" if report.get("version") == security_scan.SEMGREP_VERSION else "false"
        for name, field in (("errors", "errors"), ("skipped_rules", "skipped_rules"), ("findings", "results")):
            value = report.get(field)
            if isinstance(value, list):
                count(name, len(value))
        results = report.get("results")
        stage("report-findings")
        require(isinstance(results, list) and bool(results)
                and all(isinstance(result, dict) for result in results))
        expected = SAST_RULES if product == "sast" else SCA_RULES
        stage("configured-rules")
        configured = {rule.get("id") for rule in response["config"]["rules"]["rules"]}
        count("configured_expected", len(expected & configured))
        stage("expected-findings")
        known = [result for result in results if result.get("check_id") in expected]
        count("expected", known)
        count("unexpected", len(results) - len(known))
        require(bool(known) and all(result["check_id"] in configured for result in known))
        allowed_paths = {"probe.go"} if product == "sast" else {"go.mod", "go.sum", "probe.go"}
        stage("finding-paths")
        require(all(isinstance(result.get("path"), str)
                    and result["path"].removeprefix("/src/").removeprefix("./") in allowed_paths
                    for result in known))
        rejected = False
        stage("actual-rejection")
        try:
            security_scan.validate_semgrep(report, response, product)
        except security_scan.ScanFailure:
            rejected = True
        DIAGNOSTICS["actual_rejected"] = "true" if rejected else "false"
        require(rejected)
        # Negative-test technique only: remove expected finding results from a
        # COPY to test product/version/source/resolution proof independently.
        # Preserve all errors, skipped rules, and unexpected findings unchanged.
        stage("proof-copy")
        proof_report = copy.deepcopy(report)
        proof_report["results"] = [result for result in proof_report["results"]
                                   if result.get("check_id") not in expected]
        try:
            files, rules = security_scan.validate_semgrep(proof_report, response, product)
        except security_scan.ScanFailure as error:
            DIAGNOSTICS["validator"] = validator_category(error)
            raise
        DIAGNOSTICS["validator"] = "passed"
        return checkout_revision, revision, len(results), files, rules
    finally:
        preceding_stage = DIAGNOSTICS["stage"]
        stage("container-cleanup")
        try:
            stop_semgrep(work)
        except (Exception, KeyboardInterrupt):
            DIAGNOSTICS["cleanup"] = "failed"
            raise
        DIAGNOSTICS["cleanup"] = "passed"
        stage(preceding_stage)


def main() -> int:
    os.umask(0o077)
    def terminate(signum: int, frame: object) -> None:
        raise ProbeFailure()

    signal.signal(signal.SIGTERM, terminate)
    try:
        stage("initial-trust")
        require(os.environ.get("GITHUB_ACTIONS") == "true"
                and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch")
        stage("argument-selection")
        args = sys.argv[1:]
        require(not args or args in (["semgrep-sast"], ["semgrep-sca"]))
        product = args[0].removeprefix("semgrep-") if args else None
        stage("runner-temp")
        runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
        with tempfile.TemporaryDirectory(prefix="security-negative-", dir=runner_temp) as temp:
            directory = Path(temp)
            with (directory / "private-output").open("w") as output, contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                if product:
                    checkout_revision, revision, findings, files, rules = semgrep_control(directory, product)
                else:
                    stage("gitleaks-controls")
                    binary = security_scan.install_gitleaks(directory)
                    code, version = private_process([str(binary), "version"], directory,
                                                    {"PATH": os.defpath, "HOME": str(directory)})
                    require(code == 0 and version.strip() == security_scan.GITLEAKS_VERSION)
                    removed = production_history(binary, directory)
                    baseline, exact, restored = fingerprint_controls(binary, directory)
        if product:
            print(f"Semgrep {security_scan.SEMGREP_VERSION}: image {security_scan.SEMGREP_IMAGE}; checkout revision {checkout_revision}; fixture revision {revision}")
            print(f"Semgrep {product} negative control passed; findings {findings}; scanned files {files}; applicable rules {rules}; actual report rejected")
            print("Negative-test copy removed only expected results; full product/version/source/resolution proof passed; errors and skipped rules unchanged")
            return 0
        print(f"Gitleaks removed-history control passed; findings {removed}; final tracked files 0")
        print(f"Gitleaks fingerprint control passed; baseline findings {baseline}; exact-ignore findings {exact}")
        print(f"Gitleaks near-miss controls passed; commit findings {restored[0]}; path findings {restored[1]}; rule findings {restored[2]}; line findings {restored[3]}")
        return 0
    except (Exception, KeyboardInterrupt):
        print("Security negative probe failed; trust, setup, execution, inventory, or detection proof unavailable")
        print(f"Security negative probe diagnostics: stage {DIAGNOSTICS['stage']}; scanner exit {DIAGNOSTICS['scanner_exit']}; "
              f"findings {DIAGNOSTICS['findings']}; expected {DIAGNOSTICS['expected']}; unexpected {DIAGNOSTICS['unexpected']}; "
              f"configured expected {DIAGNOSTICS['configured_expected']}; errors {DIAGNOSTICS['errors']}; "
              f"skipped rules {DIAGNOSTICS['skipped_rules']}; version match {DIAGNOSTICS['version_match']}; "
              f"actual rejected {DIAGNOSTICS['actual_rejected']}; validator {DIAGNOSTICS['validator']}; cleanup {DIAGNOSTICS['cleanup']}; "
              f"report bytes {DIAGNOSTICS['report_bytes']}; config bytes {DIAGNOSTICS['config_bytes']}")
        return 1


if __name__ == "__main__":
    sys.exit(main())
