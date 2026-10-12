"""Temporary real SAST controls. Never merge this branch or fixture inventories."""

from __future__ import annotations

import contextlib
import copy
import hashlib
import importlib.util
import io
import json
import os
import re
import secrets
import signal
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True

# Fixed independently reviewed implementation. Recapture requires a reviewed
# literal update in this file and the workflow; no caller override is supported.
HELPER_TARGET = "969b64ece768478aebc21c60a01c1ddaad5fb412"
HELPER_TARGET_PENDING = False
VERSION = "1.180.0"
IMAGE = "semgrep/semgrep:1.180.0@sha256:529ee8a277ec8adc5b534d7c74eea0a47e9de21d62852b6ba7ac6ba9566845c3"
TLS_RULE = "problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification"
# Distinct enrolled rule IDs in the safe capture from run 38230074648.
ENROLLED_RULE_IDS = (
    "go.lang.security.audit.crypto.missing-ssl-minversion.missing-ssl-minversion",
    "go.lang.security.audit.dangerous-exec-command.dangerous-exec-command",
    "go.lang.security.audit.database.string-formatted-query.string-formatted-query",
    "go.lang.security.audit.xss.no-direct-write-to-responsewriter.no-direct-write-to-responsewriter",
    "go.lang.security.audit.xss.no-io-writestring-to-responsewriter.no-io-writestring-to-responsewriter",
    "go.lang.security.injection.open-redirect.open-redirect",
    "html.security.audit.missing-integrity.missing-integrity",
    "problem-based-packs.insecure-transport.go-stdlib.bypass-tls-verification.bypass-tls-verification",
    "python.lang.security.audit.dynamic-urllib-use-detected.dynamic-urllib-use-detected",
    "trailofbits.go.invalid-usage-of-modified-variable.invalid-usage-of-modified-variable",
    "trailofbits.go.iterate-over-empty-map.iterate-over-empty-map",
)
MANIFEST = "tools/ci/semgrep-legacy.json"
STAGES = frozenset({
    "setup", "load-target", "fixture-inputs", "baseline-canary", "baseline-manifest",
    "baseline-acceptance", "baseline-counts", "extra-input", "rule-comparison",
    "extra-findings", "extra-parser", "baseline-second-config", "extra-rejection",
    "extra-removal", "source-controls", "parser-controls", "privacy-control",
    "scanner-command", "scanner-execution", "scanner-exit", "report-files",
    "report-decode", "report-shape", "container-cleanup",
})
DIAGNOSTICS = {"stage": "setup", "scan": "none", "scanner_exit": "unavailable",
               "findings": "unavailable", "errors": "unavailable",
               "validator": "unavailable", "cleanup": "unavailable"}
RULE_COMPARISON = []


class ProbeFailure(Exception):
    """Only fixed status is public."""


def require(condition: bool) -> None:
    if not condition:
        raise ProbeFailure()


def stage(value: str) -> None:
    require(value in STAGES)
    DIAGNOSTICS["stage"] = value


def diagnostic_output() -> None:
    print(f"SAST transition probe diagnostics: stage {DIAGNOSTICS['stage']}; scan {DIAGNOSTICS['scan']}; "
          f"scanner exit {DIAGNOSTICS['scanner_exit']}; findings {DIAGNOSTICS['findings']}; "
          f"errors {DIAGNOSTICS['errors']}; validator {DIAGNOSTICS['validator']}; cleanup {DIAGNOSTICS['cleanup']}")
    for line in RULE_COMPARISON:
        print(line)


def diagnostic_rule_hash(rule: dict) -> str:
    # Independent of the production rule_hash, including any future amendment.
    encoded = json.dumps(rule, sort_keys=True, separators=(",", ":"),
                         ensure_ascii=True, allow_nan=False).encode("utf-8")
    return hashlib.sha256(encoded).hexdigest()


