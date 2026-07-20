from pathlib import Path
from tempfile import TemporaryDirectory
import unittest

from scripts.verify_docs import validate


class DocumentationValidationTest(unittest.TestCase):
    def test_reports_missing_file_and_anchor(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "docs").mkdir()
            (root / "README.md").write_text(
                "# Start\n\n[Fehlende Datei](docs/missing.md)\n[Fehlender Anker](#nicht-da)\n",
                encoding="utf-8",
            )
            errors = validate(root, required_paths=("README.md", "docs/required.md"))
            self.assertTrue(any("required documentation is missing" in error for error in errors))
            self.assertTrue(any("missing local link target" in error for error in errors))
            self.assertTrue(any("missing Markdown anchor" in error for error in errors))

    def test_accepts_existing_cross_file_and_duplicate_anchors(self) -> None:
        with TemporaryDirectory() as temporary:
            root = Path(temporary)
            (root / "docs").mkdir()
            (root / "README.md").write_text("# Start\n\n[Ziel](docs/target.md#titel-1)\n", encoding="utf-8")
            (root / "docs" / "target.md").write_text("# Titel\n\n# Titel\n", encoding="utf-8")
            self.assertEqual(validate(root, required_paths=("README.md", "docs/target.md")), [])

    def test_rejects_absolute_and_parent_escape(self) -> None:
        with TemporaryDirectory() as temporary:
            parent = Path(temporary)
            root = parent / "repository"
            (root / "docs").mkdir(parents=True)
            outside = parent / "outside.md"
            outside.write_text("# Outside\n", encoding="utf-8")
            (root / "README.md").write_text(
                f"# Start\n\n[Absolut]({outside})\n[Parent](../outside.md)\n",
                encoding="utf-8",
            )
            errors = validate(root, required_paths=("README.md",))
            self.assertEqual(sum("local link escapes repository" in error for error in errors), 2)


if __name__ == "__main__":
    unittest.main()
