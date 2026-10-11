from __future__ import annotations

import base64
import hashlib
import tempfile
import unittest
from pathlib import Path

import corpus_generation


class CorpusGenerationTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        for schema in corpus_generation.SCHEMAS:
            directory = self.root / "deploy" / "migrations" / schema
            directory.mkdir(parents=True)
            migration = directory / "00001_init.sql"
            migration.write_text("SELECT 1;\n", encoding="utf-8")
            digest = hashlib.sha256(migration.read_bytes()).hexdigest()
            (directory / "atlas.sum").write_text(
                f"h1:directory\n00001_init.sql h1:{digest}\n", encoding="utf-8"
            )
        self.baseline = corpus_generation.snapshot(self.root)

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def append_migration(self, schema: str = "bronze") -> None:
        directory = self.root / "deploy" / "migrations" / schema
        migration = directory / "00002_corpus_redesign.sql"
        migration.write_text("SELECT 2;\n", encoding="utf-8")
        old_entry = self.baseline["schemas"][schema]["checksum_entries"][0]
        (directory / "atlas.sum").write_text(
            f"h1:new-directory\n{old_entry}\n00002_corpus_redesign.sql h1:new\n",
            encoding="utf-8",
        )

    def write_inventory(
        self, path: Path, rows: list[tuple[str, str, str, str]]
    ) -> None:
        path.write_text(
            "".join(
                f"{kind}\t{schema}\t{key}\t"
                f"{base64.b64encode(definition.encode()).decode()}\n"
                for kind, schema, key, definition in rows
            ),
            encoding="utf-8",
        )

    def test_accepts_one_appended_migration_and_checksum_entry(self) -> None:
        self.append_migration()

        generated = corpus_generation.verify(self.root, self.baseline)

        self.assertEqual(
            generated, ["deploy/migrations/bronze/00002_corpus_redesign.sql"]
        )

    def test_rejects_changed_existing_migration(self) -> None:
        self.append_migration()
        migration = self.root / "deploy" / "migrations" / "bronze" / "00001_init.sql"
        migration.write_text("SELECT 9;\n", encoding="utf-8")

        with self.assertRaisesRegex(corpus_generation.GenerationError, "was changed"):
            corpus_generation.verify(self.root, self.baseline)

    def test_rejects_rewritten_checksum_history(self) -> None:
        self.append_migration()
        checksum = self.root / "deploy" / "migrations" / "bronze" / "atlas.sum"
        checksum.write_text(
            "h1:new-directory\n00001_init.sql h1:rewritten\n"
            "00002_corpus_redesign.sql h1:new\n",
            encoding="utf-8",
        )

        with self.assertRaisesRegex(corpus_generation.GenerationError, "history changed"):
            corpus_generation.verify(self.root, self.baseline)

    def test_rejects_multiple_new_migrations_in_one_schema(self) -> None:
        self.append_migration()
        directory = self.root / "deploy" / "migrations" / "bronze"
        (directory / "00003_second.sql").write_text("SELECT 3;\n", encoding="utf-8")

        with self.assertRaisesRegex(corpus_generation.GenerationError, "at most one"):
            corpus_generation.verify(self.root, self.baseline)

    def test_accepts_a_tree_that_is_already_in_sync(self) -> None:
        self.assertEqual(corpus_generation.verify(self.root, self.baseline), [])

    def test_appends_new_function_before_trigger_with_goose_boundaries(self) -> None:
        self.append_migration()
        manifest = self.root / "new-migrations.txt"
        manifest.write_text(
            "deploy/migrations/bronze/00002_corpus_redesign.sql\n", encoding="utf-8"
        )
        baseline = self.root / "opaque-baseline.tsv"
        canonical = self.root / "opaque-canonical.tsv"
        self.write_inventory(baseline, [])
        self.write_inventory(
            canonical,
            [
                (
                    "function",
                    "bronze",
                    "bronze.reject_change()",
                    "CREATE OR REPLACE FUNCTION bronze.reject_change() RETURNS trigger "
                    "LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$;",
                ),
                (
                    "trigger",
                    "bronze",
                    "bronze.source_record.reject_change",
                    "CREATE TRIGGER reject_change BEFORE UPDATE ON bronze.source_record "
                    "FOR EACH ROW EXECUTE FUNCTION bronze.reject_change()",
                ),
            ],
        )

        corpus_generation.append_opaque_delta(
            self.root, baseline, canonical, manifest
        )

        migration = (
            self.root
            / "deploy"
            / "migrations"
            / "bronze"
            / "00002_corpus_redesign.sql"
        ).read_text(encoding="utf-8")
        self.assertIn("-- +goose StatementBegin\nCREATE OR REPLACE FUNCTION", migration)
        self.assertIn("$$;\n-- +goose StatementEnd", migration)
        self.assertLess(migration.index("FUNCTION"), migration.index("CREATE TRIGGER"))
        self.assertIn("EXECUTE FUNCTION bronze.reject_change();", migration)

    def test_creates_append_only_migration_for_routine_only_delta(self) -> None:
        manifest = self.root / "new-migrations.txt"
        manifest.write_text("", encoding="utf-8")
        baseline = self.root / "opaque-baseline.tsv"
        canonical = self.root / "opaque-canonical.tsv"
        self.write_inventory(baseline, [])
        self.write_inventory(
            canonical,
            [
                (
                    "function",
                    "config",
                    "config.reject_change()",
                    "CREATE OR REPLACE FUNCTION config.reject_change() RETURNS trigger "
                    "LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$;",
                )
            ],
        )

        corpus_generation.append_opaque_delta(
            self.root, baseline, canonical, manifest
        )

        self.assertEqual(
            manifest.read_text(encoding="utf-8"),
            "deploy/migrations/config/00002_corpus_redesign.sql\n",
        )

    def test_rejects_changed_opaque_object(self) -> None:
        manifest = self.root / "new-migrations.txt"
        manifest.write_text("", encoding="utf-8")
        baseline = self.root / "opaque-baseline.tsv"
        canonical = self.root / "opaque-canonical.tsv"
        key = "bronze.reject_change()"
        self.write_inventory(
            baseline,
            [("function", "bronze", key, "CREATE FUNCTION bronze.reject_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN OLD; END; $$;")],
        )
        self.write_inventory(
            canonical,
            [("function", "bronze", key, "CREATE FUNCTION bronze.reject_change() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RETURN NEW; END; $$;")],
        )

        with self.assertRaisesRegex(
            corpus_generation.GenerationError, "change needs an explicit reviewed migration"
        ):
            corpus_generation.append_opaque_delta(
                self.root, baseline, canonical, manifest
            )

    def test_stage_copies_only_generated_stores_new_migrations_checksums_and_report(self) -> None:
        self.append_migration()
        manifest = self.root / "new-migrations.txt"
        manifest.write_text(
            "deploy/migrations/bronze/00002_corpus_redesign.sql\n", encoding="utf-8"
        )
        for schema in corpus_generation.SCHEMAS:
            store = self.root / "pkg" / "store" / schema
            store.mkdir(parents=True)
            (store / "models.go").write_text("package generated\n", encoding="utf-8")
        report = self.root / "report.md"
        report.write_text("# Verification\n", encoding="utf-8")
        output = self.root / "artifact"

        corpus_generation.stage(self.root, output, manifest, report)

        files = {
            path.relative_to(output).as_posix() for path in output.rglob("*") if path.is_file()
        }
        expected = {
            f"pkg/store/{schema}/models.go" for schema in corpus_generation.SCHEMAS
        }
        expected.update(
            f"deploy/migrations/{schema}/atlas.sum" for schema in corpus_generation.SCHEMAS
        )
        expected.update(
            {
                "deploy/migrations/bronze/00002_corpus_redesign.sql",
                "verification/report.md",
            }
        )
        self.assertEqual(files, expected)


if __name__ == "__main__":
    unittest.main()
