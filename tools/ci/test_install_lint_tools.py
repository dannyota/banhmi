from __future__ import annotations

import os
import subprocess
import tempfile
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("install_lint_tools.sh")
MANIFEST = Path(__file__).with_name("lint-tools.sha256")


class InstallLintToolsTest(unittest.TestCase):
    def run_installer(self, manifest: str) -> tuple[subprocess.CompletedProcess, bool]:
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            (root / "tools/ci").mkdir(parents=True)
            (root / "tools/ci/lint-tools.sha256").write_text(manifest, encoding="utf-8")
            bin_dir = root / "bin"
            bin_dir.mkdir()
            marker = root / "download-attempted"
            # Stop before network access, extraction, installation, or binary execution.
            curl = bin_dir / "curl"
            curl.write_text('#!/bin/sh\n: > "$DOWNLOAD_MARKER"\nexit 73\n', encoding="utf-8")
            curl.chmod(0o755)
            result = subprocess.run(
                ["bash", str(SCRIPT)], cwd=root,
                env={**os.environ, "PATH": f"{bin_dir}:{os.environ['PATH']}",
                     "RUNNER_TEMP": str(root / "runner"), "DOWNLOAD_MARKER": str(marker)},
                capture_output=True, text=True, check=False,
            )
            return result, marker.exists()

    def test_complete_manifest_reaches_download(self) -> None:
        result, downloaded = self.run_installer(MANIFEST.read_text(encoding="utf-8"))
        self.assertEqual(result.returncode, 73)
        self.assertTrue(downloaded)

    def test_rejects_each_missing_checksum_before_download(self) -> None:
        lines = MANIFEST.read_text(encoding="utf-8").splitlines()
        records = [line for line in lines if line and not line.startswith("#")]
        self.assertEqual(len(records), 2)
        for removed in records:
            with self.subTest(archive=removed.split()[-1]):
                manifest = "\n".join(line for line in lines if line != removed) + "\n"
                result, downloaded = self.run_installer(manifest)
                self.assertEqual(result.returncode, 1)
                self.assertIn("Missing lint tool checksum", result.stderr)
                self.assertFalse(downloaded)

    def test_rejects_duplicate_extra_and_malformed_records(self) -> None:
        manifest = MANIFEST.read_text(encoding="utf-8")
        record = next(line for line in manifest.splitlines() if not line.startswith("#"))
        for extra in (record, "a" * 64 + "  unexpected.tar.gz", "invalid record"):
            with self.subTest(extra=extra):
                result, downloaded = self.run_installer(manifest + extra + "\n")
                self.assertEqual(result.returncode, 1)
                self.assertIn("lint tool checksum", result.stderr)
                self.assertFalse(downloaded)


if __name__ == "__main__":
    unittest.main()
