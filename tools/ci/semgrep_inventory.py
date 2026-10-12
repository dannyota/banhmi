"""Exact source-bound SAST inventory; no raw scanner data is printed."""

from __future__ import annotations

import hashlib
import json
import math
import os
import re
import stat
import subprocess
from pathlib import Path


INVENTORY_PATH = "tools/ci/semgrep-legacy.json"
COMMON_FIELDS = {"id", "path", "source_sha256", "supporting_sources", "disposition",
                 "rationale", "evidence", "rule_id", "rule_sha256"}
FINDING_FIELDS = COMMON_FIELDS | {"start", "end"}
PARSER_FIELDS = COMMON_FIELDS | {"code", "level", "locations", "spans"}
DISPOSITIONS = {"false-positive", "fixed-but-audit", "trusted-operator-audit", "legacy-risk"}


class InventoryFailure(Exception):
    """Only fixed, non-scanner-controlled messages belong here."""


def strict_json_loads(text: str) -> object:
    def pairs(values):
        result = {}
        for key, value in values:
            if key in result:
                raise ValueError("duplicate JSON key")
            result[key] = value
        return result

    def number(value):
        result = float(value)
        if not math.isfinite(result):
            raise ValueError("nonfinite JSON number")
        return result

    def constant(_):
        raise ValueError("nonfinite JSON number")

    return json.loads(text, object_pairs_hook=pairs, parse_float=number, parse_constant=constant)


def rule_hash(rule: dict) -> str:
    if not isinstance(rule, dict):
        raise InventoryFailure("Semgrep rule hash proof malformed")
    try:
        # Validate all raw fields before excluding service-added metadata.
        json.dumps(rule, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False)
        executable = {key: value for key, value in rule.items() if key != "metadata"}
        encoded = json.dumps(executable, sort_keys=True, separators=(",", ":"),
                             ensure_ascii=True, allow_nan=False).encode("utf-8")
    except (TypeError, ValueError, UnicodeError):
        raise InventoryFailure("Semgrep rule hash proof malformed") from None
    return hashlib.sha256(encoded).hexdigest()


def path_value(value: object, report: bool = False) -> str:
    if not isinstance(value, str):
        raise InventoryFailure("Semgrep transition path malformed")
    if report:
        value = value.removeprefix("/src/")
    if (not re.fullmatch(r"[A-Za-z0-9_.\-/]{1,240}", value)
            or any(part in ("", ".", "..") for part in value.split("/"))):
        raise InventoryFailure("Semgrep transition path malformed")
    return value


def rule_id_value(value: object) -> str:
    if (not isinstance(value, str)
            or not re.fullmatch(r"[A-Za-z0-9][A-Za-z0-9_.-]{0,239}", value)
            or not all(value.split("."))):
        raise InventoryFailure("Semgrep transition rule ID malformed")
    return value


def digest_value(value: object) -> str:
    if not isinstance(value, str) or not re.fullmatch(r"[0-9a-f]{64}", value):
        raise InventoryFailure("Semgrep transition source or rule hash malformed")
    return value


class GitSource:
    """Prove regular mounted files equal regular tracked blobs at one fixed HEAD."""

    def __init__(self, root: Path):
        self.root = root.absolute()
        if self.root.resolve() != self.root:
            raise InventoryFailure("Semgrep transition source is not a regular tracked file")
        self.cache: dict[str, bytes] = {}
        self.revision = self.git("rev-parse", "--verify", "HEAD").decode("ascii").strip()
        if not re.fullmatch(r"[0-9a-f]{40}", self.revision):
            raise InventoryFailure("Semgrep transition source proof unavailable")

    def git(self, *args: str) -> bytes:
        try:
            result = subprocess.run(["git", "-C", str(self.root), *args],
                                    stdout=subprocess.PIPE, stderr=subprocess.PIPE, check=False,
                                    timeout=10, env={"PATH": os.defpath,
                                                     "GIT_CONFIG_GLOBAL": os.devnull,
                                                     "GIT_CONFIG_NOSYSTEM": "1"})
        except (OSError, subprocess.TimeoutExpired):
            raise InventoryFailure("Semgrep transition source proof unavailable") from None
        if result.returncode != 0:
            raise InventoryFailure("Semgrep transition source proof unavailable")
        return result.stdout

    def read(self, path: str) -> bytes:
        path = path_value(path)
        if path in self.cache:
            return self.cache[path]
        try:
            target = self.root
            for component in path.split("/"):
                target = target / component
                if target.is_symlink():
                    raise InventoryFailure("Semgrep transition source is not a regular tracked file")
            if not stat.S_ISREG(target.stat().st_mode):
                raise InventoryFailure("Semgrep transition source is not a regular tracked file")
            record = self.git("ls-tree", "-r", "-z", "--full-tree", self.revision, "--", path)
            rows = record.split(b"\0")
            if len(rows) != 2 or rows[1] != b"":
                raise InventoryFailure("Semgrep transition source is not a regular tracked file")
            metadata, git_path = rows[0].split(b"\t", 1)
            mode, kind, blob = metadata.split(b" ")
            if (mode not in (b"100644", b"100755") or kind != b"blob"
                    or git_path != path.encode("ascii")
                    or not re.fullmatch(rb"[0-9a-f]{40}", blob)):
                raise InventoryFailure("Semgrep transition source is not a regular tracked file")
            committed = self.git("cat-file", "blob", blob.decode("ascii"))
            mounted = target.read_bytes()
            if mounted != committed:
                raise InventoryFailure("Semgrep transition mounted source differs from Git blob")
        except (OSError, ValueError, UnicodeError):
            raise InventoryFailure("Semgrep transition source proof unavailable") from None
        self.cache[path] = mounted
        return mounted

    def bind(self, path: str, expected: str) -> bytes:
        if path == INVENTORY_PATH:
            raise InventoryFailure("Semgrep transition inventory cannot bind itself")
        data = self.read(path)
        if hashlib.sha256(data).hexdigest() != digest_value(expected):
            raise InventoryFailure("Semgrep transition source hash changed")
        return data


