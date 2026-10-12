"""Temporary read-only location capture. This branch must never merge."""

from __future__ import annotations

import contextlib
import copy
import hashlib
import importlib.util
import json
import os
import re
import signal
import stat
import subprocess
import sys
import tempfile
from pathlib import Path

sys.dont_write_bytecode = True

TARGET = "179797a7cde50d70478b90fd8e9c0ff058cffe28"
VERSION = "1.180.0"
IMAGE = "semgrep/semgrep:1.180.0@sha256:529ee8a277ec8adc5b534d7c74eea0a47e9de21d62852b6ba7ac6ba9566845c3"
SCHEMA_SOURCE = "https://github.com/semgrep/semgrep/blob/v1.180.0/cli/src/semgrep/semgrep_interfaces/semgrep_output_v1.py"


class CaptureFailure(Exception):
    """Only fixed capture status is public."""


def require(condition: bool) -> None:
    if not condition:
        raise CaptureFailure()


def canonical(value: object) -> bytes:
    return json.dumps(value, sort_keys=True, separators=(",", ":"),
                      ensure_ascii=True, allow_nan=False).encode("utf-8")


def strict_json(path: Path) -> object:
    def pairs(items: list[tuple]) -> dict:
        result = {}
        for key, value in items:
            require(key not in result)
            result[key] = value
        return result

    def nonfinite(value: str) -> None:
        raise CaptureFailure()

    return json.loads(path.read_text(), object_pairs_hook=pairs, parse_constant=nonfinite)


def git_bytes(source: Path, private: Path, *args: str) -> bytes:
    env = {"PATH": os.defpath, "HOME": str(private), "GIT_CONFIG_NOSYSTEM": "1",
           "GIT_CONFIG_GLOBAL": os.devnull, "GIT_TERMINAL_PROMPT": "0"}
    output = private / "git-stdout"
    with output.open("wb") as stdout, (private / "git-stderr").open("wb") as stderr:
        process = subprocess.run(["git", "-C", str(source), *args], env=env,
                                 stdout=stdout, stderr=stderr, timeout=30, check=False)
    require(process.returncode == 0 and output.stat().st_size <= 10_000_000)
    return output.read_bytes()


def tracked_files(source: Path, private: Path, revision: str) -> dict[str, str]:
    require(git_bytes(source, private, "rev-parse", "HEAD").decode().strip() == revision)
    files = {}
    for record in git_bytes(source, private, "ls-tree", "-r", "-z", revision).split(b"\0"):
        if not record:
            continue
        metadata, path = record.split(b"\t", 1)
        mode, kind, oid = metadata.decode("ascii").split(" ")
        if mode in ("100644", "100755") and kind == "blob":
            require(re.fullmatch(r"[0-9a-f]{40}", oid) is not None)
            files[path.decode("utf-8")] = oid
    return files


def relative_path(value: object) -> str:
    require(isinstance(value, str))
    path = value.removeprefix("/src/")
    require(re.fullmatch(r"[A-Za-z0-9_.\-/]{1,240}", path) is not None
            and all(part not in ("", ".", "..") for part in path.split("/")))
    return path


class SourceProof:
    def __init__(self, source: Path, private: Path, files: dict[str, str], scanned: set[str]):
        self.source = source
        self.private = private
        self.files = files
        self.scanned = scanned
        self.content: dict[str, bytes] = {}

    def file(self, value: object) -> tuple[str, bytes]:
        path = relative_path(value)
        require(path in self.files)
        if path not in self.content:
            current = self.source
            require(stat.S_ISDIR(current.lstat().st_mode))
            for part in path.split("/")[:-1]:
                current /= part
                require(stat.S_ISDIR(current.lstat().st_mode))
            target = current / path.split("/")[-1]
            require(stat.S_ISREG(target.lstat().st_mode) and target.stat().st_size <= 10_000_000)
            data = target.read_bytes()
            require(data == git_bytes(self.source, self.private, "cat-file", "blob", self.files[path]))
            self.content[path] = data
        return path, self.content[path]

    def metadata(self, value: object) -> dict:
        path, data = self.file(value)
        return {"path": path, "source_sha256": hashlib.sha256(data).hexdigest(),
                "path_in_scanned": path in self.scanned}

    def location(self, value: object, path_key: str) -> dict:
        require(isinstance(value, dict))
        path, data = self.file(value[path_key])
        start = position(value["start"], data)
        end = position(value["end"], data)
        require((start["line"], start["col"]) <= (end["line"], end["col"]))
        if start["offset"] is not None and end["offset"] is not None:
            if start["offset"] >= 0 and end["offset"] >= 0:
                require(start["offset"] <= end["offset"])
        return {**self.metadata(path), "start": start, "end": end}


