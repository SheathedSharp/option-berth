"""Check the documentation pair checker against fixtures and the live corpus."""
import importlib.util
from pathlib import Path
import tempfile
import unittest

ROOT = Path(__file__).resolve().parent.parent
SPEC = importlib.util.spec_from_file_location(
    "berth_check_docs", Path(__file__).with_name("check_docs.py")
)
docs_check = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(docs_check)

STUB = "brand/stub.svg"


def write(root: Path, name: str, text: str) -> Path:
    path = root / name
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(text, encoding="utf-8")
    return path


def pair(root: Path, stem: str, en: str, zh: str) -> None:
    write(root, f"docs/{stem}.md", en)
    write(root, f"docs/{stem}.zh.md", zh)


GOOD_EN = """# Title

English | [中文](demo.zh.md)

Intro paragraph.

## Usage

- one
- two

```sh
oberth status --json
```

| a | b |
|---|---|
| 1 | 2 |
"""

GOOD_ZH = """# 标题

[English](demo.md) | 中文

介绍段落。

## 用法

- 一
- 二

```sh
oberth status --json
```

| 甲 | 乙 |
|---|---|
| 1 | 2 |
"""


class PairTests(unittest.TestCase):
    def test_a_mirrored_pair_passes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH)
            self.assertEqual(docs_check.check_pair(root / "docs" / "demo.md"), [])

    def test_missing_counterpart_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write(root, "docs/demo.md", GOOD_EN)
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("missing Chinese counterpart" in p for p in problems))

    def test_heading_count_mismatch_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("## 用法", "## 用法\n\n### 额外"))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("heading count differs" in p for p in problems))

    def test_heading_depth_mismatch_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("## 用法", "### 用法"))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("depth differs" in p for p in problems))

    def test_code_block_drift_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("oberth status --json", "oberth up"))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("not byte-identical" in p for p in problems))

    def test_list_structure_drift_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("- 二\n", ""))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("list structure differs" in p for p in problems))

    def test_table_shape_drift_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("| 1 | 2 |\n", "| 1 | 2 | 3 |\n"))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("table shape differs" in p for p in problems))

    def test_english_switcher_is_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN.replace("English | [中文](demo.zh.md)\n", ""), GOOD_ZH)
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("missing language switcher" in p for p in problems))

    def test_chinese_switcher_is_required(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN, GOOD_ZH.replace("[English](demo.md) | 中文\n", ""))
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("missing language switcher" in p for p in problems))

    def test_orphan_chinese_file_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write(root, "docs/orphan.zh.md", GOOD_ZH)
            problems = docs_check.check_orphans(root / "docs")
            self.assertTrue(any("no English counterpart" in p for p in problems))


class LinkTests(unittest.TestCase):
    def test_broken_relative_link_is_reported(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN + "\nSee [gone](missing.md).\n", GOOD_ZH + "\n见 [没了](missing.md)。\n")
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("broken link" in p for p in problems))

    def test_existing_relative_link_passes(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write(root, "docs/target.md", "# Target\n")
            write(root, "docs/target.zh.md", "# 目标\n")
            pair(
                root,
                "demo",
                GOOD_EN + "\nSee [target](target.md).\n",
                GOOD_ZH + "\n见 [目标](target.zh.md)。\n",
            )
            self.assertEqual(docs_check.check_pair(root / "docs" / "demo.md"), [])

    def test_chinese_side_must_use_the_zh_counterpart(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write(root, "docs/target.md", "# Target\n")
            write(root, "docs/target.zh.md", "# 目标\n")
            pair(
                root,
                "demo",
                GOOD_EN + "\nSee [target](target.md).\n",
                GOOD_ZH + "\n见 [目标](target.md)。\n",
            )
            problems = docs_check.check_pair(root / "docs" / "demo.md")
            self.assertTrue(any("should target the .zh.md" in p for p in problems))

    def test_links_outside_docs_are_not_locale_checked(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write(root, "engine/UPSTREAM.md", "# Upstream\n")
            pair(
                root,
                "demo",
                GOOD_EN + "\nSee [upstream](../engine/UPSTREAM.md).\n",
                GOOD_ZH + "\n见 [上游](../engine/UPSTREAM.md)。\n",
            )
            self.assertEqual(docs_check.check_pair(root / "docs" / "demo.md"), [])

    def test_anchor_only_links_are_ignored(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            pair(root, "demo", GOOD_EN + "\nSee [above](#title).\n", GOOD_ZH + "\n见 [上面](#标题)。\n")
            self.assertEqual(docs_check.check_pair(root / "docs" / "demo.md"), [])


class LiveCorpusTests(unittest.TestCase):
    def test_every_paired_document_is_consistent(self):
        documents = docs_check.paired_documents(docs_check.DOCS)
        self.assertTrue(documents, "no paired documents found")
        problems = []
        for english in documents:
            problems.extend(docs_check.check_pair(english))
        problems.extend(docs_check.check_orphans(docs_check.DOCS))
        self.assertEqual(problems, [])

    def test_agent_documents_are_english_only(self):
        problems = docs_check.check_orphans(docs_check.DOCS)
        self.assertEqual(problems, [])
        for path in sorted((docs_check.DOCS / "agent").glob("*.md")):
            self.assertIsNone(
                docs_check.re.search(r"[一-鿿]", path.read_text(encoding="utf-8")),
                f"{path.name} must be English only",
            )


if __name__ == "__main__":
    unittest.main()