def position(value: object, size: int, report: bool = False) -> tuple:
    if not isinstance(value, dict):
        raise InventoryFailure("Semgrep transition position malformed")
    fields = set(value)
    if fields != {"line", "col", "offset"} and not (report and fields == {"line", "col"}):
        raise InventoryFailure("Semgrep transition position malformed")
    line, col, offset = value.get("line"), value.get("col"), value.get("offset")
    if (type(line) is not int or type(col) is not int
            or not 1 <= line <= 1_000_000 or not 1 <= col <= 1_000_000
            or (report and "offset" in value and type(offset) is not int)
            or (offset is not None and (type(offset) is not int or not -1 <= offset <= size))):
        raise InventoryFailure("Semgrep transition position malformed")
    return (line, col, offset)


def bounds(start: object, end: object, size: int, report: bool = False) -> tuple:
    first, last = position(start, size, report), position(end, size, report)
    if (first[:2] > last[:2]
            or (first[2] is not None and last[2] is not None
                and first[2] >= 0 and last[2] >= 0 and first[2] > last[2])):
        raise InventoryFailure("Semgrep transition position order malformed")
    return first, last


def locations(values: object, path: str, size: int, report: bool = False) -> tuple:
    if not isinstance(values, list) or not values:
        raise InventoryFailure("Semgrep transition parser locations malformed")
    result = []
    for value in values:
        if not isinstance(value, dict) or set(value) != {"path", "start", "end"}:
            raise InventoryFailure("Semgrep transition parser locations malformed")
        location_path = path_value(value["path"], report)
        if location_path != path:
            raise InventoryFailure("Semgrep transition parser locations disagree")
        result.append((location_path, *bounds(value["start"], value["end"], size, report)))
    # An explicit null-offset ordering preserves null and -1 as distinct values.
    def key(location):
        return (location[0], *(item for pos in location[1:] for item in
                               (pos[0], pos[1], (0, 0) if pos[2] is None else (1, pos[2]))))
    return tuple(sorted(result, key=key))


def configured_rules(response: dict) -> dict[str, str]:
    try:
        rules = response["config"]["rules"]["rules"]
        if not isinstance(rules, list) or not rules:
            raise InventoryFailure("Semgrep transition configured rules missing")
        result = {}
        for rule in rules:
            if not isinstance(rule, dict):
                raise InventoryFailure("Semgrep transition configured rules malformed")
            rule_id = rule_id_value(rule.get("id"))
            if rule_id in result:
                raise InventoryFailure("Semgrep transition configured rule IDs duplicated")
            result[rule_id] = rule_hash(rule)
        return result
    except (KeyError, TypeError):
        raise InventoryFailure("Semgrep transition configured rules malformed") from None