def position(value: object, data: bytes) -> dict:
    require(isinstance(value, dict) and set(value) <= {"line", "col", "offset"})
    line, col = value["line"], value["col"]
    require(type(line) is int and type(col) is int
            and 1 <= line <= 1_000_000 and 1 <= col <= 1_000_000)
    lines = data.split(b"\n")
    require(line <= len(lines) and col <= len(lines[line - 1]) + 1)
    offset = value.get("offset")
    require("offset" not in value or (type(offset) is int and -1 <= offset <= len(data)))
    return {"line": line, "col": col, "offset": offset}


def rule_id(value: object) -> str:
    require(isinstance(value, str)
            and re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,239}", value) is not None
            and all(value.split(".")))
    return value


def normalize_error(error: object, proof: SourceProof, rules: dict[str, str]) -> dict:
    require(isinstance(error, dict))
    kind = error["type"]
    require(isinstance(kind, list) and len(kind) == 2 and kind[0] == "PartialParsing"
            and isinstance(kind[1], list) and 1 <= len(kind[1]) <= 10_000)
    code, level = error["code"], error["level"]
    require(type(code) is int and 0 <= code <= 255 and level in ("error", "warn", "info"))
    error_rule = error.get("rule_id")
    if error_rule is not None:
        error_rule = rule_id(error_rule)
        require(error_rule in rules)
    top_path = error.get("path")
    top = proof.metadata(top_path) if top_path is not None else None
    payload = [proof.location(location, "path") for location in kind[1]]
    raw_spans = error.get("spans")
    spans = None
    if raw_spans is not None:
        require(isinstance(raw_spans, list) and len(raw_spans) <= 10_000)
        spans = []
        for span in raw_spans:
            require(isinstance(span, dict)
                    and all(span.get(key) is None for key in
                            ("config_start", "config_end", "config_path", "context_start", "context_end")))
            spans.append(proof.location(span, "file"))
    # Keep order and duplicates. The two location representations stay separate.
    return {"type": "PartialParsing", "code": code, "level": level, "rule_id": error_rule,
            "rule_sha256": rules[error_rule] if error_rule is not None else None,
            "path": top, "type_locations": payload, "spans": spans}


def inventory(report: dict, response: dict, source: Path, private: Path,
              files: dict[str, str], exit_code: int, helper: object, revision: str) -> dict:
    require(report.get("version") == VERSION and report.get("skipped_rules") == [])
    findings, errors = report["results"], report["errors"]
    require(isinstance(findings, list) and len(findings) == 25
            and isinstance(errors, list) and len(errors) == 3)
    configured = response["config"]["rules"]["rules"]
    require(isinstance(configured, list) and 1 <= len(configured) <= 100_000)
    rules = {}
    for rule in configured:
        require(isinstance(rule, dict))
        identity = rule_id(rule["id"])
        digest = hashlib.sha256(canonical(rule)).hexdigest()
        require(identity not in rules)
        rules[identity] = digest
    scanned_raw = report["paths"]["scanned"]
    require(isinstance(scanned_raw, list) and len(scanned_raw) <= 100_000)
    scanned = {relative_path(path) for path in scanned_raw}
    proof = SourceProof(source, private, files, scanned)
    output = []
    for finding in findings:
        require(isinstance(finding, dict))
        identity = rule_id(finding["check_id"])
        require(identity in rules)
        location = proof.location(finding, "path")
        require(location["path_in_scanned"])
        output.append({"rule_id": identity, "rule_sha256": rules[identity], **location})
    normalized_errors = [normalize_error(error, proof, rules) for error in errors]
    # Capability proof only. This labeled copy cannot accept findings or errors.
    capability = copy.deepcopy(report)
    capability["results"] = []
    capability["errors"] = []
    helper.validate_semgrep(capability, response, "sast")
    return {"revision": revision, "scanner": {"version": VERSION, "image": IMAGE},
            "scanner_exit_code": exit_code, "skipped_rule_count": 0,
            "purpose": "review-inventory-capture", "sast_gate_passed": False,
            "capability_proof_technique": "copy-with-results-and-errors-removed",
            "findings": output, "errors": normalized_errors}


def cleanup(helper: object, work: Path) -> None:
    cidfile = work / "container-id"
    if not cidfile.exists():
        return
    cid = cidfile.read_text().strip()
    require(re.fullmatch(r"[0-9a-f]{64}", cid) is not None)
    private = work / "cleanup"
    private.mkdir(mode=0o700)
    try:
        helper.run_private(["docker", "rm", "--force", cid], private,
                           {"PATH": os.defpath}, timeout=30)
    except helper.ScanFailure:
        pass
    remaining = helper.run_private(
        ["docker", "ps", "--all", "--no-trunc", "--filter", f"id={cid}", "--format", "{{.ID}}"],
        private, {"PATH": os.defpath}, timeout=30)
    require(remaining.strip() == "")


