"""Pinned security scans with private output and version-bound coverage proof."""

from __future__ import annotations

import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import urllib.request
from pathlib import Path


GITLEAKS_VERSION = "8.30.1"
GITLEAKS_ARCHIVE = "gitleaks_8.30.1_linux_x64.tar.gz"
GITLEAKS_SHA256 = "551f6fc83ea457d62a0d98237cbad105af8d557003051f41f3e7ca7b3f2470eb"
SEMGREP_VERSION = "1.180.0"
SEMGREP_IMAGE = "semgrep/semgrep:1.180.0@sha256:529ee8a277ec8adc5b534d7c74eea0a47e9de21d62852b6ba7ac6ba9566845c3"
TOKEN_EXPRESSION = "${{ (github.actor != 'dependabot[bot]' && (github.event_name == 'push' || github.event_name == 'workflow_dispatch' || (github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && github.event.pull_request.user.login != 'dependabot[bot]'))) && secrets.SEMGREP_APP_TOKEN || '' }}"


class ScanFailure(Exception):
    """Only fixed messages belong in this exception."""


def eligible(env: dict, event: dict) -> bool:
    if not env.get("GITHUB_ACTOR") or env["GITHUB_ACTOR"] == "dependabot[bot]":
        return False
    name = env.get("GITHUB_EVENT_NAME")
    if name in ("push", "workflow_dispatch"):
        return bool(env.get("GITHUB_REPOSITORY"))
    if name != "pull_request":
        return False
    try:
        pr = event["pull_request"]
        return (bool(env.get("GITHUB_REPOSITORY"))
                and pr["head"]["repo"]["full_name"] == env["GITHUB_REPOSITORY"]
                and bool(pr["user"]["login"])
                and pr["user"]["login"] != "dependabot[bot]")
    except (KeyError, TypeError):
        return False


def run_private(args: list[str], directory: Path, env: dict | None = None,
                timeout: int = 900) -> str:
    with (directory / "stdout").open("wb") as stdout, (directory / "stderr").open("wb") as stderr:
        result = subprocess.run(args, stdout=stdout, stderr=stderr, env=env,
                                timeout=timeout, check=False)
    if result.returncode != 0:
        raise ScanFailure("scanner process failed; check authentication, products, policies, and network")
    return (directory / "stdout").read_text(encoding="utf-8")


def install_gitleaks(directory: Path) -> Path:
    archive = directory / GITLEAKS_ARCHIVE
    url = f"https://github.com/gitleaks/gitleaks/releases/download/v{GITLEAKS_VERSION}/{GITLEAKS_ARCHIVE}"
    with urllib.request.urlopen(url, timeout=60) as response, archive.open("wb") as output:
        shutil.copyfileobj(response, output)
    if hashlib.sha256(archive.read_bytes()).hexdigest() != GITLEAKS_SHA256:
        raise ScanFailure("Gitleaks archive checksum mismatch")
    binary = directory / "gitleaks"
    with tarfile.open(archive, "r:gz") as package:
        member = package.getmember("gitleaks")
        if not member.isfile():
            raise ScanFailure("Gitleaks archive has no regular binary")
        with package.extractfile(member) as source, binary.open("wb") as output:
            shutil.copyfileobj(source, output)
    binary.chmod(0o700)
    return binary


def gitleaks(source: Path, directory: Path) -> tuple[int, int]:
    binary = install_gitleaks(directory)
    env = {"PATH": os.defpath, "HOME": str(directory)}
    if run_private([str(binary), "version"], directory, env).strip() != GITLEAKS_VERSION:
        raise ScanFailure("Gitleaks version mismatch")
    config = directory / "gitleaks.toml"
    config.write_text("[extend]\nuseDefault = true\n")
    ignore = directory / "gitleaksignore"
    ignore.write_text("")
    report = directory / "gitleaks.json"
    scan_failed = False
    try:
        run_private([str(binary), "git", str(source), "--log-opts=--all", "--redact=100",
                     "--ignore-gitleaks-allow", "--exit-code=1", "--config", str(config),
                     "--gitleaks-ignore-path", str(ignore), "--report-format=json",
                     "--report-path", str(report)], directory, env)
    except ScanFailure:
        scan_failed = True
    try:
        findings = json.loads(report.read_text())
    except (OSError, ValueError):
        raise ScanFailure("Gitleaks execution failed; check configuration and Git history") from None
    if isinstance(findings, list) and findings and all(isinstance(finding, dict) for finding in findings):
        raise ScanFailure(f"Gitleaks findings; count {len(findings)}; review and remediate secrets")
    if scan_failed or findings != []:
        raise ScanFailure("Gitleaks execution failed; check configuration and Git history")
    return (0, 0)


