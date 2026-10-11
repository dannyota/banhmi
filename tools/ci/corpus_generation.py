#!/usr/bin/env python3
"""Guard append-only corpus generation and stage its review artifact."""

from __future__ import annotations

import argparse
import base64
import binascii
import hashlib
import json
import re
import shutil
import sys
from pathlib import Path


SCHEMAS = ("bronze", "silver", "gold", "ingest", "config")
MIGRATION_NAME = re.compile(r"^[0-9]{5}_[a-z0-9_]+\.sql$")


class GenerationError(ValueError):
    """The generated tree violates the corpus generation contract."""


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as source:
        for block in iter(lambda: source.read(1024 * 1024), b""):
            digest.update(block)
    return digest.hexdigest()


def checksum_entries(path: Path) -> list[str]:
    lines = path.read_text(encoding="utf-8").splitlines()
    if not lines or not lines[0].startswith("h1:"):
        raise GenerationError(f"invalid Atlas checksum header: {path}")
    entries = [line for line in lines[1:] if line]
    for entry in entries:
        fields = entry.split(" ", 1)
        if len(fields) != 2 or not fields[1].startswith("h1:"):
            raise GenerationError(f"invalid Atlas checksum entry in {path}: {entry}")
    return entries


def migration_directory(root: Path, schema: str) -> Path:
    return root / "deploy" / "migrations" / schema


def snapshot(root: Path) -> dict[str, object]:
    schemas: dict[str, object] = {}
    for schema in SCHEMAS:
        directory = migration_directory(root, schema)
        sql_files = sorted(directory.glob("*.sql"))
        if not sql_files:
            raise GenerationError(f"no committed migrations for {schema}")
        schemas[schema] = {
            "files": {path.name: file_sha256(path) for path in sql_files},
            "checksum_entries": checksum_entries(directory / "atlas.sum"),
        }
    return {"version": 1, "schemas": schemas}


def verify(root: Path, baseline: dict[str, object]) -> list[str]:
    if baseline.get("version") != 1:
        raise GenerationError("unsupported corpus migration snapshot version")
    baseline_schemas = baseline.get("schemas")
    if not isinstance(baseline_schemas, dict):
        raise GenerationError("corpus migration snapshot has no schema map")

    generated: list[str] = []
    for schema in SCHEMAS:
        before = baseline_schemas.get(schema)
        if not isinstance(before, dict):
            raise GenerationError(f"snapshot is missing schema {schema}")
        before_files = before.get("files")
        before_entries = before.get("checksum_entries")
        if not isinstance(before_files, dict) or not isinstance(before_entries, list):
            raise GenerationError(f"invalid snapshot data for schema {schema}")

        directory = migration_directory(root, schema)
        current_files = {path.name: path for path in sorted(directory.glob("*.sql"))}
        for name, expected_hash in before_files.items():
            path = current_files.get(name)
            if path is None:
                raise GenerationError(f"existing migration was removed: {schema}/{name}")
            if file_sha256(path) != expected_hash:
                raise GenerationError(f"existing migration was changed: {schema}/{name}")

        new_names = sorted(set(current_files) - set(before_files))
        if len(new_names) > 1:
            raise GenerationError(f"expected at most one new migration for {schema}: {new_names}")
        if new_names:
            name = new_names[0]
            if not MIGRATION_NAME.fullmatch(name):
                raise GenerationError(f"invalid generated migration name: {schema}/{name}")
            if name <= max(before_files):
                raise GenerationError(f"generated migration does not append history: {schema}/{name}")
            generated.append(f"deploy/migrations/{schema}/{name}")

        current_entries = checksum_entries(directory / "atlas.sum")
        if current_entries[: len(before_entries)] != before_entries:
            raise GenerationError(f"Atlas checksum history changed for schema {schema}")
        expected_new_entries = [
            entry.split(" ", 1)[0] for entry in current_entries[len(before_entries) :]
        ]
        if expected_new_entries != new_names:
            raise GenerationError(
                f"Atlas checksum additions do not match new migrations for {schema}: "
                f"{expected_new_entries} != {new_names}"
            )

        allowed = set(current_files) | {"atlas.sum"}
        unexpected = sorted(
            path.name for path in directory.iterdir() if path.is_file() and path.name not in allowed
        )
        if unexpected:
            raise GenerationError(f"unexpected files in {directory}: {unexpected}")

    return generated

