from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("check_results.py")


class CheckResultsTest(unittest.TestCase):
    def run_gate(
        self, needs: object, required: str = "first second", raw_json: str | None = None,
    ) -> tuple[int, str]:
        with tempfile.TemporaryDirectory() as directory:
            summary = Path(directory) / "summary.md"
            result = subprocess.run(
                [sys.executable, str(SCRIPT)],
                env={**os.environ, "NEEDS_JSON": json.dumps(needs) if raw_json is None else raw_json,
                     "REQUIRED_JOBS": required, "GITHUB_STEP_SUMMARY": str(summary)},
                capture_output=True, text=True, check=False,
            )
            return result.returncode, summary.read_text(encoding="utf-8")

    def test_accepts_only_all_success(self) -> None:
        code, summary = self.run_gate({
            "first": {"result": "success"}, "second": {"result": "success"},
        })
        self.assertEqual(code, 0)
        self.assertIn("| first | success |", summary)
        self.assertIn("| second | success |", summary)

    def test_rejects_each_non_success_result(self) -> None:
        for result in ("failure", "skipped", "cancelled", "unknown", None):
            with self.subTest(result=result):
                code, summary = self.run_gate({
                    "first": {"result": "success"}, "second": {"result": result},
                })
                self.assertEqual(code, 1)
                self.assertIn("failed", summary)
                if result is not None:
                    self.assertIn(f"| second | {result} |", summary)

    def test_rejects_missing_job_or_result(self) -> None:
        for needs in ({"first": {"result": "success"}},
                      {"first": {"result": "success"}, "second": {}}, {}):
            with self.subTest(needs=needs):
                code, summary = self.run_gate(needs)
                self.assertEqual(code, 1)
                self.assertIn("missing", summary)

    def test_rejects_empty_required_jobs(self) -> None:
        code, summary = self.run_gate({}, "")
        self.assertEqual(code, 1)
        self.assertIn("failed", summary)

    def test_rejects_malformed_needs(self) -> None:
        for needs in ([], None, {"first": "success"}):
            with self.subTest(needs=needs):
                code, summary = self.run_gate(needs)
                self.assertEqual(code, 1)
                self.assertIn("failed", summary)

    def test_rejects_invalid_json(self) -> None:
        for raw_json in ("", "{broken"):
            with self.subTest(raw_json=raw_json):
                code, summary = self.run_gate({}, raw_json=raw_json)
                self.assertEqual(code, 1)
                self.assertIn("Invalid needs JSON", summary)

    def test_reports_and_rejects_unexpected_child(self) -> None:
        code, summary = self.run_gate({
            "first": {"result": "success"}, "second": {"result": "success"},
            "extra": {"result": "failure"},
        })
        self.assertEqual(code, 1)
        self.assertIn("| extra | failure |", summary)


if __name__ == "__main__":
    unittest.main()