def semgrep_command(product: str, source: Path, directory: Path, revision: str,
                    env: dict) -> tuple[list[str], dict]:
    # Docker receives the token by environment name, never as an argument value.
    child_env = {"PATH": os.defpath, "SEMGREP_APP_TOKEN": env["SEMGREP_APP_TOKEN"]}
    metadata = {"SEMGREP_REPO_NAME": env["GITHUB_REPOSITORY"],
                "SEMGREP_REPO_URL": "https://github.com/" + env["GITHUB_REPOSITORY"],
                "SEMGREP_BRANCH": env.get("GITHUB_REF_NAME", ""),
                "SEMGREP_COMMIT": revision,
                "SEMGREP_JOB_URL": "https://github.com/" + env["GITHUB_REPOSITORY"] + "/actions/runs/" + env.get("GITHUB_RUN_ID", ""),
                "HOME": "/private/home", "SEMGREP_SEND_METRICS": "off"}
    args = ["docker", "run", "--rm", "--platform", "linux/amd64",
            "--user", f"{os.getuid()}:{os.getgid()}",
            "--cidfile", str(directory / "container-id"),
            "--mount", f"type=bind,src={source},dst=/src,readonly",
            "--mount", f"type=bind,src={directory},dst=/private",
            "--workdir", "/src", "--env", "SEMGREP_APP_TOKEN"]
    for key, value in metadata.items():
        child_env[key] = value
        args.extend(["--env", key])
    args += [SEMGREP_IMAGE, "semgrep", "ci", "--code" if product == "sast" else "--supply-chain",
             "--oss-only", "--no-suppress-errors", "--disable-nosem", "--json",
             "--x-dump-scan-config-path", "/private/scan-config.json"]
    return args, child_env


def validate_semgrep(report: dict, response: dict, product: str) -> tuple[int, int]:
    # Shapes match v1.180.0 ScanResponse and CliOutput, not private dependency internals.
    try:
        if (report["version"] != SEMGREP_VERSION or report["errors"] != []
                or report["skipped_rules"] != [] or report["results"] != []):
            raise ScanFailure("Semgrep findings, errors, skipped rules, or version mismatch")
        config = response["config"]
        products = response["info"]["enabled_products"]
        if (not isinstance(products, list) or not all(isinstance(value, str) for value in products)
                or product not in products
                or response["engine_params"].get("always_suppress_errors", False) is not False
                or config.get("project_merge_base") not in (None, "")):
            raise ScanFailure("Semgrep product or full-scan proof missing")
        rules = config["rules"]["rules"]
        if not isinstance(rules, list) or not rules or not all(isinstance(rule, dict) for rule in rules):
            raise ScanFailure("Semgrep applicable product rules missing")
        if product == "sca":
            applicable = []
            for rule in rules:
                if "r2c-internal-project-depends-on" not in rule:
                    continue
                policy = rule["r2c-internal-project-depends-on"]
                if not isinstance(policy, dict):
                    raise ScanFailure("Semgrep dependency rule ecosystem proof malformed")
                dependencies = policy.get("depends-on-either", [policy])
                if (not isinstance(dependencies, list) or not dependencies
                        or not all(isinstance(dependency, dict)
                                   and isinstance(dependency.get("namespace"), str)
                                   and bool(dependency["namespace"]) for dependency in dependencies)):
                    raise ScanFailure("Semgrep dependency rule ecosystem proof malformed")
                if any(dependency["namespace"].lower() == "gomod" for dependency in dependencies):
                    applicable.append(rule)
        else:
            applicable = [rule for rule in rules if (
                "r2c-internal-project-depends-on" not in rule
                and rule.get("metadata", {}).get("product") != "secrets"
                and isinstance(rule.get("languages"), list)
                and "go" in rule.get("languages", []))]
        if not applicable:
            raise ScanFailure("Semgrep applicable product rules missing")
        scanned = report["paths"]["scanned"]
        if not isinstance(scanned, list) or not all(isinstance(path, str) for path in scanned):
            raise ScanFailure("Semgrep scanned source proof missing")
        if product == "sast":
            if not any(path.endswith(".go") for path in scanned):
                raise ScanFailure("Semgrep scanned Go source missing")
        else:
            subprojects = report["subprojects"]
            if not isinstance(subprojects, list) or not subprojects:
                raise ScanFailure("Semgrep root Go dependency resolution missing")
            root_resolved = False
            for project in subprojects:
                if project["resolved"] is not True or project.get("unresolved_reason"):
                    raise ScanFailure("Semgrep unresolved dependency subproject")
                stats = project["resolved_stats"]
                sources = project["dependency_sources"]
                if not isinstance(sources, list) or not all(isinstance(path, str) for path in sources):
                    raise ScanFailure("Semgrep dependency source proof missing")
                if "go.mod" in sources or "./go.mod" in sources or "/src/go.mod" in sources:
                    count = stats["dependency_count"]
                    if (type(count) is not int or count <= 0 or stats["ecosystem"] != "gomod"
                            or stats["resolution_method"] != "LockfileParsing"):
                        raise ScanFailure("Semgrep root Go dependency resolution missing")
                    root_resolved = True
            if not root_resolved:
                raise ScanFailure("Semgrep root Go dependency resolution missing")
        return len(scanned), len(applicable)
    except (KeyError, TypeError, AttributeError):
        raise ScanFailure("Semgrep report or configuration proof malformed") from None


