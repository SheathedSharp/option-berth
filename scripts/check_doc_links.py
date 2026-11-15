#!/usr/bin/env python3
"""Check local README/docs/source-notice links and GitHub-style Markdown fragments."""
import html
from pathlib import Path
import re
import unicodedata
from urllib.parse import unquote, urlsplit

from check_docs import Document

ROOT = Path(__file__).resolve().parents[1]
FENCE = re.compile(r"^ {0,3}(`{3,}|~{3,})")


def prose(text):
    fence = None
    for line in text.splitlines():
        match = FENCE.match(line)
        if match:
            token = match.group(1)
            if fence is None: fence = token
            elif token[0] == fence[0] and len(token) >= len(fence): fence = None
            continue
        if fence is None: yield line


def slug(text):
    text = re.sub(r"<[^>]+>", "", text)
    text = re.sub(r"\[([^]]+)\]\([^)]*\)", r"\1", text)
    text = html.unescape(text).strip().lower()
    return "".join("-" if char.isspace() else char for char in text if char.isspace() or char in "-_" or unicodedata.category(char)[0] in "LNM")


def anchors(path):
    found, used, previous = set(), set(), ""
    for line in prose(path.read_text()):
        found.update(html.unescape(x) for x in re.findall(r"(?:id|name)=[\"']([^\"']+)[\"']",line))
        match = re.match(r"^ {0,3}#{1,6} +(.+?) *#* *$",line)
        heading = match.group(1) if match else previous if previous and re.fullmatch(r" {0,3}(?:={3,}|-{3,}) *",line) else None
        if heading:
            base = slug(heading); value = base; number = 0
            while value in used:
                number += 1; value = base + "-" + str(number)
            used.add(value); found.add(value)
        previous = line.strip()
    return found


def check_file(path, root):
    errors = []
    for line in prose(path.read_text()):
        line = re.sub(r"`+[^`]*`+", "", line)
        targets = re.findall(r"\]\(<?([^\s)>]+)>?(?: +[\"'][^)]*)?\)",line)
        targets += re.findall(r"(?:href|src|srcset)=[\"']([^\"']+)[\"']",line)
        targets += re.findall(r"^ {0,3}\[[^]]+\]: +<?([^\s>]+)>?",line)
        for target in targets:
            parsed = urlsplit(html.unescape(target))
            if parsed.scheme or parsed.netloc: continue
            part = unquote(parsed.path)
            dest = (root / part.lstrip("/")) if part.startswith("/") else (path.parent / part) if part else path
            if not dest.exists():
                errors.append(f"{path.relative_to(root)}: missing target {target}")
            elif parsed.fragment and dest.suffix == ".md" and unquote(parsed.fragment) not in anchors(dest):
                errors.append(f"{path.relative_to(root)}: missing fragment {target}")
    return errors


def main():
    files = [ROOT/"README.md", ROOT/"README.zh.md", ROOT/"THIRD_PARTY_NOTICES.md", ROOT/"third_party/README.md", *sorted((ROOT/"docs").rglob("*.md"))]
    en = Document(ROOT/"README.md", "en"); zh = Document(ROOT/"README.zh.md", "zh")
    errors = []
    if [h[0] for h in en.headings] != [h[0] for h in zh.headings] or en.code_blocks != zh.code_blocks or en.list_kinds != zh.list_kinds or en.table_shape != zh.table_shape:
        errors.append("README bilingual structure or code blocks differ")
    if "[中文](README.zh.md)" not in en.text or "[English](README.md)" not in zh.text:
        errors.append("README bilingual navigation missing")
    for path in files:
        if not path.exists(): errors.append("missing documentation: " + str(path.relative_to(ROOT)))
        else: errors.extend(check_file(path,ROOT))
    if errors:
        print("Documentation links failed:\n" + "\n".join(errors)); return 1
    print(f"Documentation links/fragments: {len(files)} pages, README pair consistent (external links not fetched)")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
