from __future__ import annotations

import tempfile
import unittest
from pathlib import Path

import check_repository_hygiene as hygiene


class RepositoryHygieneTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temporary_directory = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary_directory.name)
        self.document = self.root / "docs/guide.md"
        self.document.parent.mkdir(parents=True)

    def tearDown(self) -> None:
        self.temporary_directory.cleanup()

    def errors_for(self, content: str) -> list[str]:
        self.document.write_text(content, encoding="utf-8")
        errors: list[str] = []
        hygiene.check_document(self.document, self.root, errors)
        return errors

    def test_accepts_relative_aws_cli_file_argument(self) -> None:
        errors = self.errors_for(
            "```bash\n"
            "aws ecs register-task-definition "
            "--cli-input-json file://PATH_TO_REVIEWED_TASK_DEFINITION.json\n"
            "```\n"
        )

        self.assertEqual(errors, [])

    def test_rejects_machine_file_markdown_link(self) -> None:
        errors = self.errors_for(
            "[local copy](file:///home/alice/src/project/README.md)\n"
        )

        self.assertTrue(any("absolute file URI" in error for error in errors))
        self.assertTrue(any("Markdown file link" in error for error in errors))
        self.assertTrue(any("workstation checkout path" in error for error in errors))

    def test_accepts_existing_repository_relative_link(self) -> None:
        (self.document.parent / "target.md").write_text("# Target\n", encoding="utf-8")

        errors = self.errors_for("[target](target.md#target)\n")

        self.assertEqual(errors, [])

    def test_rejects_missing_repository_relative_link(self) -> None:
        errors = self.errors_for("[missing](missing.md)\n")

        self.assertTrue(any("local Markdown link is missing" in error for error in errors))

    def test_rejects_windows_absolute_markdown_links(self) -> None:
        for target in (
            "C:/Users/alice/src/project/README.md",
            r"C:\Users\alice\src\project\README.md",
        ):
            with self.subTest(target=target):
                errors = self.errors_for(f"[local copy]({target})\n")

                self.assertTrue(
                    any("must be repository-relative" in error for error in errors)
                )


if __name__ == "__main__":
    unittest.main()
