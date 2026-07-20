#!/usr/bin/env python3
"""Fail when required LANReady manuals, links, or Markdown anchors are missing."""

from __future__ import annotations

import re
import sys
import unicodedata
from pathlib import Path
from urllib.parse import unquote


ROOT = Path(__file__).resolve().parent.parent
REQUIRED_PATHS = (
    "README.md",
    "docs/README.md",
    "docs/installation.md",
    "docs/backup-restore.md",
    "docs/user-guide.md",
)
LINK = re.compile(r"\[[^\]]*\]\(([^)]+)\)")
HEADING = re.compile(r"^#{1,6}\s+(.+?)\s*#*\s*$")
EXPLICIT_ANCHOR = re.compile(r"<(?:a\s+name|[^>]+\sid)=[\"']([^\"']+)[\"']", re.IGNORECASE)


def github_slug(value: str) -> str:
    value = re.sub(r"`([^`]*)`", r"\1", value).strip().lower()
    value = re.sub(r"<[^>]+>", "", value)
    kept = "".join(
        char
        for char in value
        if char in "-_ " or char.isspace() or unicodedata.category(char)[0] in {"L", "N"}
    )
    return re.sub(r"\s", "-", kept)


def anchors_for(markdown: Path) -> set[str]:
    anchors: set[str] = set()
    occurrences: dict[str, int] = {}
    fenced = False
    for line in markdown.read_text(encoding="utf-8").splitlines():
        if line.lstrip().startswith(("```", "~~~")):
            fenced = not fenced
            continue
        if fenced:
            continue
        heading = HEADING.match(line)
        if heading:
            base = github_slug(heading.group(1))
            count = occurrences.get(base, 0)
            occurrences[base] = count + 1
            anchors.add(base if count == 0 else f"{base}-{count}")
        anchors.update(unquote(value).lower() for value in EXPLICIT_ANCHOR.findall(line))
    return anchors


def validate(root: Path, required_paths: tuple[str, ...] = REQUIRED_PATHS) -> list[str]:
    root = root.resolve()
    errors: list[str] = []
    for relative in required_paths:
        required = root / relative
        if not required.is_file():
            errors.append(f"required documentation is missing: {relative}")

    markdown_files = [root / "README.md", *sorted((root / "docs").rglob("*.md"))]
    markdown_files = [path for path in markdown_files if path.is_file()]
    anchor_cache: dict[Path, set[str]] = {}
    for markdown in markdown_files:
        fenced = False
        for line_number, line in enumerate(markdown.read_text(encoding="utf-8").splitlines(), 1):
            if line.lstrip().startswith(("```", "~~~")):
                fenced = not fenced
                continue
            if fenced:
                continue
            for match in LINK.finditer(line):
                raw = match.group(1).strip().strip("<>")
                target = raw.split(maxsplit=1)[0]
                if target.startswith(("http://", "https://", "mailto:")):
                    continue
                path_part, separator, fragment = target.partition("#")
                resolved = markdown if not path_part else (markdown.parent / unquote(path_part)).resolve()
                if not resolved.is_relative_to(root):
                    errors.append(
                        f"{markdown.relative_to(root)}:{line_number}: local link escapes repository: {target}"
                    )
                    continue
                if not resolved.exists():
                    errors.append(
                        f"{markdown.relative_to(root)}:{line_number}: missing local link target {target}"
                    )
                    continue
                if separator and fragment:
                    if not resolved.is_file() or resolved.suffix.lower() != ".md":
                        errors.append(
                            f"{markdown.relative_to(root)}:{line_number}: anchor target is not Markdown: {target}"
                        )
                        continue
                    anchors = anchor_cache.setdefault(resolved, anchors_for(resolved))
                    decoded_fragment = unquote(fragment).lower()
                    if decoded_fragment not in anchors:
                        errors.append(
                            f"{markdown.relative_to(root)}:{line_number}: missing Markdown anchor {target}"
                        )
    return errors


def main() -> int:
    errors = validate(ROOT)
    if errors:
        print("Documentation validation failed:", file=sys.stderr)
        for error in errors:
            print(f"- {error}", file=sys.stderr)
        return 1
    markdown_count = 1 + len(list((ROOT / "docs").rglob("*.md")))
    print(f"Documentation validation passed for {markdown_count} Markdown files.")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
