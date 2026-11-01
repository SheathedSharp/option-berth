#!/usr/bin/env python3
"""Release gates and tag validation shared by CI and packaging.

Version bumps and tags are made through a reviewed PR followed by annotated
tags; the local commit/publish modes that used to live here are gone.
"""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import subprocess
import sys

ROOT = Path(__file__).resolve().parent.parent
VERSION_RE = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\Z")
PROTOCOL_FILE = Path("engine/internal/daemon/rpc/types.go")
SCHEMA_FILE = Path("engine/docs/schema/protocol.schema.json")
RACE_PACKAGES_FILE = Path("scripts/race-packages.txt")
PROTOCOL_RE = re.compile(r'(?m)^const ProtocolVersion = "([0-9]+\.[0-9]+\.[0-9]+)"$')


def run(root: Path, *args: str, capture: bool = True, environment: dict[str, str] | None = None) -> str:
    env = dict(os.environ, GOMAXPROCS="4")
    if environment:
        env.update(environment)
    result = subprocess.run(args, cwd=root, env=env, text=True,
                            stdout=subprocess.PIPE if capture else None,
                            stderr=subprocess.PIPE if capture else None, check=False)
    if result.returncode:
        # Do not print command arguments: a future caller might pass a credential.
        raise RuntimeError(f"{args[0]} failed (exit {result.returncode}): " + (result.stderr or "see verification output"))
    return (result.stdout or "").strip()


def protocol(root: Path) -> str:
    matches = PROTOCOL_RE.findall((root / PROTOCOL_FILE).read_text())
    if len(matches) != 1:
        raise ValueError("expected one daemon protocol version declaration")
    return matches[0]


def check_versions(root: Path) -> None:
    if not VERSION_RE.fullmatch((root / "VERSION").read_text().strip()):
        raise ValueError("invalid VERSION")
    if json.loads((root / SCHEMA_FILE).read_text())["protocol_version"] != protocol(root):
        raise ValueError("generated schema does not match daemon protocol version")


def race_packages(root: Path) -> tuple[str, ...]:
    """Read the race package list shared with the CI workflow."""
    lines = (root / RACE_PACKAGES_FILE).read_text().splitlines()
    packages = tuple(line.strip() for line in lines
                     if line.strip() and not line.lstrip().startswith("#"))
    if not packages:
        raise ValueError(f"{RACE_PACKAGES_FILE} has no packages")
    for package in packages:
        if not package.startswith("./") or any(char.isspace() for char in package):
            raise ValueError(f"race package entries must be single ./ paths: {package}")
    if len(packages) != len(set(packages)):
        raise ValueError(f"{RACE_PACKAGES_FILE} has duplicate entries")
    return packages


def verify(root: Path) -> None:
    """No skip-checks mode is exposed by the release gates."""
    check_versions(root)
    engine = root / "engine"
    for args in [("go", "build", "./..."), ("go", "vet", "./..."),
                 ("go", "test", "-count=1", "-timeout=8m", "./..."),
                 ("go", "test", "-race", "-count=1", "-timeout=10m", *race_packages(root)),
                 ("go", "test", "-tags", "integration", "-count=1", "-timeout=8m", "./internal/scenario/...")]:
        run(engine, *args, capture=False)
    run(root, "go", "test", "-count=1", "magefiles/build.go", "magefiles/workflow.go", "magefiles/workflow_test.go", capture=False)
    run(root, sys.executable, "-m", "unittest", "discover", "-s", "scripts", "-p", "*_test.py", capture=False)
    if (root.parent / "option-berth-macos" / "Sources" / "BerthGeometry.swift").is_file():
        run(root, sys.executable, "brand/build-option-berth.py", "--check", capture=False)
    else:
        # The 24x24 geometry is the macOS client's. Without a sibling checkout
        # there is nothing to compare against; say so instead of passing quietly.
        print("brand: skipped the terminal-mark drift check; BerthGeometry.swift lives in option-berth-macos")


def validate_tag(root: Path, tag: str) -> None:
    check_versions(root)
    if tag != "v" + (root / "VERSION").read_text().strip():
        raise ValueError("tag and VERSION do not match")
    if run(root, "git", "cat-file", "-t", "refs/tags/" + tag) != "tag":
        raise ValueError("release tag must be annotated")
    if run(root, "git", "rev-parse", "refs/tags/" + tag + "^{commit}") != run(root, "git", "rev-parse", "HEAD"):
        raise ValueError("checkout is not the tagged commit")
    module_tag = "engine/" + tag
    if run(root, "git", "cat-file", "-t", "refs/tags/" + module_tag) != "tag":
        raise ValueError("engine module tag must be annotated")
    if run(root, "git", "rev-parse", "refs/tags/" + module_tag + "^{commit}") != run(root, "git", "rev-parse", "HEAD"):
        raise ValueError("engine module tag does not point at the tagged commit")
    run(root, "git", "merge-base", "--is-ancestor", "HEAD", "origin/main")


def main() -> int:
    parser = argparse.ArgumentParser(description="Release gates and tag validation for CI.")
    parser.add_argument("--validate-tag", help="validate an existing annotated release tag")
    parser.add_argument("--verify-only", action="store_true", help="run release gates without changing refs")
    args = parser.parse_args()
    try:
        if args.validate_tag:
            validate_tag(ROOT, args.validate_tag)
            return 0
        if args.verify_only:
            verify(ROOT)
            return 0
        parser.error("choose --verify-only or --validate-tag; local release/publish modes were removed")
    except (OSError, RuntimeError, ValueError) as error:
        print(f"release refused: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