def parser_spans(value: object, path: str, size: int, report: bool = False) -> tuple | None:
    if value is None and not report:
        return None
    if not isinstance(value, list):
        raise InventoryFailure("Semgrep transition parser spans malformed")
    if not value:
        return ()
    normalized = []
    for span in value:
        if not isinstance(span, dict):
            raise InventoryFailure("Semgrep transition parser spans malformed")
        if report:
            required = {"file", "start", "end"}
            optional = {"source_hash", "config_start", "config_end", "config_path",
                        "context_start", "context_end"}
            if not required <= set(span) or set(span) - required - optional:
                raise InventoryFailure("Semgrep transition parser spans malformed")
            if (any(span.get(field) is not None for field in optional - {"source_hash"})
                    or ("source_hash" in span and not isinstance(span["source_hash"], str))):
                raise InventoryFailure("Semgrep transition parser span context unsupported")
            normalized.append({"path": span["file"], "start": span["start"], "end": span["end"]})
        else:
            normalized.append(span)
    return locations(normalized, path, size, report)


def rule_binding(rule_id: object, expected: object, rules: dict,
                 nullable: bool = False) -> tuple:
    if nullable and rule_id is None:
        if expected is not None:
            raise InventoryFailure("Semgrep transition parser rule binding malformed")
        return None, None
    rule_id = rule_id_value(rule_id)
    digest = digest_value(expected)
    if rules.get(rule_id) != digest:
        raise InventoryFailure("Semgrep transition configured rule binding changed")
    return rule_id, digest


def reviewed_evidence(value: object) -> bool:
    if not isinstance(value, str) or not value:
        return False
    relative = value
    if value.startswith("https://github.com/"):
        relative = value.removeprefix("https://github.com/")
        if len(relative.split("/")) < 3:
            return False
    path, _, anchor = relative.partition("#")
    return (bool(re.fullmatch(r"[A-Za-z0-9_.\-/]{1,500}", path))
            and all(part not in ("", ".", "..") for part in path.split("/"))
            and (not anchor or bool(re.fullmatch(r"[A-Za-z0-9_.-]+", anchor))))


def entry_identity(entry: dict, parser: bool, git: GitSource,
                   scanned: set[str], rules: dict) -> tuple:
    if not isinstance(entry, dict) or set(entry) != (PARSER_FIELDS if parser else FINDING_FIELDS):
        raise InventoryFailure("Semgrep transition inventory entry malformed")
    if (not isinstance(entry["id"], str) or not re.fullmatch(r"[a-z0-9-]{1,80}", entry["id"])
            or not isinstance(entry["rationale"], str) or not entry["rationale"].strip()
            or not isinstance(entry["evidence"], list) or not entry["evidence"]
            or not all(reviewed_evidence(value) for value in entry["evidence"])
            or entry["disposition"] not in ({"coverage-gap"} if parser else DISPOSITIONS)):
        raise InventoryFailure("Semgrep transition inventory review record malformed")
    path = path_value(entry["path"])
    data = git.bind(path, entry["source_sha256"])
    if path not in scanned:
        raise InventoryFailure("Semgrep transition enrolled source was not scanned")
    support = entry["supporting_sources"]
    if not isinstance(support, list):
        raise InventoryFailure("Semgrep transition supporting sources malformed")
    seen = {path}
    for binding in support:
        if not isinstance(binding, dict) or set(binding) != {"path", "source_sha256"}:
            raise InventoryFailure("Semgrep transition supporting sources malformed")
        supporting_path = path_value(binding["path"])
        if supporting_path in seen:
            raise InventoryFailure("Semgrep transition supporting source duplicated")
        seen.add(supporting_path)
        git.bind(supporting_path, binding["source_sha256"])
    rule_id, digest = rule_binding(entry["rule_id"], entry["rule_sha256"], rules, parser)
    if not parser:
        return (path, entry["source_sha256"], rule_id, digest,
                *bounds(entry["start"], entry["end"], len(data)))
    if type(entry["code"]) is not int or entry["level"] not in ("error", "warn", "info"):
        raise InventoryFailure("Semgrep transition parser identity malformed")
    locs = locations(entry["locations"], path, len(data))
    spans = parser_spans(entry["spans"], path, len(data))
    if spans and spans != locs:
        raise InventoryFailure("Semgrep transition parser spans disagree with locations")
    return (path, entry["source_sha256"], entry["code"], entry["level"], rule_id, digest, locs, spans)


