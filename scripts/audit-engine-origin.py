#!/usr/bin/env python3
"""Report the historical versus post-fork origin of engine Go files.

This is provenance evidence for the rewrite boundary, not a copyright or
license decision. A file that began in the fork can contain substantial new
work and must still be reviewed before its notice is changed.
"""

from __future__ import annotations

import pathlib
import re
import subprocess
import sys
from collections import defaultdict

FORK_COMMIT = "fb19612"


def layer_for(package: str) -> str:
    if package.startswith("internal/ports"):
        return "port collection"
    if package.startswith("internal/groups"):
        return "process attribution"
    if package.startswith("internal/scanner") or package.startswith("internal/daemon"):
        return "daemon scheduling"
    if package.startswith(("internal/store", "internal/runs", "internal/paths", "internal/config")):
        return "storage"
    if any(package.endswith(suffix) for suffix in ("_darwin", "_linux", "_windows")):
        return "platform adaptation"
    return "other engine"


def historical_commit(root: pathlib.Path, commit: str, cache: dict[str, bool]) -> bool:
    """Only Git's explicit non-ancestor result means post-fork; errors are unknown."""
    if not commit or commit == "0" * 40:
        raise RuntimeError("uncommitted or unknown origin; commit the audited engine changes first")
    if commit not in cache:
        result = subprocess.run(
            ["git", "merge-base", "--is-ancestor", commit, FORK_COMMIT],
            cwd=root, text=True, capture_output=True,
        )
        if result.returncode not in (0, 1):
            raise RuntimeError("cannot establish ancestry: " + result.stderr.strip())
        cache[commit] = result.returncode == 0
    return cache[commit]


def validate_history(root: pathlib.Path) -> None:
    """Source archives and shallow clones cannot establish historical origin."""
    shallow = subprocess.run(
        ["git", "rev-parse", "--is-shallow-repository"], cwd=root,
        check=True, text=True, capture_output=True,
    )
    if shallow.stdout.strip() != "false":
        raise RuntimeError("a complete Git history is required; unshallow the checkout first")
    result = subprocess.run(
        ["git", "merge-base", "--is-ancestor", FORK_COMMIT, "HEAD"],
        cwd=root, text=True, capture_output=True,
    )
    if result.returncode != 0:
        raise RuntimeError("the fork commit must exist in HEAD's history; a source archive is insufficient")
    dirty = subprocess.run(
        ["git", "status", "--porcelain", "--untracked-files=all", "--", "engine"],
        cwd=root, check=True, text=True, capture_output=True,
    )
    if dirty.stdout.strip():
        raise RuntimeError("engine changes must be committed before reporting source origin")


def first_commit(root: pathlib.Path, path: pathlib.Path) -> str:
    rel = path.relative_to(root)
    result = subprocess.run(
        ["git", "log", "--follow", "--diff-filter=A", "--format=%H", "--", str(rel)],
        cwd=root, check=True, text=True, capture_output=True,
    )
    lines = result.stdout.splitlines()
    if not lines:
        raise RuntimeError(f"no addition history for {rel}; origin is unknown")
    # Follow renames newest-first; reversing traversal can lose the creation.
    # Choose the oldest addition after Git has resolved the path history.
    return lines[-1]


def line_origins(root: pathlib.Path, path: pathlib.Path, commit_cache: dict[str, bool]) -> tuple[int, int]:
    """Count current lines whose blame commit is before/after the fork."""
    result = subprocess.run(
        ["git", "blame", "--line-porcelain", "--", str(path.relative_to(root))],
        cwd=root, check=True, text=True, capture_output=True,
    )
    historical = current = 0
    for line in result.stdout.splitlines():
        match = re.match(r"^([0-9a-f]{40}) \d+ \d+ (\d+)", line)
        if not match:
            continue
        commit = match.group(1)
        before = historical_commit(root, commit, commit_cache)
        lines = int(match.group(2))
        if before:
            historical += lines
        else:
            current += lines
    return historical, current


def main() -> int:
    root = pathlib.Path(__file__).resolve().parents[1]
    validate_history(root)
    rows: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
    layers: dict[str, dict[str, int]] = defaultdict(lambda: defaultdict(int))
    commit_cache: dict[str, bool] = {}
    for path in sorted((root / "engine").rglob("*.go")):
        commit = first_commit(root, path)
        origin = "historical fork" if historical_commit(root, commit, commit_cache) else "post-fork project"
        package = str(path.parent.relative_to(root / "engine"))
        layer = layer_for(package)
        rows[package][origin + " files"] += 1
        historical_lines, current_lines = line_origins(root, path, commit_cache)
        rows[package]["historical fork LOC"] += historical_lines
        rows[package]["post-fork project LOC"] += current_lines
        layers[layer][origin + " files"] += 1
        layers[layer]["historical fork LOC"] += historical_lines
        layers[layer]["post-fork project LOC"] += current_lines

    print("layer\thistorical fork files\thistorical fork LOC\tpost-fork project files\tpost-fork project LOC")
    for layer in sorted(layers):
        row = layers[layer]
        print(f"{layer}\t{row['historical fork files']}\t{row['historical fork LOC']}\t{row['post-fork project files']}\t{row['post-fork project LOC']}")
    print("package\thistorical fork files\thistorical fork LOC\tpost-fork project files\tpost-fork project LOC")
    for package in sorted(rows):
        row = rows[package]
        print(f"{package}\t{row['historical fork files']}\t{row['historical fork LOC']}\t{row['post-fork project files']}\t{row['post-fork project LOC']}")
    print("\ndirect Go dependencies")
    in_require = False
    for line in (root / "engine" / "go.mod").read_text().splitlines():
        stripped = line.strip()
        if stripped == "require (":
            in_require = True
            continue
        if in_require and stripped == ")":
            break
        if in_require and stripped and not stripped.startswith("//") and "// indirect" not in stripped:
            print(stripped)
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, subprocess.CalledProcessError) as error:
        print(f"audit-engine-origin: {error}", file=sys.stderr)
        raise SystemExit(2)