def compare_enrolled_rules(first: dict, second: dict) -> None:
    # Fixed rule and field labels only. Pinned rule.py documents semgrep.dev/src
    # and removes metadata from its own full_hash because metadata can be user-specific.
    require(len(ENROLLED_RULE_IDS) == len(set(ENROLLED_RULE_IDS)) == 11)
    chosen, missing_counts, duplicate_counts = [], [], []
    for response in (first, second):
        rules = response["config"]["rules"]["rules"]
        require(isinstance(rules, list) and all(isinstance(rule, dict) for rule in rules))
        matched, missing_count, duplicate_count = {}, 0, 0
        for identity in ENROLLED_RULE_IDS:
            matches = [rule for rule in rules if rule.get("id") == identity]
            missing_count += not matches
            duplicate_count += len(matches) > 1
            if len(matches) == 1:
                matched[identity] = matches[0]
        chosen.append(matched)
        missing_counts.append(missing_count)
        duplicate_counts.append(duplicate_count)
    RULE_COMPARISON.append(f"SAST fixed rule availability: first missing {missing_counts[0]}; "
                           f"second missing {missing_counts[1]}; first duplicate {duplicate_counts[0]}; "
                           f"second duplicate {duplicate_counts[1]}")
    require(missing_counts == [0, 0] and duplicate_counts == [0, 0])
    missing = object()

    def src(rule: dict) -> object:
        metadata = rule.get("metadata")
        vendor = metadata.get("semgrep.dev") if isinstance(metadata, dict) else None
        return vendor.get("src", missing) if isinstance(vendor, dict) else missing

    full_changed = metadata_changed = without_metadata_changed = knownsrc_changed = 0
    for identity in ENROLLED_RULE_IDS:
        left, right = chosen[0][identity], chosen[1][identity]
        full_changed += diagnostic_rule_hash(left) != diagnostic_rule_hash(right)
        without_metadata_changed += (
            diagnostic_rule_hash({key: value for key, value in left.items() if key != "metadata"})
            != diagnostic_rule_hash({key: value for key, value in right.items() if key != "metadata"}))
        metadata_changed += (
            diagnostic_rule_hash({key: value for key, value in left.items() if key == "metadata"})
            != diagnostic_rule_hash({key: value for key, value in right.items() if key == "metadata"}))
        left_src, right_src = src(left), src(right)
        knownsrc_changed += (
            diagnostic_rule_hash({} if left_src is missing else {"src": left_src})
            != diagnostic_rule_hash({} if right_src is missing else {"src": right_src}))
    RULE_COMPARISON.append(f"SAST fixed enrolled rule comparison: rules compared 11; full changed count {full_changed}; "
                           f"metadata changed count {metadata_changed}; "
                           f"without-metadata changed count {without_metadata_changed}; "
                           f"knownsrc changed count {knownsrc_changed}")


def git(repo: Path, work: Path, env: dict, *args: str) -> bytes:
    output = work / "git-stdout"
    with output.open("wb") as stdout, (work / "git-stderr").open("wb") as stderr:
        process = subprocess.run(["git", "-c", "core.hooksPath=/dev/null", "-C", str(repo), *args],
                                 env=env, stdout=stdout, stderr=stderr, timeout=30, check=False)
    require(process.returncode == 0 and output.stat().st_size <= 10_000_000)
    return output.read_bytes()


def git_environment(home: Path) -> dict:
    return {"PATH": os.defpath, "HOME": str(home), "GIT_CONFIG_GLOBAL": os.devnull,
            "GIT_CONFIG_NOSYSTEM": "1", "GIT_TERMINAL_PROMPT": "0",
            "GIT_AUTHOR_NAME": "Synthetic probe", "GIT_AUTHOR_EMAIL": "probe@example.invalid",
            "GIT_COMMITTER_NAME": "Synthetic probe", "GIT_COMMITTER_EMAIL": "probe@example.invalid"}


def target_bytes(source: Path, work: Path, env: dict, path: str) -> bytes:
    current = source
    require(stat.S_ISDIR(current.lstat().st_mode))
    for part in path.split("/")[:-1]:
        current /= part
        require(stat.S_ISDIR(current.lstat().st_mode))
    target = current / path.split("/")[-1]
    require(stat.S_ISREG(target.lstat().st_mode) and target.stat().st_size <= 10_000_000)
    record = git(source, work, env, "ls-tree", "HEAD", "--", path).decode().strip()
    require(record.startswith(("100644 blob ", "100755 blob ")) and record.endswith("\t" + path))
    data = target.read_bytes()
    require(data == git(source, work, env, "show", "HEAD:" + path))
    return data


