#!/usr/bin/env python3
"""Check the human-facing documentation pairs and their links.

Documents under `docs/` are written for people and are maintained in English
(`foo.md`) and Simplified Chinese (`foo.zh.md`). This check enforces the parts
of that contract a machine can judge: both sides exist, their Markdown
structure mirrors one to one, code blocks are byte-identical, each side carries
its language switcher, and every relative link resolves.

It cannot judge whether the two sides say the same thing, or whether the
wording is accurate. That stays a reviewer's job — see docs/AGENTS.md.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DOCS = ROOT / "docs"

# Documents that are deliberately English-only: agent instructions, this
# checker's own standard. Everything else under docs/ must be a complete pair.
EXEMPT = {
    "AGENTS.md",
}

SWITCHER_EN = "English | [中文]({stem}.zh.md)"
SWITCHER_ZH = "[English]({stem}.md) | 中文"

FENCE = re.compile(r"^(`{3,}|~{3,})")
HEADING = re.compile(r"^(#{1,6})\s+(.*)$")
LIST_ITEM = re.compile(r"^(\s*)([-*+]|\d+[.)])\s+")
TABLE_ROW = re.compile(r"^\s*\|")
LINK = re.compile(r"\]\(([^)\s]+)\)")


class Document:
    """The structural signature of one Markdown file.

    Only the features the pairing contract constrains are captured: headings,
    code blocks, list and table shapes, and semantic link targets.
    """

    def __init__(self, path: Path, locale: str) -> None:
        self.path = path
        self.locale = locale
        self.headings: list[tuple[int, str]] = []
        self.code_blocks: list[tuple[str, str]] = []
        self.list_kinds: list[str] = []
        self.table_shape: list[tuple[int, ...]] = []
        self.links: list[str] = []
        self.text = path.read_text(encoding="utf-8")
        self._parse()

    def _parse(self) -> None:
        lines = self.text.splitlines()
        in_code = False
        code_info = ""
        code_body: list[str] = []
        table_rows: list[int] | None = None

        def flush_table() -> None:
            nonlocal table_rows
            if table_rows is not None:
                # Row count plus every row's cell count: a table whose rows are
                # all the same length is not the same shape as one that is not.
                self.table_shape.append((len(table_rows), *table_rows))
                table_rows = None

        for line in lines:
            fence = FENCE.match(line)
            if fence:
                if in_code:
                    self.code_blocks.append((code_info, "\n".join(code_body)))
                    in_code = False
                    code_body = []
                    code_info = ""
                else:
                    flush_table()
                    in_code = True
                    code_info = line[len(fence.group(1)):].strip()
                continue
            if in_code:
                code_body.append(line)
                continue

            heading = HEADING.match(line)
            if heading:
                flush_table()
                self.headings.append((len(heading.group(1)), self._slug(heading.group(2))))
                continue

            if TABLE_ROW.match(line):
                cells = [c for c in line.strip().strip("|").split("|")]
                if table_rows is None:
                    table_rows = []
                table_rows.append(len(cells))
                continue
            flush_table()

            item = LIST_ITEM.match(line)
            if item:
                ordered = item.group(2).endswith(")") or item.group(2)[0].isdigit()
                self.list_kinds.append("ol" if ordered else "ul")

            for target in LINK.findall(line):
                if target.startswith(("http:", "https:", "mailto:", "#")):
                    continue
                self.links.append(target)
                continue

            for target in re.findall(r'(?:src|srcset)="([^"]+)"', line):
                if target.startswith(("http:", "https:", "mailto:", "#")):
                    continue
                self.links.append(target)

        if in_code:  # unterminated fence; keep it visible rather than dropping it
            self.code_blocks.append((code_info, "\n".join(code_body)))
        flush_table()

    @staticmethod
    def _slug(heading: str) -> str:
        """A locale-independent heading key.

        The headings themselves differ by language, so the signature compares
        depth and position; the slug is recorded only to make failures legible.
        """
        slug = re.sub(r"[`*_\[\]()]", "", heading).strip().lower()
        return re.sub(r"[^0-9a-z一-鿿]+", "-", slug).strip("-")


def paired_documents(docs: Path) -> list[Path]:
    """English-side documents under docs/ that must have a Chinese counterpart."""
    out = []
    for path in sorted(docs.glob("*.md")):
        if path.name.endswith(".zh.md") or path.name in EXEMPT:
            continue
        out.append(path)
    return out


def counterpart(path: Path) -> Path:
    return path.with_name(path.stem + ".zh.md")


def check_pair(english: Path) -> list[str]:
    """Every way a pair violates the contract, as human-readable lines."""
    problems: list[str] = []
    chinese = counterpart(english)
    if not chinese.is_file():
        return [f"{english.name}: missing Chinese counterpart {chinese.name}"]

    en = Document(english, "en")
    zh = Document(chinese, "zh")

    if len(en.headings) != len(zh.headings):
        problems.append(
            f"{english.stem}: heading count differs "
            f"(en={len(en.headings)}, zh={len(zh.headings)})"
        )
    else:
        for index, ((depth_en, slug_en), (depth_zh, slug_zh)) in enumerate(
            zip(en.headings, zh.headings), start=1
        ):
            if depth_en != depth_zh:
                problems.append(
                    f"{english.stem}: heading {index} depth differs "
                    f"(en h{depth_en} {slug_en!r}, zh h{depth_zh} {slug_zh!r})"
                )

    if en.code_blocks != zh.code_blocks:
        for index, (block_en, block_zh) in enumerate(
            zip(en.code_blocks, zh.code_blocks), start=1
        ):
            if block_en != block_zh:
                problems.append(
                    f"{english.stem}: code block {index} is not byte-identical "
                    f"on both sides"
                )
                break
        else:
            problems.append(
                f"{english.stem}: code block count differs "
                f"(en={len(en.code_blocks)}, zh={len(zh.code_blocks)})"
            )

    if en.list_kinds != zh.list_kinds:
        problems.append(
            f"{english.stem}: list structure differs "
            f"(en={len(en.list_kinds)} items, zh={len(zh.list_kinds)})"
        )
    if en.table_shape != zh.table_shape:
        problems.append(
            f"{english.stem}: table shape differs (en={en.table_shape}, "
            f"zh={zh.table_shape})"
        )

    if SWITCHER_EN.format(stem=english.stem) not in en.text:
        problems.append(
            f"{english.name}: missing language switcher "
            f"{SWITCHER_EN.format(stem=english.stem)!r}"
        )
    if SWITCHER_ZH.format(stem=english.stem) not in zh.text:
        problems.append(
            f"{chinese.name}: missing language switcher "
            f"{SWITCHER_ZH.format(stem=english.stem)!r}"
        )

    problems.extend(check_links(en, english.parent))
    problems.extend(check_links(zh, english.parent))
    return problems


def check_links(doc: Document, docs_root: Path) -> list[str]:
    """Relative link targets that do not resolve, including locale pairing.

    Both sides of a pair link to the same files; only the language of a link
    into the paired corpus differs. Everywhere else — `docs/agent/`, the repo
    root, another repository — both sides carry the identical target.
    """
    problems: list[str] = []
    docs_root = docs_root.resolve()
    stem = doc.path.name[: -len(".zh.md")] if doc.locale == "zh" else doc.path.stem
    # The language switcher points at the other locale on purpose, so its URL
    # is exempt from the "Chinese side links to .zh.md" rule.
    switcher_targets = {f"{stem}.zh.md", f"{stem}.md"}
    for target in doc.links:
        if target in switcher_targets:
            continue
        path_part = target.split("#")[0]
        if not path_part:
            continue
        resolved = (doc.path.parent / path_part).resolve()
        if not resolved.exists():
            problems.append(f"{doc.path.name}: broken link {target!r}")
            continue
        if resolved.parent != docs_root:
            continue
        if resolved.name.endswith(".zh.md") or resolved.name in EXEMPT:
            continue
        if not resolved.name.endswith(".md"):
            continue
        if doc.locale == "zh":
            problems.append(
                f"{doc.path.name}: link {target!r} should target the .zh.md counterpart"
            )
    return problems


def check_orphans(docs: Path) -> list[str]:
    """Chinese files whose English side is missing."""
    problems = []
    for path in sorted(docs.glob("*.zh.md")):
        english = path.with_name(path.name[: -len(".zh.md")] + ".md")
        if not english.is_file():
            problems.append(f"{path.name}: no English counterpart {english.name}")
    for path in sorted(docs.glob("*.md")):
        if path.name.endswith(".zh.md") and path.name[: -len(".zh.md")] + ".md" in EXEMPT:
            problems.append(f"{path.name}: {path.name} is an exempt document and must not be paired")
    return problems


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--docs", type=Path, default=DOCS)
    args = parser.parse_args(argv)

    documents = paired_documents(args.docs)
    if not documents:
        print(f"no paired documents found under {args.docs}", file=sys.stderr)
        return 1

    problems: list[str] = []
    for english in documents:
        problems.extend(check_pair(english))
    problems.extend(check_orphans(args.docs))

    if problems:
        print(f"documentation check failed: {len(problems)} problem(s)", file=sys.stderr)
        for problem in problems:
            print(f"  {problem}", file=sys.stderr)
        print(
            "Fix the pair; see docs/AGENTS.md for the contract.",
            file=sys.stderr,
        )
        return 1

    print(f"documentation: {len(documents)} pair(s) consistent under {args.docs}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
