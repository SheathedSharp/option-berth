#!/usr/bin/env python3
"""Render the Homebrew formula for one released engine tag.

The tap repository runs this against the latest release; keeping the template
and checksum parsing here means the formula cannot drift from the asset names
the release workflow actually publishes.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import re
import urllib.request

ASSETS = {
    "DARWIN_AMD64": "option-berth-{tag}-darwin-amd64.tar.gz",
    "DARWIN_ARM64": "option-berth-{tag}-darwin-arm64.tar.gz",
    "LINUX_AMD64": "option-berth-{tag}-linux-amd64.tar.gz",
    "LINUX_ARM64": "option-berth-{tag}-linux-arm64.tar.gz",
}
TAG_RE = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
BASE = "https://github.com/SheathedSharp/option-berth/releases/download"


def checksums(text: str) -> dict[str, str]:
    found = {}
    for line in text.splitlines():
        parts = line.split()
        if len(parts) == 2:
            found[parts[1]] = parts[0]
    return found


def render(template: str, tag: str, sums: dict[str, str]) -> str:
    if not TAG_RE.fullmatch(tag):
        raise ValueError(f"invalid release tag: {tag}")
    values = {"TAG": tag, "BASE": BASE}
    for key, name in ASSETS.items():
        asset = name.format(tag=tag)
        digest = sums.get(asset)
        if not digest or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValueError(f"missing or malformed checksum for {asset}")
        values[key] = digest
    missing = set(re.findall(r"\{\{([A-Z_]+)\}\}", template)) - set(values)
    if missing:
        raise ValueError("template placeholders without values: " + ", ".join(sorted(missing)))
    for key, value in values.items():
        template = template.replace("{{" + key + "}}", value)
    if "{{" in template:
        raise ValueError("template still contains placeholders")
    return template


def fetch_checksums(tag: str) -> str:
    url = f"{BASE}/{tag}/SHA256SUMS"
    request = urllib.request.Request(url, headers={"User-Agent": "option-berth-formula"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return response.read().decode("utf-8")


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--tag", required=True, help="release tag, e.g. v1.0.0")
    parser.add_argument("--template", type=Path, default=Path(__file__).resolve().parent.parent / "packaging" / "homebrew" / "option-berth.rb.in")
    parser.add_argument("--output", type=Path, help="write here instead of stdout")
    parser.add_argument("--checksums", type=Path, help="local SHA256SUMS fixture instead of downloading")
    args = parser.parse_args()
    text = args.checksums.read_text() if args.checksums else fetch_checksums(args.tag)
    formula = render(args.template.read_text(), args.tag, checksums(text))
    if args.output:
        args.output.write_text(formula)
    else:
        print(formula, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