def load_helpers(source: Path, work: Path, env: dict) -> tuple[object, object]:
    require(git(source, work, env, "rev-parse", "HEAD").decode().strip() == HELPER_TARGET)
    loaded = []
    for name in ("semgrep_inventory", "security_scan"):
        target_bytes(source, work, env, "tools/ci/" + name + ".py")
        spec = importlib.util.spec_from_file_location(name, source / "tools/ci" / (name + ".py"))
        require(spec is not None and spec.loader is not None)
        module = importlib.util.module_from_spec(spec)
        sys.modules[name] = module
        spec.loader.exec_module(module)
        loaded.append(module)
    inventory, helper = loaded
    require(helper.SEMGREP_VERSION == VERSION and helper.SEMGREP_IMAGE == IMAGE)
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    require(helper.eligible(os.environ, event))
    return helper, inventory


def commit(repo: Path, work: Path, env: dict, *paths: str) -> None:
    git(repo, work, env, "add", "--", *paths)
    git(repo, work, env, "commit", "-m", "Update disposable scanner control inputs")


def tls_source(canary: str) -> str:
    return ('package probe\n\nimport "crypto/tls"\n\n'
            'func UnsafeConfig() *tls.Config {\n'
            '\treturn &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: true} // '
            + canary + '\n}\n')


def stop_container(helper: object, work: Path) -> None:
    cidfile = work / "container-id"
    if not cidfile.exists():
        return
    cid = cidfile.read_text().strip()
    require(re.fullmatch(r"[0-9a-f]{64}", cid) is not None)
    cleanup = work / "cleanup"
    cleanup.mkdir(mode=0o700)
    try:
        helper.run_private(["docker", "rm", "--force", cid], cleanup,
                           {"PATH": os.defpath}, timeout=30)
    except helper.ScanFailure:
        pass
    remaining = helper.run_private(
        ["docker", "ps", "--all", "--no-trunc", "--filter", f"id={cid}", "--format", "{{.ID}}"],
        cleanup, {"PATH": os.defpath}, timeout=30)
    require(remaining.strip() == "")


def scan(helper: object, inventory: object, repo: Path, private: Path,
         env: dict, name: str) -> tuple[dict, dict]:
    require(name in ("baseline-scan", "extra-finding-scan"))
    DIAGNOSTICS["scan"] = "baseline" if name == "baseline-scan" else "extra"
    DIAGNOSTICS["scanner_exit"] = "unavailable"
    DIAGNOSTICS["findings"] = "unavailable"
    DIAGNOSTICS["errors"] = "unavailable"
    DIAGNOSTICS["validator"] = "unavailable"
    DIAGNOSTICS["cleanup"] = "unavailable"
    stage("scanner-command")
    work = private / name
    work.mkdir(mode=0o700)
    (work / "home").mkdir(mode=0o700)
    revision = git(repo, private, env, "rev-parse", "HEAD").decode().strip()
    require(re.fullmatch(r"[0-9a-f]{40}", revision) is not None)
    args, child_env = helper.semgrep_command("sast", repo, work, revision, os.environ)
    try:
        stage("scanner-execution")
        with (work / "stdout").open("wb") as stdout, (work / "stderr").open("wb") as stderr:
            process = subprocess.run(args, env=child_env, stdout=stdout, stderr=stderr,
                                     timeout=600, check=False)
        # Exactly the production acceptance contract, including findings-policy exits.
        DIAGNOSTICS["scanner_exit"] = "zero" if process.returncode == 0 else "nonzero"
        stage("scanner-exit")
        require(process.returncode == 0)
        report_path, config_path = work / "stdout", work / "scan-config.json"
        stage("report-files")
        require(report_path.is_file() and report_path.stat().st_size <= 32_000_000)
        require(config_path.is_file() and config_path.stat().st_size <= 320_000_000)
        stage("report-decode")
        report = inventory.strict_json_loads(report_path.read_text())
        response = inventory.strict_json_loads(config_path.read_text())
        stage("report-shape")
        require(isinstance(report, dict) and isinstance(response, dict))
        for name, field in (("findings", "results"), ("errors", "errors")):
            if isinstance(report.get(field), list):
                DIAGNOSTICS[name] = min(len(report[field]), 1_000_000)
        require(report.get("version") == VERSION and report.get("skipped_rules") == [])
        return report, response
    finally:
        preceding_stage = DIAGNOSTICS["stage"]
        stage("container-cleanup")
        try:
            stop_container(helper, work)
        except (Exception, KeyboardInterrupt):
            DIAGNOSTICS["cleanup"] = "failed"
            raise
        DIAGNOSTICS["cleanup"] = "passed"
        stage(preceding_stage)


