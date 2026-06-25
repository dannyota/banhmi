#!/usr/bin/env python3
"""Convert one local file to Markdown with Microsoft MarkItDown.

The Go worker invokes this helper directly. It prints a small JSON object to
stdout and keeps errors intentionally terse so local paths do not leak into logs.
"""

from __future__ import annotations

import json
import os
import pathlib
import shutil
import subprocess
import sys
import tempfile
import unicodedata
from contextlib import contextmanager

os.environ.setdefault("ORT_LOG_SEVERITY_LEVEL", "3")


@contextmanager
def suppress_native_stderr():
    """Silence native-library stderr noise while keeping our own errors terse."""
    saved_fd = os.dup(2)
    try:
        with open(os.devnull, "w", encoding="utf-8") as devnull:
            os.dup2(devnull.fileno(), 2)
            yield
    finally:
        os.dup2(saved_fd, 2)
        os.close(saved_fd)


class LegacyDocConversionError(Exception):
    """Raised when a legacy OLE .doc cannot be rendered to PDF."""


def is_legacy_doc(path: str) -> bool:
    return pathlib.Path(path).suffix.lower() == ".doc"


@contextmanager
def markitdown_input(path: str):
    """Yield a path MarkItDown can read, rendering legacy .doc files to PDF."""
    if not is_legacy_doc(path):
        yield path
        return

    soffice = shutil.which("soffice") or shutil.which("libreoffice")
    if not soffice:
        raise LegacyDocConversionError("soffice not found")

    with tempfile.TemporaryDirectory(prefix="banhmi-doc-pdf-") as work:
        work_dir = pathlib.Path(work)
        out_dir = work_dir / "out"
        profile_dir = work_dir / "profile"
        out_dir.mkdir()
        profile_dir.mkdir()

        cmd = [
            soffice,
            "--headless",
            "--nologo",
            "--nodefault",
            "--nofirststartwizard",
            "--nolockcheck",
            f"-env:UserInstallation={profile_dir.resolve().as_uri()}",
            "--convert-to",
            "pdf:writer_pdf_Export",
            "--outdir",
            str(out_dir),
            path,
        ]
        try:
            proc = subprocess.run(  # noqa: S603 - command is fixed; path is a local file argument.
                cmd,
                stdout=subprocess.DEVNULL,
                stderr=subprocess.DEVNULL,
                timeout=120,
                check=False,
            )
        except subprocess.TimeoutExpired as exc:
            raise LegacyDocConversionError("timed out") from exc

        if proc.returncode != 0:
            raise LegacyDocConversionError(f"exit {proc.returncode}")

        pdfs = sorted(out_dir.glob("*.pdf"))
        if not pdfs:
            raise LegacyDocConversionError("no pdf output")
        yield str(pdfs[0])


def main() -> int:
    if len(sys.argv) != 2:
        print("usage: markitdown_convert.py <path>", file=sys.stderr)
        return 2

    try:
        with markitdown_input(sys.argv[1]) as input_path:
            with suppress_native_stderr():
                from markitdown import MarkItDown, StreamInfo

                converter = MarkItDown(enable_plugins=False)
                # vbpl serves charset-less HTML; markitdown's auto-detection
                # mis-guesses Vietnamese UTF-8 as cp1251/cp1252 and yields mojibake
                # (one doc sniffs Cyrillic, another Latin-1). The body is stored as
                # UTF-8, so force the charset for HTML and override the sniffer.
                if pathlib.Path(input_path).suffix.lower() in (".html", ".htm"):
                    result = converter.convert(
                        input_path, stream_info=StreamInfo(charset="utf-8")
                    )
                else:
                    result = converter.convert(input_path)
    except LegacyDocConversionError as exc:
        print(f"doc-to-pdf failed: {exc}", file=sys.stderr)
        return 1
    except Exception as exc:  # noqa: BLE001 - converter-specific failures vary.
        print(f"markitdown failed: {type(exc).__name__}", file=sys.stderr)
        return 1

    markdown = unicodedata.normalize("NFC", result.text_content or "")
    title = result.title
    if title:
        title = unicodedata.normalize("NFC", title)

    print(json.dumps({"markdown": markdown, "title": title}, ensure_ascii=False))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
