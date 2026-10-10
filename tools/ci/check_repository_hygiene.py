#!/usr/bin/env python3
"""Check maintained Markdown for portable repository links and instruction files."""

from __future__ import annotations

import re
import sys
from pathlib import Path
from urllib.parse import unquote, urlsplit


ROOT = Path(__file__).resolve().parents[2]

REQUIRED_FILES = (
    "AGENTS.md",
    "CLAUDE.md",
    "instructions/workflow.md",
    "instructions/verification.md",
    "instructions/engineering.md",
    "instructions/corpus.md",
    "instructions/documentation.md",
    "instructions/security.md",
)

MARKDOWN_LINK = re.compile(r"!?\[[^\]\n]*\]\((?P<destination>[^)\n]+)\)")
ABSOLUTE_FILE_URI = re.compile(r"(?i)\bfile://(?:/|[A-Za-z]:[\\/])")
WORKSTATION_CHECKOUT = re.compile(
    r"(?P<path>/(?:home|Users)/(?P<user>[^/\s`'\"<>]+)/"
    r"(?:src|source|code|repos|repositories|projects|workspace|workspaces|"
    r"Desktop|Documents|Downloads|\.ssh)(?:/[^\s`'\"<>)]*)?)"
)
TILDE_CHECKOUT = re.compile(
    r"(?P<path>~/(?:src|source|code|repos|repositories|projects|workspace|"
    r"workspaces)(?:/[^\s`'\"<>)]*)?)"
)
PLACEHOLDER_USERS = {
    "app",
    "dev",
    "developer",
    "example",
    "runner",
    "root",
    "ubuntu",
    "user",
    "username",
    "you",
}


def maintained_markdown(root: Path) -> list[Path]:
    paths = set(root.glob("*.md"))
    paths.update((root / "docs").rglob("*.md"))
    paths.update((root / "instructions").glob("*.md"))
    paths.update((root / "deploy").rglob("*.md"))
    return sorted(path for path in paths if path.is_file())


def link_target(raw_destination: str) -> str:
    destination = raw_destination.strip()
    if destination.startswith("<"):
        closing = destination.find(">")
        return destination[1:closing] if closing >= 0 else destination
    return destination.split(maxsplit=1)[0]


def check_required_files(root: Path, errors: list[str]) -> None:
    for relative in REQUIRED_FILES:
        if not (root / relative).is_file():
            errors.append(f"missing required instruction file: {relative}")


def check_document(path: Path, root: Path, errors: list[str]) -> None:
    relative = path.relative_to(root)
    text = path.read_text(encoding="utf-8")

    for match in ABSOLUTE_FILE_URI.finditer(text):
        line = text.count("\n", 0, match.start()) + 1
        errors.append(f"{relative}:{line}: absolute file URI is not portable")

    for match in WORKSTATION_CHECKOUT.finditer(text):
        if match.group("user").lower() in PLACEHOLDER_USERS:
            continue
        line = text.count("\n", 0, match.start()) + 1
        errors.append(
            f"{relative}:{line}: workstation checkout path is not portable: "
            f"{match.group('path')}"
        )

    for match in TILDE_CHECKOUT.finditer(text):
        line = text.count("\n", 0, match.start()) + 1
        errors.append(
            f"{relative}:{line}: workstation checkout path is not portable: "
            f"{match.group('path')}"
        )

    for match in MARKDOWN_LINK.finditer(text):
        target = link_target(match.group("destination"))
        if not target or target.startswith("#"):
            continue

        line = text.count("\n", 0, match.start()) + 1
        if re.match(r"^[A-Za-z]:[\\/]", unquote(target)):
            errors.append(
                f"{relative}:{line}: local Markdown link must be repository-relative: "
                f"{target}"
            )
            continue

        parsed = urlsplit(target)
        if parsed.scheme.lower() == "file":
            errors.append(f"{relative}:{line}: Markdown file link is not portable: {target}")
            continue
        if parsed.scheme or parsed.netloc:
            continue

        decoded_path = unquote(parsed.path)
        if decoded_path.startswith(("/", "~/")):
            errors.append(
                f"{relative}:{line}: local Markdown link must be repository-relative: "
                f"{target}"
            )
            continue

        resolved = (path.parent / decoded_path).resolve()
        try:
            resolved.relative_to(root)
        except ValueError:
            errors.append(
                f"{relative}:{line}: local Markdown link leaves the repository: {target}"
            )
            continue

        if not resolved.exists():
            errors.append(f"{relative}:{line}: local Markdown link is missing: {target}")


def main() -> int:
    errors: list[str] = []
    check_required_files(ROOT, errors)

    documents = maintained_markdown(ROOT)
    for path in documents:
        check_document(path, ROOT, errors)

    if errors:
        print("Repository hygiene check failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1

    print(f"Repository hygiene check passed for {len(documents)} maintained Markdown files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