def path(value: str) -> str:
    require(isinstance(value, str))
    value = value.removeprefix("/src/")
    require(value in ("probe.go", "extra.go", "run-embed.sh"))
    return value


def positions(value: dict) -> dict:
    require(isinstance(value, dict) and type(value.get("line")) is int
            and type(value.get("col")) is int)
    return {"line": value["line"], "col": value["col"], "offset": value.get("offset")}


def fixture_manifest(report: dict, response: dict, repo: Path, inventory: object) -> dict:
    rules = response["config"]["rules"]["rules"]
    rule_objects = {}
    for rule in rules:
        identity = rule["id"]
        require(identity not in rule_objects)
        rule_objects[identity] = rule
    def rule_hash(identity: str | None) -> str | None:
        if identity is None:
            return None
        require(identity in rule_objects)
        return inventory.rule_hash(rule_objects[identity])

    def entry(name: str, filename: str, disposition: str) -> dict:
        return {"id": name, "path": filename,
                "source_sha256": hashlib.sha256((repo / filename).read_bytes()).hexdigest(),
                "supporting_sources": [], "disposition": disposition,
                "rationale": "Self-contained disposable scanner fixture; never production enrollment",
                "evidence": ["docs/specs/security-ci.md"]}

    findings = []
    require(len(report["results"]) == 1)
    for finding in report["results"]:
        require(finding["check_id"] == TLS_RULE and path(finding["path"]) == "probe.go")
        record = entry("fixture-tls", "probe.go", "legacy-risk")
        record.update(rule_id=TLS_RULE, rule_sha256=rule_hash(TLS_RULE),
                      start=positions(finding["start"]), end=positions(finding["end"]))
        findings.append(record)
    errors = []
    require(len(report["errors"]) == 1)
    for error in report["errors"]:
        kind = error["type"]
        require(isinstance(kind, list) and len(kind) == 2 and kind[0] == "PartialParsing"
                and isinstance(kind[1], list) and bool(kind[1]))
        locations = []
        for location in kind[1]:
            require(path(location["path"]) == "run-embed.sh")
            locations.append({"path": "run-embed.sh", "start": positions(location["start"]),
                              "end": positions(location["end"])})
        spans = None
        if "spans" in error:
            require(isinstance(error["spans"], list))
            spans = []
            allowed = {"file", "start", "end", "source_hash", "config_start", "config_end",
                       "config_path", "context_start", "context_end"}
            for span in error["spans"]:
                require(isinstance(span, dict) and set(span) <= allowed
                        and all(span.get(field) is None for field in
                                ("config_start", "config_end", "config_path", "context_start", "context_end")))
                require("source_hash" not in span or isinstance(span["source_hash"], str))
                require(path(span["file"]) == "run-embed.sh")
                spans.append({"path": "run-embed.sh", "start": positions(span["start"]),
                              "end": positions(span["end"])})
            if spans:
                # Compare complete lists without dropping duplicate occurrences.
                encoded = lambda value: json.dumps(value, sort_keys=True, allow_nan=False)
                require(sorted(map(encoded, spans)) == sorted(map(encoded, locations)))
        require(bool(spans))
        record = entry("fixture-parser", "run-embed.sh", "coverage-gap")
        identity = error.get("rule_id")
        record.update(code=error["code"], level=error["level"], rule_id=identity,
                      rule_sha256=rule_hash(identity), locations=locations, spans=spans)
        errors.append(record)
    return {"schema_version": 1, "semgrep_version": VERSION, "semgrep_image": IMAGE,
            "findings": findings, "partial_parsing": errors}


def acceptance(helper: object, report: dict, response: dict, repo: Path,
               canary: str, expected: bool, rejection_summary: str | None = None) -> dict | None:
    DIAGNOSTICS["validator"] = "unavailable"
    output = io.StringIO()
    failure = None
    result = None
    with contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
        try:
            result = helper.validate_sast_transition(report, response, repo)
        except helper.ScanFailure as error:
            failure = error
        except (Exception, KeyboardInterrupt):
            DIAGNOSTICS["validator"] = "unexpected-exception"
            raise
    DIAGNOSTICS["validator"] = ("accepted" if failure is None else {
        "Semgrep transition configured rule binding changed": "rule-binding",
        "Semgrep transition has unmatched records or stale entries": "unmatched-or-stale",
    }.get(str(failure), "other-fixed-category"))
    require(canary not in output.getvalue() and (failure is None or canary not in str(failure)))
    require((failure is None) == expected)
    if rejection_summary is not None:
        require(failure is not None
                and str(failure) == "Semgrep transition has unmatched records or stale entries"
                and output.getvalue().splitlines().count(rejection_summary) == 1)
    return result[1] if result is not None else None


