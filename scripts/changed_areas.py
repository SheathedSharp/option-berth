#!/usr/bin/env python3
"""Map changed paths to the CI areas they require.

One place decides which check runs for a change. `ci.yml` reads this script's
outputs; the workflow files carry no path filter of their own, because a
skipped required workflow never reports and blocks the pull request.
"""
from __future__ import annotations

import argparse
from pathlib import Path
import os
import subprocess
import sys

# A rule ending in "/" is a directory prefix; any other rule matches one path.
AREAS: dict[str, tuple[str, ...]] = {
    "engine": (
        "engine/",
        "magefiles/",
        "scripts/",
        "docs/cli.md",
        "docs/cli.zh.md",
        "install.sh",
        "install.ps1",
        "uninstall.sh",
        "uninstall.ps1",
    ),
    "windows": ("engine/", "install.ps1", "uninstall.ps1"),
    "git": (
        "engine/internal/git/",
        "engine/internal/groups/",
        "engine/internal/scanner/",
        "engine/go.mod",
        "engine/go.sum",
    ),
    "examples": ("examples/", "engine/", "scripts/smoke.py"),
    "readiness": ("engine/", "scripts/verify-reused-readiness.py"),
    "ci": (
        ".github/workflows/",
        ".github/quality-gate-checks.json",
        "scripts/check_ci_gate.py",
        "scripts/check_ci_gate_test.py",
        "scripts/changed_areas.py",
        "scripts/changed_areas_test.py",
        "scripts/race-packages.txt",
        "scripts/release.py",
    ),
}


def matches(path: str, rule: str) -> bool:
    return path.startswith(rule) if rule.endswith("/") else path == rule


def areas_for(paths: list[str]) -> dict[str, bool]:
    result = {name: any(matches(path, rule) for path in paths for rule in rules)
              for name, rules in AREAS.items()}
    if result["ci"]:
        # A pipeline change is proved by running the pipeline itself.
        result = dict.fromkeys(AREAS, True)
    return result


def changed_paths(base: str, head: str) -> list[str]:
    output = subprocess.run(
        ["git", "diff", "--name-only", "--no-renames", f"{base}...{head}"],
        text=True,
        stdout=subprocess.PIPE,
        check=True,
    ).stdout
    return [line for line in output.splitlines() if line]


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--base", help="diff from the merge base with this commit")
    parser.add_argument("--head", help="diff to this commit")
    parser.add_argument("--paths-file", type=Path, help="read changed paths from a file, one per line")
    parser.add_argument("--all", action="store_true", help="mark every area as needed")
    parser.add_argument("--output", type=Path, help="append the outputs here; defaults to $GITHUB_OUTPUT")
    args = parser.parse_args(argv)

    if args.all:
        result = dict.fromkeys(AREAS, True)
    elif args.paths_file:
        result = areas_for([line.strip() for line in args.paths_file.read_text().splitlines() if line.strip()])
    elif args.base and args.head:
        result = areas_for(changed_paths(args.base, args.head))
    else:
        parser.error("choose --all, --paths-file, or --base with --head")

    needed = [name for name, required in result.items() if required]
    print("needed areas: " + (", ".join(needed) or "none"))
    text = "".join(f"{name}={'true' if required else 'false'}\n" for name, required in result.items())
    target = args.output or os.environ.get("GITHUB_OUTPUT")
    if target:
        with open(target, "a", encoding="utf-8") as handle:
            handle.write(text)
    else:
        sys.stdout.write(text)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