def stage(root: Path, output: Path, manifest: Path, report: Path) -> None:
    if output.exists():
        raise GenerationError(f"refusing to replace existing output directory: {output}")
    if not report.is_file():
        raise GenerationError(f"missing verification report: {report}")

    migration_paths = [line for line in manifest.read_text(encoding="utf-8").splitlines() if line]
    copied: list[Path] = []
    for schema in SCHEMAS:
        store = root / "pkg" / "store" / schema
        generated_go = sorted(store.glob("*.go"))
        if not generated_go:
            raise GenerationError(f"sqlc produced no Go files for {schema}")
        copied.extend(generated_go)
        copied.append(migration_directory(root, schema) / "atlas.sum")

    for relative in migration_paths:
        if not re.fullmatch(
            r"deploy/migrations/(bronze|silver|gold|ingest|config)/[0-9]{5}_[a-z0-9_]+\.sql",
            relative,
        ):
            raise GenerationError(f"migration manifest contains a forbidden path: {relative}")
        copied.append(root / relative)

    copied.append(report)
    output.mkdir(parents=True)
    artifact_hashes: list[str] = []
    for source in copied:
        if not source.is_file() or source.is_symlink():
            raise GenerationError(f"artifact source must be a regular file: {source}")
        if source == report:
            relative = Path("verification") / "report.md"
        else:
            relative = source.relative_to(root)
        destination = output / relative
        destination.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, destination)
        if source != report:
            artifact_hashes.append(f"{file_sha256(destination)}  {relative.as_posix()}")

    report_path = output / "verification" / "report.md"
    with report_path.open("a", encoding="utf-8") as destination:
        destination.write("\n## Generated source hashes\n\n```text\n")
        destination.write("\n".join(sorted(artifact_hashes)))
        destination.write("\n```\n")


def read_opaque_inventory(path: Path) -> dict[tuple[str, str, str], str]:
    inventory: dict[tuple[str, str, str], str] = {}
    for line_number, line in enumerate(path.read_text(encoding="utf-8").splitlines(), 1):
        if not line:
            continue
        fields = line.split("\t")
        if len(fields) != 4:
            raise GenerationError(f"invalid opaque inventory line {path}:{line_number}")
        kind, schema, key, encoded = fields
        if kind not in {"function", "trigger"} or schema not in SCHEMAS:
            raise GenerationError(f"unsupported opaque object at {path}:{line_number}")
        try:
            definition = base64.b64decode(encoded, validate=True).decode("utf-8")
        except (binascii.Error, ValueError, UnicodeDecodeError) as error:
            raise GenerationError(
                f"invalid opaque definition at {path}:{line_number}: {error}"
            ) from error
        object_key = (kind, schema, key)
        if object_key in inventory:
            raise GenerationError(f"duplicate opaque object in {path}: {object_key}")
        definition = definition.strip()
        if not definition.endswith(";"):
            definition += ";"
        inventory[object_key] = definition + "\n"
    return inventory


def migration_for_schema(root: Path, schema: str, paths: list[str]) -> Path:
    prefix = f"deploy/migrations/{schema}/"
    matches = [path for path in paths if path.startswith(prefix)]
    if len(matches) > 1:
        raise GenerationError(f"multiple generated migrations for schema {schema}: {matches}")
    if matches:
        return root / matches[0]

    directory = migration_directory(root, schema)
    existing = sorted(path.name for path in directory.glob("*.sql"))
    if not existing:
        raise GenerationError(f"cannot append opaque objects without migration history: {schema}")
    try:
        next_number = int(existing[-1].split("_", 1)[0]) + 1
    except ValueError as error:
        raise GenerationError(f"non-sequential migration history for {schema}") from error
    name = f"{next_number:05d}_corpus_redesign.sql"
    path = directory / name
    path.write_text("-- +goose Up\n", encoding="utf-8")
    paths.append(path.relative_to(root).as_posix())
    return path