def parser_identity(error: dict) -> dict:
    kind = error["type"]
    require(isinstance(kind, list) and len(kind) == 2 and kind[0] == "PartialParsing")
    def normalized(location: dict, key: str) -> str:
        return json.dumps({"path": path(location[key]), "start": positions(location["start"]),
                           "end": positions(location["end"])}, sort_keys=True, allow_nan=False)

    spans = None
    if "spans" in error:
        require(isinstance(error["spans"], list))
        spans = sorted(normalized(span, "file") for span in error["spans"])
    return {"code": error["code"], "level": error["level"], "rule_id": error.get("rule_id"),
            "path": path(error["path"]) if error.get("path") is not None else None,
            "locations": sorted(normalized(location, "path") for location in kind[1]), "spans": spans}


def controls(helper: object, inventory: object, source: Path, private: Path, env: dict) -> None:
    stage("fixture-inputs")
    repo = private / "fixture"
    repo.mkdir(mode=0o700)
    git(repo, private, env, "init", "--initial-branch=probe")
    canary = "transition-private-" + secrets.token_hex(16)
    (repo / "probe.go").write_text(tls_source(canary))
    parser = target_bytes(source, private, env, "deploy/aws/run-embed.sh")
    (repo / "run-embed.sh").write_bytes(parser)
    commit(repo, private, env, "probe.go", "run-embed.sh")
    report, response = scan(helper, inventory, repo, private, env, "baseline-scan")
    stage("baseline-canary")
    require(canary in json.dumps(report))
    stage("baseline-manifest")
    manifest = fixture_manifest(report, response, repo, inventory)
    destination = repo / MANIFEST
    destination.parent.mkdir(parents=True)
    destination.write_text(json.dumps(manifest, sort_keys=True, allow_nan=False))
    commit(repo, private, env, MANIFEST)
    stage("baseline-acceptance")
    counts = acceptance(helper, report, response, repo, canary, True)
    stage("baseline-counts")
    require(counts["total_findings"] == 1 and counts["deferred_legacy_risks"] == 1
            and counts["accepted_parser_gaps"] == 1 and counts["unmatched_records"] == 0
            and counts["stale_entries"] == 0)

    stage("extra-input")
    (repo / "extra.go").write_text(tls_source(canary).replace("UnsafeConfig", "AnotherConfig"))
    commit(repo, private, env, "extra.go")
    extra_report, extra_response = scan(helper, inventory, repo, private, env, "extra-finding-scan")
    stage("rule-comparison")
    compare_enrolled_rules(response, extra_response)
    stage("extra-findings")
    require(len(extra_report["results"]) == 2
            and {path(finding["path"]) for finding in extra_report["results"]} == {"probe.go", "extra.go"}
            and all(finding["check_id"] == TLS_RULE for finding in extra_report["results"]))
    existing = next(finding for finding in extra_report["results"] if path(finding["path"]) == "probe.go")
    original = report["results"][0]
    require(all(existing[field] == original[field] for field in ("check_id", "start", "end")))
    stage("extra-parser")
    require(len(extra_report["errors"]) == 1
            and parser_identity(extra_report["errors"][0]) == parser_identity(report["errors"][0]))
    # Saved actual baseline with the second scan's configuration: prove rule,
    # product, parser, and source proof still pass before isolating the new finding.
    stage("baseline-second-config")
    baseline_again = acceptance(helper, report, extra_response, repo, canary, True)
    require(baseline_again == counts)
    summary = ("Semgrep transition: total findings 2; accepted false positives 0; accepted audits 0; "
               "deferred legacy risks 1; accepted parser gaps 1; unmatched records 1; stale entries 0")
    stage("extra-rejection")
    acceptance(helper, extra_report, extra_response, repo, canary, False, summary)
    stage("extra-removal")
    git(repo, private, env, "rm", "--", "extra.go")
    git(repo, private, env, "commit", "-m", "Remove extra disposable finding")
    acceptance(helper, report, response, repo, canary, True)

    # Source mutation controls use the saved actual report, without a rescan.
    stage("source-controls")
    for filename, suffix in (("probe.go", b"\n// changed outside finding\n"),
                             ("run-embed.sh", b"\n# changed outside parser locations\n")):
        original = (repo / filename).read_bytes()
        (repo / filename).write_bytes(original + suffix)
        acceptance(helper, report, response, repo, canary, False)
        commit(repo, private, env, filename)
        acceptance(helper, report, response, repo, canary, False)
        (repo / filename).write_bytes(original)
        commit(repo, private, env, filename)
        acceptance(helper, report, response, repo, canary, True)

    # Explicit report-copy mutation: duplicate one real parser location to prove
    # that matching the first location alone cannot authorize additional ones.
    stage("parser-controls")
    augmented = copy.deepcopy(report)
    augmented["errors"][0]["type"][1].append(copy.deepcopy(report["errors"][0]["type"][1][0]))
    augmented["errors"][0]["spans"].append(copy.deepcopy(report["errors"][0]["spans"][0]))
    acceptance(helper, augmented, response, repo, canary, False)
    # Additional span presence, geometry, and multiplicity controls use labeled
    # copies of the actual baseline. No scanner records or findings are removed.
    extra_span = copy.deepcopy(report)
    extra_span["errors"][0]["spans"].append(copy.deepcopy(report["errors"][0]["spans"][0]))
    acceptance(helper, extra_span, response, repo, canary, False)
    missing_spans = copy.deepcopy(report)
    missing_spans["errors"][0].pop("spans")
    acceptance(helper, missing_spans, response, repo, canary, False)
    null_spans = copy.deepcopy(report)
    null_spans["errors"][0]["spans"] = None
    acceptance(helper, null_spans, response, repo, canary, False)
    context_span = copy.deepcopy(report)
    context_span["errors"][0]["spans"][0]["context_start"] = {"line": 1, "col": 1, "offset": 0}
    acceptance(helper, context_span, response, repo, canary, False)
    unknown_span = copy.deepcopy(report)
    unknown_span["errors"][0]["spans"][0]["payload"] = canary
    acceptance(helper, unknown_span, response, repo, canary, False)
    stage("privacy-control")
    unsafe = copy.deepcopy(report)
    unsafe["results"][0]["path"] = "unsafe\n" + canary
    acceptance(helper, unsafe, response, repo, canary, False)


