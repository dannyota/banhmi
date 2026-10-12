"""Hosted synthetic controls for exact reviewed SAST enrollment."""

import contextlib
import copy
import hashlib
import io
import json
import os
import subprocess
import tempfile
import unittest
from pathlib import Path

import security_scan as scan
import semgrep_inventory as inventory
from test_security_scan import proof


class InventoryTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.source = Path(self.temp.name)
        self.git("init", "-q")
        self.git("config", "user.name", "Fixture")
        self.git("config", "user.email", "fixture@example.invalid")
        self.git("config", "commit.gpgsign", "false")
        (self.source / "main.go").write_text("package main\n// synthetic source\n")
        (self.source / "guard.txt").write_text("synthetic guard\n")
        self.report, self.response = proof("sast")
        self.position = {"line": 1, "col": 1, "offset": 0}
        self.finding = {"path": "main.go", "check_id": "test.go",
                        "start": self.position.copy(), "end": self.position.copy(),
                        "extra": {"message": "PRIVATE-CANARY", "lines": "PRIVATE-CANARY"}}
        self.entry = {"id": "fixture-finding", "path": "main.go",
                      "source_sha256": self.digest("main.go"), "supporting_sources": [],
                      "disposition": "false-positive", "rationale": "Synthetic self-contained fixture",
                      "evidence": ["docs/specs/security-ci.md"], "rule_id": "test.go",
                      "rule_sha256": inventory.rule_hash(self.response["config"]["rules"]["rules"][0]),
                      "start": self.position.copy(), "end": self.position.copy()}
        self.manifest = {"schema_version": 1, "semgrep_version": scan.SEMGREP_VERSION,
                         "semgrep_image": scan.SEMGREP_IMAGE, "findings": [], "partial_parsing": []}

    def git(self, *args):
        result = subprocess.run(["git", "-C", str(self.source), *args], check=True,
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                env={"PATH": os.defpath, "GIT_CONFIG_GLOBAL": os.devnull,
                                     "GIT_CONFIG_NOSYSTEM": "1"})
        return result.stdout

    def digest(self, path):
        return hashlib.sha256((self.source / path).read_bytes()).hexdigest()

    def commit(self, raw=None):
        path = self.source / inventory.INVENTORY_PATH
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(raw if raw is not None else json.dumps(self.manifest))
        self.git("add", "main.go", "guard.txt", inventory.INVENTORY_PATH)
        self.git("commit", "-qm", "Fixture")

    def classify(self):
        with contextlib.redirect_stdout(io.StringIO()) as output:
            result = inventory.classify_sast(self.report, self.response, self.source,
                                             scan.SEMGREP_VERSION, scan.SEMGREP_IMAGE)
        self.assertNotIn("PRIVATE-CANARY", output.getvalue())
        return result, output.getvalue()

    def enroll_finding(self):
        self.manifest["findings"] = [copy.deepcopy(self.entry)]
        self.report["results"] = [copy.deepcopy(self.finding)]

    def enroll_parser(self, rule_id=None):
        location = {"path": "/src/main.go", "start": {"line": 1, "col": 1, "offset": -1},
                    "end": {"line": 1, "col": 2}}
        self.report["errors"] = [{"type": ["PartialParsing", [location, copy.deepcopy(location)]],
                                  "code": 3, "level": "warn", "rule_id": rule_id,
                                  "path": "main.go", "message": "PRIVATE-CANARY"}]
        entry = {key: copy.deepcopy(value) for key, value in self.entry.items()
                 if key not in ("start", "end")}
        entry.update(id="fixture-parser", disposition="coverage-gap", code=3, level="warn",
                     rule_id=rule_id, rule_sha256=self.entry["rule_sha256"] if rule_id else None,
                     spans=None,
                     locations=[{"path": "main.go", "start": location["start"].copy(),
                                 "end": dict(location["end"], offset=None)} for _ in range(2)])
        self.manifest["partial_parsing"] = [entry]

    def test_exact_match_truthful_counts_and_empty_inventory(self):
        self.commit()
        counts, output = self.classify()
        self.assertEqual(counts["total_findings"], 0)
        self.enroll_finding()
        for disposition, field in (("false-positive", "accepted_false_positives"),
                                   ("fixed-but-audit", "accepted_audits"),
                                   ("trusted-operator-audit", "accepted_audits"),
                                   ("legacy-risk", "deferred_legacy_risks")):
            self.manifest["findings"][0]["disposition"] = disposition
            self.commit()
            counts, output = self.classify()
            self.assertEqual(counts["total_findings"], 1)
            self.assertEqual(counts[field], 1)
            self.assertIn("fixture-finding", output)
            self.assertIn("total findings 1", output)

    def test_duplicate_and_missing_records_report_unmatched_and_stale_counts(self):
        self.enroll_finding()
        self.commit()
        self.report["results"].append(copy.deepcopy(self.finding))
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(inventory.InventoryFailure):
            inventory.classify_sast(self.report, self.response, self.source,
                                    scan.SEMGREP_VERSION, scan.SEMGREP_IMAGE)
        self.assertIn("total findings 2; accepted false positives 1", output.getvalue())
        self.assertIn("unmatched records 1; stale entries 0", output.getvalue())
        self.assertEqual(output.getvalue().count("accepted inventory entry: fixture-finding"), 1)
        self.assertNotIn("PRIVATE-CANARY", output.getvalue())
        self.report["results"] = []
        output = io.StringIO()
        with contextlib.redirect_stdout(output), self.assertRaises(inventory.InventoryFailure):
            inventory.classify_sast(self.report, self.response, self.source,
                                    scan.SEMGREP_VERSION, scan.SEMGREP_IMAGE)
        self.assertIn("unmatched records 0; stale entries 1", output.getvalue())

    def test_rule_hash_uses_canonical_executable_rule_and_preserves_array_order(self):
        rule = {"pattern": "$X", "id": "x", "languages": ["go", "python"]}
        canonical = b'{"id":"x","languages":["go","python"],"pattern":"$X"}'
        self.assertEqual(inventory.rule_hash(rule), hashlib.sha256(canonical).hexdigest())
        self.assertEqual(inventory.rule_hash(rule), inventory.rule_hash(dict(reversed(list(rule.items())))))
        self.assertNotEqual(inventory.rule_hash(rule),
                            inventory.rule_hash(dict(rule, languages=["python", "go"])))
        with self.assertRaises(inventory.InventoryFailure):
            inventory.rule_hash(dict(rule, metadata=float("nan")))

    def test_top_level_metadata_changes_preserve_hash_and_accepted_finding(self):
        self.enroll_finding()
        self.commit()
        rule = self.response["config"]["rules"]["rules"][0]
        expected = self.entry["rule_sha256"]
        for metadata in ({"product": "sast", "scan_id": "first"},
                         {"product": "sast", "scan_id": "second", "nested": {"value": 42}}):
            rule["metadata"] = metadata
            original_rule = copy.deepcopy(rule)
            self.assertEqual(inventory.rule_hash(rule), expected)
            self.assertEqual(rule, original_rule)
            with contextlib.redirect_stdout(io.StringIO()):
                proof_counts, counts = scan.validate_sast_transition(self.report, self.response, self.source)
            self.assertEqual(proof_counts, (1, 1))
            self.assertEqual(counts["accepted_false_positives"], 1)
            self.assertEqual(counts["total_findings"], 1)

    def test_retained_rule_fields_change_hash_and_reject_prior_inventory(self):
        self.enroll_finding()
        self.commit()
        original = copy.deepcopy(self.response)
        changes = {"id": "changed.rule", "message": "Changed message", "severity": "ERROR",
                   "pattern": "$Y", "patterns": [{"pattern": "$Y"}],
                   "options": {"constant_propagation": False}, "languages": ["python"],
                   "paths": {"include": ["main.go"]}, "fix": "replacement",
                   "unknown": {"value": "retained"},
                   "nested": {"metadata": {"value": "retained"}}}
        for field, value in changes.items():
            with self.subTest(field=field):
                self.response = copy.deepcopy(original)
                rule = self.response["config"]["rules"]["rules"][0]
                rule[field] = value
                self.assertNotEqual(inventory.rule_hash(rule), self.entry["rule_sha256"])
                with self.assertRaises(inventory.InventoryFailure):
                    self.classify()

    def test_nested_metadata_change_invalidates_existing_executable_rule_binding(self):
        rule = self.response["config"]["rules"]["rules"][0]
        rule["options"] = {"metadata": {"value": "original"}}
        self.entry["rule_sha256"] = inventory.rule_hash(rule)
        self.enroll_finding()
        self.commit()
        counts, _ = self.classify()
        self.assertEqual(counts["accepted_false_positives"], 1)
        rule["options"]["metadata"]["value"] = "changed"
        self.assertNotEqual(inventory.rule_hash(rule), self.entry["rule_sha256"])
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_excluded_metadata_nonfinite_values_still_fail(self):
        self.enroll_finding()
        self.commit()
        rule = self.response["config"]["rules"]["rules"][0]
        for value in (float("nan"), float("inf"), float("-inf")):
            rule["metadata"] = {"nested": [{"value": value}]}
            with self.assertRaises(inventory.InventoryFailure):
                inventory.rule_hash(rule)
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()

    def test_hash_projection_cannot_override_original_product_and_scan_proof(self):
        self.enroll_finding()
        self.commit()
        original = copy.deepcopy(self.response)
        for mutate in (
                lambda c: c["config"]["rules"]["rules"][0].update(metadata={"product": "secrets"}),
                lambda c: c["config"]["rules"]["rules"][0].update(languages=["python"]),
                lambda c: c["info"].update(enabled_products=[]),
                lambda c: c["engine_params"].update(always_suppress_errors=True),
                lambda c: c["config"].update(project_merge_base="baseline")):
            self.response = copy.deepcopy(original)
            mutate(self.response)
            with contextlib.redirect_stdout(io.StringIO()), self.assertRaises(scan.ScanFailure):
                scan.validate_sast_transition(self.report, self.response, self.source)

    def test_parser_full_locations_multiplicity_and_nullable_rule(self):
        for rule_id in (None, "test.go"):
            self.enroll_parser(rule_id)
            self.commit()
            counts, _ = self.classify()
            self.assertEqual(counts["accepted_parser_gaps"], 1)
            original = copy.deepcopy(self.report)
            for mutate in (
                    lambda e: e.update(code=4), lambda e: e.update(level="error"),
                    lambda e: e["type"][1].pop(),
                    lambda e: e["type"][1].append(copy.deepcopy(e["type"][1][0])),
                    lambda e: e.update(path="guard.txt"),
                    lambda e: e.update(spans=[{}]),
                    lambda e: e.update(type="Syntax error"),
                    lambda e: e.update(rule_id="unknown.rule")):
                self.report = copy.deepcopy(original)
                mutate(self.report["errors"][0])
                with self.assertRaises(inventory.InventoryFailure):
                    self.classify()
            self.report = copy.deepcopy(original)
            self.report["errors"].append(copy.deepcopy(self.report["errors"][0]))
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()
            self.report = original

    def test_parser_spans_preserve_absence_empty_and_matching_locations(self):
        for span_kind in ("absent", "empty", "present"):
            self.enroll_parser()
            error = self.report["errors"][0]
            entry = self.manifest["partial_parsing"][0]
            if span_kind == "empty":
                error["spans"] = []
                entry["spans"] = []
            elif span_kind == "present":
                error["spans"] = [dict(file=location["path"], start=location["start"].copy(),
                                       end=location["end"].copy(), source_hash="PRIVATE-CANARY",
                                       config_start=None, context_end=None)
                                  for location in error["type"][1]]
                entry["spans"] = copy.deepcopy(entry["locations"])
            self.commit()
            counts, output = self.classify()
            self.assertEqual(counts["accepted_parser_gaps"], 1)
            self.assertNotIn("PRIVATE-CANARY", output)
            if span_kind == "absent":
                error["spans"] = []
            else:
                error.pop("spans")
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()

    def test_parser_spans_reject_drift_context_unknown_fields_and_null(self):
        self.enroll_parser()
        error = self.report["errors"][0]
        error["spans"] = [dict(file=loc["path"], start=loc["start"].copy(), end=loc["end"].copy())
                          for loc in error["type"][1]]
        self.manifest["partial_parsing"][0]["spans"] = copy.deepcopy(
            self.manifest["partial_parsing"][0]["locations"])
        self.commit()
        original = copy.deepcopy(self.report)
        for mutate in (
                lambda e: e.update(spans=None),
                lambda e: e["spans"].pop(),
                lambda e: e["spans"].append(copy.deepcopy(e["spans"][0])),
                lambda e: e["spans"][0].update(file="guard.txt"),
                lambda e: e["spans"][0]["start"].update(offset=0),
                lambda e: e["spans"][0].update(config_path="PRIVATE-CANARY"),
                lambda e: e["spans"][0].update(context_start={}),
                lambda e: e["spans"][0].update(unknown="PRIVATE-CANARY"),
                lambda e: e["spans"][0].update(source_hash=None)):
            self.report = copy.deepcopy(original)
            mutate(self.report["errors"][0])
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()

    def test_new_changed_duplicate_and_stale_report_records_fail(self):
        self.enroll_finding()
        self.commit()
        original = copy.deepcopy(self.report)
        for mutate in (
                lambda r: r["results"][0].update(path="guard.txt"),
                lambda r: r["results"][0].update(check_id="other.rule"),
                lambda r: r["results"][0]["start"].update(col=2),
                lambda r: r["results"][0]["end"].update(line=2),
                lambda r: r["results"][0]["end"].update(offset=1),
                lambda r: r["results"].append(copy.deepcopy(r["results"][0])),
                lambda r: r.update(results=[]),
                lambda r: r.update(errors=[{"type": "Syntax error", "message": "PRIVATE-CANARY"}])):
            self.report = copy.deepcopy(original)
            mutate(self.report)
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()

    def test_rule_body_missing_duplicate_and_null_pair_fail(self):
        self.enroll_parser("test.go")
        self.commit()
        original = copy.deepcopy(self.response)
        for rules in ([], [dict(original["config"]["rules"]["rules"][0], pattern="$Y")],
                      original["config"]["rules"]["rules"] * 2):
            self.response = copy.deepcopy(original)
            self.response["config"]["rules"]["rules"] = rules
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()
        self.response = original
        self.manifest["partial_parsing"][0]["rule_sha256"] = None
        self.commit()
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_source_and_supporting_bytes_are_bound_to_head_and_inventory(self):
        self.enroll_finding()
        self.manifest["findings"][0]["supporting_sources"] = [
            {"path": "guard.txt", "source_sha256": self.digest("guard.txt")}]
        self.commit()
        for path in ("main.go", "guard.txt"):
            original = (self.source / path).read_bytes()
            (self.source / path).write_bytes(original + b"changed outside match\n")
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()
            self.git("add", path)
            self.git("commit", "-qm", "Change source")
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()
            (self.source / path).write_bytes(original)
            self.git("add", path)
            self.git("commit", "-qm", "Restore source")
        (self.source / "main.go").unlink()
        (self.source / "main.go").symlink_to("guard.txt")
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_inventory_schema_pins_duplicates_and_self_binding_fail(self):
        self.enroll_finding()
        original = copy.deepcopy(self.manifest)
        for mutate in (
                lambda m: m.update(schema_version=True),
                lambda m: m.update(semgrep_version="other"),
                lambda m: m.update(semgrep_image="other"),
                lambda m: m.update(unknown=True),
                lambda m: m["findings"].append(copy.deepcopy(m["findings"][0])),
                lambda m: m["findings"][0].update(id="unsafe\nPRIVATE-CANARY"),
                lambda m: m["findings"].append(dict(m["findings"][0], id="different-id")),
                lambda m: m["findings"][0].update(path=inventory.INVENTORY_PATH),
                lambda m: m["findings"][0].update(supporting_sources=[
                    {"path": inventory.INVENTORY_PATH, "source_sha256": "0" * 64}]),
                lambda m: m["findings"][0].update(supporting_sources=[
                    {"path": "guard.txt", "source_sha256": self.digest("guard.txt")}]*2),
                lambda m: m["findings"][0].update(supporting_sources=[
                    {"path": "main.go", "source_sha256": self.digest("main.go")}]),
                lambda m: m["findings"][0]["start"].update(offset=1000000),
                lambda m: m["findings"][0]["start"].update(offset=-2),
                lambda m: m["findings"][0]["start"].update(line=True)):
            self.manifest = copy.deepcopy(original)
            mutate(self.manifest)
            self.commit()
            with self.assertRaises(inventory.InventoryFailure):
                self.classify()

    def test_json_rejects_duplicate_keys_and_nonfinite_values(self):
        for text in ('{"a":1,"a":2}', '{"a":NaN}', '{"a":Infinity}', '{"a":1e999}'):
            with self.assertRaises(ValueError):
                inventory.strict_json_loads(text)
        self.assertEqual(inventory.strict_json_loads('{"a":1}'), {"a": 1})

    def test_inventory_must_be_tracked_and_equal_working_bytes(self):
        self.commit()
        path = self.source / inventory.INVENTORY_PATH
        path.write_text("PRIVATE-CANARY")
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()
        self.git("rm", "-f", inventory.INVENTORY_PATH)
        self.git("commit", "-qm", "Remove inventory")
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(json.dumps(self.manifest))
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_deleted_renamed_untracked_and_parent_symlink_source_fail(self):
        self.enroll_finding()
        self.commit()
        path = self.source / "main.go"
        data = path.read_bytes()
        path.rename(self.source / "renamed.go")
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()
        self.git("rm", "main.go")
        self.git("commit", "-qm", "Delete source")
        path.write_bytes(data)
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()
        directory = self.source / "linked"
        directory.symlink_to(self.source, target_is_directory=True)
        self.manifest["findings"][0]["path"] = "linked/main.go"
        self.report["results"][0]["path"] = "linked/main.go"
        self.report["paths"]["scanned"] = ["linked/main.go"]
        self.commit()
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_parser_null_offsets_and_positions_are_not_lossily_collapsed(self):
        self.enroll_parser()
        self.commit()
        self.report["errors"][0]["type"][1][0]["start"]["offset"] = None
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_absent_offset_matches_inventory_null_but_explicit_report_null_fails(self):
        self.enroll_parser()
        self.commit()
        counts, _ = self.classify()
        self.assertEqual(counts["accepted_parser_gaps"], 1)
        # Both reported ends omit offset and match inventory offset null.
        for location in self.report["errors"][0]["type"][1]:
            location["end"]["offset"] = None
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()
        for location in self.report["errors"][0]["type"][1]:
            location["end"].pop("offset")
            # Inventory start offset -1 must not match an absent reported offset.
            location["start"].pop("offset")
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()

    def test_parser_location_order_is_normalized_without_losing_records(self):
        self.enroll_parser()
        self.manifest["partial_parsing"][0]["locations"][1]["end"]["col"] = 3
        self.report["errors"][0]["type"][1][1]["end"]["col"] = 3
        self.report["errors"][0]["type"][1].reverse()
        self.commit()
        counts, _ = self.classify()
        self.assertEqual(counts["accepted_parser_gaps"], 1)

    def test_scanned_source_proof_and_unknown_fields_fail(self):
        self.enroll_finding()
        self.commit()
        self.report["paths"]["scanned"] = []
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()
        self.report["paths"]["scanned"] = ["./main.go"]
        with self.assertRaises(inventory.InventoryFailure):
            self.classify()


if __name__ == "__main__":
    unittest.main()