def append_opaque_delta(
    root: Path, baseline_path: Path, canonical_path: Path, manifest_path: Path
) -> None:
    baseline = read_opaque_inventory(baseline_path)
    canonical = read_opaque_inventory(canonical_path)
    removed = sorted(set(baseline) - set(canonical))
    changed = sorted(
        key for key in set(baseline) & set(canonical) if baseline[key] != canonical[key]
    )
    if removed:
        raise GenerationError(f"opaque object removal needs an explicit reviewed migration: {removed}")
    if changed:
        raise GenerationError(f"opaque object change needs an explicit reviewed migration: {changed}")

    generated_paths = [
        line for line in manifest_path.read_text(encoding="utf-8").splitlines() if line
    ]
    additions = sorted(set(canonical) - set(baseline))
    for schema in SCHEMAS:
        schema_additions = [key for key in additions if key[1] == schema]
        if not schema_additions:
            continue
        migration = migration_for_schema(root, schema, generated_paths)
        functions = [key for key in schema_additions if key[0] == "function"]
        triggers = [key for key in schema_additions if key[0] == "trigger"]
        with migration.open("a", encoding="utf-8") as destination:
            destination.write("\n-- PostgreSQL routines and triggers omitted by Atlas Community.\n")
            for key in functions:
                destination.write("-- +goose StatementBegin\n")
                destination.write(canonical[key])
                destination.write("-- +goose StatementEnd\n")
            for key in triggers:
                destination.write(canonical[key])

    manifest_path.write_text(
        "" if not generated_paths else "\n".join(sorted(generated_paths)) + "\n",
        encoding="utf-8",
    )


def main() -> int:
    parser = argparse.ArgumentParser()
    subparsers = parser.add_subparsers(dest="command", required=True)

    snapshot_parser = subparsers.add_parser("snapshot")
    snapshot_parser.add_argument("--root", type=Path, default=Path.cwd())
    snapshot_parser.add_argument("--output", type=Path, required=True)

    verify_parser = subparsers.add_parser("verify")
    verify_parser.add_argument("--root", type=Path, default=Path.cwd())
    verify_parser.add_argument("--baseline", type=Path, required=True)
    verify_parser.add_argument("--manifest", type=Path, required=True)

    stage_parser = subparsers.add_parser("stage")
    stage_parser.add_argument("--root", type=Path, default=Path.cwd())
    stage_parser.add_argument("--output", type=Path, required=True)
    stage_parser.add_argument("--manifest", type=Path, required=True)
    stage_parser.add_argument("--report", type=Path, required=True)

    opaque_parser = subparsers.add_parser("append-opaque-delta")
    opaque_parser.add_argument("--root", type=Path, default=Path.cwd())
    opaque_parser.add_argument("--baseline", type=Path, required=True)
    opaque_parser.add_argument("--canonical", type=Path, required=True)
    opaque_parser.add_argument("--manifest", type=Path, required=True)

    args = parser.parse_args()
    try:
        root = args.root.resolve()
        if args.command == "snapshot":
            args.output.write_text(
                json.dumps(snapshot(root), indent=2, sort_keys=True) + "\n", encoding="utf-8"
            )
        elif args.command == "verify":
            baseline = json.loads(args.baseline.read_text(encoding="utf-8"))
            generated = verify(root, baseline)
            args.manifest.write_text(
                "" if not generated else "\n".join(generated) + "\n", encoding="utf-8"
            )
        elif args.command == "stage":
            stage(root, args.output, args.manifest, args.report.resolve())
        elif args.command == "append-opaque-delta":
            append_opaque_delta(root, args.baseline, args.canonical, args.manifest)
    except (GenerationError, OSError, json.JSONDecodeError) as error:
        print(error, file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