def capture(source: Path, private: Path, revision: str) -> dict:
    require(revision == TARGET)
    # Decide trust before importing any code into a token-bearing process.
    require(os.environ.get("GITHUB_ACTIONS") == "true"
            and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch"
            and bool(os.environ.get("GITHUB_ACTOR"))
            and os.environ["GITHUB_ACTOR"] != "dependabot[bot]"
            and bool(os.environ.get("GITHUB_REPOSITORY")))
    require(bool(os.environ.get("SEMGREP_APP_TOKEN")))
    files = tracked_files(source, private, revision)
    proof = SourceProof(source, private, files, set())
    sibling_path = "tools/ci/semgrep_inventory.py"
    # Block fallback imports from the working directory when the old reviewed
    # target has no sibling. New targets must supply a verified regular Git blob.
    sys.modules["semgrep_inventory"] = None
    if sibling_path in files:
        proof.file(sibling_path)
        sibling_spec = importlib.util.spec_from_file_location("semgrep_inventory", source / sibling_path)
        require(sibling_spec is not None and sibling_spec.loader is not None)
        sibling = importlib.util.module_from_spec(sibling_spec)
        sys.modules["semgrep_inventory"] = sibling
        sibling_spec.loader.exec_module(sibling)
    proof.file("tools/ci/security_scan.py")
    spec = importlib.util.spec_from_file_location("target_security_scan", source / "tools/ci/security_scan.py")
    require(spec is not None and spec.loader is not None)
    helper = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(helper)
    require(helper.SEMGREP_VERSION == VERSION and helper.SEMGREP_IMAGE == IMAGE)
    event = json.loads(Path(os.environ["GITHUB_EVENT_PATH"]).read_text())
    require(helper.eligible(os.environ, event) and bool(os.environ.get("SEMGREP_APP_TOKEN")))
    work = private / "scanner"
    work.mkdir(mode=0o700)
    (work / "home").mkdir(mode=0o700)
    args, env = helper.semgrep_command("sast", source, work, revision, os.environ)
    try:
        # Keep the actual exit code; run_private discards nonzero outcomes.
        with (work / "stdout").open("wb") as stdout, (work / "stderr").open("wb") as stderr:
            process = subprocess.run(args, env=env, stdout=stdout, stderr=stderr,
                                     timeout=900, check=False)
        require(process.returncode in (0, 1))
        report_path, config_path = work / "stdout", work / "scan-config.json"
        require(report_path.is_file() and report_path.stat().st_size <= 32_000_000)
        require(config_path.is_file() and config_path.stat().st_size <= 320_000_000)
        report = strict_json(report_path)
        response = strict_json(config_path)
        require(isinstance(report, dict) and isinstance(response, dict))
        return inventory(report, response, source, private, files, process.returncode, helper, revision)
    finally:
        cleanup(helper, work)


def main() -> int:
    os.umask(0o077)
    def terminate(signum: int, frame: object) -> None:
        raise CaptureFailure()

    signal.signal(signal.SIGTERM, terminate)
    try:
        require(os.environ.get("GITHUB_ACTIONS") == "true"
                and os.environ.get("GITHUB_EVENT_NAME") == "workflow_dispatch")
        require(len(sys.argv) == 1)
        revision = TARGET
        workspace = Path(os.environ["GITHUB_WORKSPACE"]).resolve(strict=True)
        source = workspace / "target"
        require(stat.S_ISDIR(source.lstat().st_mode))
        runner_temp = Path(os.environ["RUNNER_TEMP"]).resolve(strict=True)
        with tempfile.TemporaryDirectory(prefix="semgrep-capture-", dir=runner_temp) as temp:
            private = Path(temp)
            with (private / "private-output").open("w") as output, contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                metadata = capture(source, private, revision)
        # Only hand-built allowlisted metadata survives private-file cleanup.
        output_dir = runner_temp / "semgrep-inventory-output"
        output_dir.mkdir(mode=0o700)
        (output_dir / "inventory.json").write_bytes(canonical(metadata) + b"\n")
        print("Semgrep inventory metadata captured; findings 25; errors 3; skipped rules 0; not a passing SAST gate")
        return 0
    except (Exception, KeyboardInterrupt):
        print("Semgrep inventory capture failed; trust, execution, schema, or source proof unavailable")
        return 1


if __name__ == "__main__":
    sys.exit(main())
