#!/usr/bin/env python3
"""Report how much of engine/ is still copied from the upstream Sonar checkout.

The engine began as a copy of `github.com/raskrebs/sonar`. The MIT notice in
`LICENSE` and `engine/LICENSE` covers what remains, so the notice can be
removed only when this report is empty. That is an engine rewrite, not a
rename: a rewritten file that keeps five consecutive upstream lines is still
copied code.

    git clone --depth 1 https://github.com/raskrebs/sonar /tmp/sonar
    python3 scripts/audit-upstream-origin.py --upstream /tmp/sonar

A *copied run* is a block of at least `--min-run` consecutive substantive lines
that also appears, in order, at the same relative path upstream. Blank lines,
brace-only lines and `package` / `import` clauses are ignored: they match in any
Go file and are not evidence of copying. The exit status is 1 while any copied
run remains.
"""
from __future__ import annotations

import argparse
import difflib
import json
import pathlib
import sys

DEFAULT_ENGINE = pathlib.Path(__file__).resolve().parent.parent / "engine"
IGNORED = {"", "}", "{", ")", "(", "};", "})", "return", "return nil"}


def substantive(line: str) -> bool:
    stripped = line.strip()
    if stripped in IGNORED:
        return False
    return not stripped.startswith(("package ", "import "))


def copied_runs(upstream: str, candidate: str, min_run: int) -> list[tuple[int, int, int]]:
    """Return matching (upstream index, candidate index, length) runs at or above min_run."""
    left = [line.strip() for line in upstream.splitlines() if substantive(line)]
    right = [line.strip() for line in candidate.splitlines() if substantive(line)]
    matcher = difflib.SequenceMatcher(None, left, right, autojunk=False)
    return [(block.a, block.b, block.size) for block in matcher.get_matching_blocks()
            if block.size >= min_run]


def audit(engine: pathlib.Path, upstream: pathlib.Path, min_run: int = 5) -> dict:
    if not engine.is_dir():
        raise SystemExit(f"audit-upstream-origin: no engine tree at {engine}")
    if not upstream.is_dir():
        raise SystemExit(f"audit-upstream-origin: no upstream checkout at {upstream}")
    files = []
    for path in sorted(engine.rglob("*.go")):
        relative = path.relative_to(engine)
        other = upstream / relative
        if not other.is_file():
            continue
        runs = copied_runs(other.read_text(errors="replace"), path.read_text(errors="replace"), min_run)
        if not runs:
            continue
        files.append({
            "path": str(relative),
            "runs": len(runs),
            "lines": sum(size for _, _, size in runs),
            "largest": max(size for _, _, size in runs),
        })
    files.sort(key=lambda item: (-item["lines"], item["path"]))
    return {
        "engine": str(engine),
        "upstream": str(upstream),
        "min_run": min_run,
        "files": files,
        "runs": sum(item["runs"] for item in files),
        "lines": sum(item["lines"] for item in files),
    }


def report(result: dict, limit: int) -> str:
    lines = [f"copied files: {len(result['files'])}, runs: {result['runs']}, "
             f"lines: {result['lines']} (min run {result['min_run']})"]
    for item in result["files"][:limit]:
        lines.append(f"  {item['lines']:5d} lines / {item['runs']:3d} runs / "
                     f"largest {item['largest']:3d}  {item['path']}")
    if len(result["files"]) > limit:
        lines.append(f"  ... {len(result['files']) - limit} more files")
    return "\n".join(lines)


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description="Audit upstream-copied code in engine/.")
    parser.add_argument("--engine", type=pathlib.Path, default=DEFAULT_ENGINE)
    parser.add_argument("--upstream", type=pathlib.Path, required=True,
                        help="checkout of github.com/raskrebs/sonar")
    parser.add_argument("--min-run", type=int, default=5,
                        help="consecutive substantive lines that count as a copied run")
    parser.add_argument("--json", action="store_true")
    parser.add_argument("--limit", type=int, default=20, help="files to print in text mode")
    args = parser.parse_args(argv)
    if args.min_run < 1:
        parser.error("--min-run must be at least 1")
    result = audit(args.engine, args.upstream, args.min_run)
    if args.json:
        print(json.dumps(result, indent=2, ensure_ascii=False))
    else:
        print(report(result, args.limit))
    return 1 if result["lines"] else 0


if __name__ == "__main__":
    sys.exit(main())