def main() -> int:
    os.umask(0o077)
    def terminate(signum: int, frame: object) -> None:
        raise ProbeFailure()

    signal.signal(signal.SIGTERM, terminate)
    try:
        require(not HELPER_TARGET_PENDING and len(sys.argv) == 1)
        require(os.environ.get("GITHUB_ACTIONS") == "true"
                and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
                and bool(os.environ.get("GITHUB_ACTOR"))
                and os.environ["GITHUB_ACTOR"] != "dependabot[bot]"
                and bool(os.environ.get("GITHUB_REPOSITORY")))
        require(bool(os.environ.get("SEMGREP_APP_TOKEN")))
        source = Path(os.environ["GITHUB_WORKSPACE"]).resolve(strict=True) / "target"
        runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
        with tempfile.TemporaryDirectory(prefix="semgrep-transition-", dir=runner_temp) as temp:
            private = Path(temp)
            env = git_environment(private)
            with (private / "private-output").open("w") as output, contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                stage("load-target")
                helper, inventory = load_helpers(source, private, env)
                controls(helper, inventory, source, private, env)
        diagnostic_output()
        print(f"Semgrep {VERSION}: image {IMAGE}; reviewed helper revision {HELPER_TARGET}")
        print("Real scans 2; baseline accepted findings 1; baseline accepted parser gaps 1; new finding rejected")
        print("Saved-report controls: working and committed source drift rejected for 2 files; parser location/span mutations rejected; canary privacy passed")
        print("Fixture inventories remained disposable; no production enrollment or fixture execution")
        return 0
    except (Exception, KeyboardInterrupt):
        print("SAST transition probe failed; reviewed target, trust, scanner, acceptance, or privacy proof unavailable")
        diagnostic_output()
        return 1


if __name__ == "__main__":
    sys.exit(main())