def report_identity(record: object, parser: bool, git: GitSource,
                    scanned: set[str], rules: dict) -> tuple:
    if not isinstance(record, dict):
        raise InventoryFailure("Semgrep transition report record malformed")
    try:
        if parser:
            tag = record["type"]
            if (not isinstance(tag, list) or len(tag) != 2 or tag[0] != "PartialParsing"
                    or not isinstance(tag[1], list) or not tag[1]
                    or type(record["code"]) is not int or record["level"] not in ("error", "warn", "info")):
                raise InventoryFailure("Semgrep transition error is not an enrolled parser format")
            path = path_value(tag[1][0]["path"], True)
            if record.get("path") is not None and path_value(record["path"], True) != path:
                raise InventoryFailure("Semgrep transition parser paths disagree")
            rule_id = record.get("rule_id")
        else:
            path = path_value(record["path"], True)
            rule_id = record["check_id"]
        if path == INVENTORY_PATH or path not in scanned:
            raise InventoryFailure("Semgrep transition reported source proof missing")
        data = git.read(path)
        source_hash = hashlib.sha256(data).hexdigest()
        if parser and rule_id is None:
            digest = None
        else:
            rule_id = rule_id_value(rule_id)
            if rule_id not in rules:
                raise InventoryFailure("Semgrep transition reported rule proof missing")
            digest = rules[rule_id]
        if not parser:
            return (path, source_hash, rule_id, digest,
                    *bounds(record["start"], record["end"], len(data), True))
        locs = locations(tag[1], path, len(data), True)
        spans = parser_spans(record["spans"], path, len(data), True) if "spans" in record else None
        if spans and spans != locs:
            raise InventoryFailure("Semgrep transition parser spans disagree with locations")
        return (path, source_hash, record["code"], record["level"], rule_id, digest, locs, spans)
    except (KeyError, TypeError, IndexError):
        raise InventoryFailure("Semgrep transition report record malformed") from None


def classify_sast(report: dict, response: dict, source: Path,
                  version: str, image: str) -> dict[str, int]:
    """Classify after the caller proves full-scan settings and zero process exit."""
    try:
        git = GitSource(source)
        manifest = strict_json_loads(git.read(INVENTORY_PATH).decode("utf-8"))
        if (not isinstance(manifest, dict) or set(manifest) != {
                "schema_version", "semgrep_version", "semgrep_image", "findings", "partial_parsing"}
                or type(manifest["schema_version"]) is not int or manifest["schema_version"] != 1
                or manifest["semgrep_version"] != version or manifest["semgrep_image"] != image
                or not isinstance(manifest["findings"], list)
                or not isinstance(manifest["partial_parsing"], list)):
            raise InventoryFailure("Semgrep transition inventory schema or scanner pin malformed")
        scanned = {path_value(value, True) for value in report["paths"]["scanned"]}
        rules = configured_rules(response)
        enrolled = [{}, {}]
        ids = set()
        for parser, field in ((False, "findings"), (True, "partial_parsing")):
            for entry in manifest[field]:
                identity = entry_identity(entry, parser, git, scanned, rules)
                if entry["id"] in ids or identity in enrolled[parser]:
                    raise InventoryFailure("Semgrep transition inventory IDs or identities duplicated")
                ids.add(entry["id"])
                enrolled[parser][identity] = entry
        counts = {"total_findings": len(report["results"]), "accepted_false_positives": 0,
                  "accepted_audits": 0, "deferred_legacy_risks": 0, "accepted_parser_gaps": 0,
                  "unmatched_records": 0, "stale_entries": 0}
        accepted = []
        for parser, field in ((False, "results"), (True, "errors")):
            consumed = set()
            for record in report[field]:
                try:
                    identity = report_identity(record, parser, git, scanned, rules)
                except InventoryFailure:
                    counts["unmatched_records"] += 1
                    continue
                entry = enrolled[parser].get(identity)
                if entry is None or identity in consumed:
                    counts["unmatched_records"] += 1
                    continue
                consumed.add(identity)
                if parser:
                    counts["accepted_parser_gaps"] += 1
                elif entry["disposition"] == "false-positive":
                    counts["accepted_false_positives"] += 1
                elif entry["disposition"] == "legacy-risk":
                    counts["deferred_legacy_risks"] += 1
                else:
                    counts["accepted_audits"] += 1
                accepted.append(entry["id"])
            counts["stale_entries"] += len(enrolled[parser]) - len(consumed)
        print(f"Semgrep transition: total findings {counts['total_findings']}; "
              f"accepted false positives {counts['accepted_false_positives']}; "
              f"accepted audits {counts['accepted_audits']}; "
              f"deferred legacy risks {counts['deferred_legacy_risks']}; "
              f"accepted parser gaps {counts['accepted_parser_gaps']}; "
              f"unmatched records {counts['unmatched_records']}; stale entries {counts['stale_entries']}")
        for entry_id in accepted:
            print(f"Semgrep accepted inventory entry: {entry_id}")
        if counts["unmatched_records"] or counts["stale_entries"]:
            raise InventoryFailure("Semgrep transition has unmatched records or stale entries")
        return counts
    except (ValueError, TypeError, KeyError, UnicodeError, OverflowError, RecursionError):
        raise InventoryFailure("Semgrep transition inventory or report malformed") from None