def main(argv: list[str] | None = None) -> int:
    argv = sys.argv[1:] if argv is None else argv
    if len(argv) != 1 or argv[0] not in ("gitleaks", "semgrep-sast", "semgrep-sca"):
        print("Security scan failed: select one supported scanner")
        return 1
    scanner = argv[0]
    try:
        if scanner != "gitleaks":
            event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
            if not eligible(os.environ, event):
                raise ScanFailure("event is not eligible for the repository scan token")
            if not os.environ.get("SEMGREP_APP_TOKEN"):
                raise ScanFailure("SEMGREP_APP_TOKEN is missing; configure a Service (CI) Agent token")
        source = Path.cwd().resolve()
        with tempfile.TemporaryDirectory(prefix="security-scan-", dir=os.environ.get("RUNNER_TEMP")) as temp:
            directory = Path(temp)
            revision = run_private(["git", "rev-parse", "HEAD"], directory,
                                   {"PATH": os.defpath}, timeout=30).strip()
            if not re.fullmatch(r"[0-9a-f]{40}", revision):
                raise ScanFailure("checked-out revision is invalid")
            if scanner == "gitleaks":
                counts = gitleaks(source, directory)
                version = GITLEAKS_VERSION
            else:
                (directory / "home").mkdir(mode=0o700)
                product = scanner.removeprefix("semgrep-")
                args, env = semgrep_command(product, source, directory, revision, os.environ)
                try:
                    run_private(args, directory, env)
                    counts = validate_semgrep(json.loads((directory / "stdout").read_text()),
                                              json.loads((directory / "scan-config.json").read_text()), product)
                finally:
                    cidfile = directory / "container-id"
                    if cidfile.exists():
                        cid = cidfile.read_text().strip()
                        if re.fullmatch(r"[0-9a-f]{64}", cid):
                            # --rm already removes a completed container. On a client
                            # timeout, stop any surviving container before deleting reports.
                            try:
                                run_private(["docker", "rm", "--force", cid], directory,
                                            {"PATH": os.defpath}, timeout=30)
                            except ScanFailure:
                                pass
                version = SEMGREP_VERSION
            if scanner == "gitleaks":
                print(f"gitleaks {version}: revision {revision}; all fetched history scanned; findings 0")
            else:
                print(f"{scanner} {version}: revision {revision}; scanned files {counts[0]}; applicable rules {counts[1]}; findings 0")
                print("Semgrep project: https://semgrep.dev/orgs/-/projects")
        return 0
    except ScanFailure as error:
        print(f"Security scan failed: {error}")
    except subprocess.TimeoutExpired:
        print("Security scan failed: scanner timed out")
    except (OSError, ValueError, KeyError, TypeError, tarfile.TarError):
        print("Security scan failed: download, execution, or report unavailable; check scanner setup")
    return 1


if __name__ == "__main__":
    sys.exit(main())
