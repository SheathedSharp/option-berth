#!/usr/bin/env python3
"""Rebuild main from c8556fc using --no-ff merges from merged PR heads.

Squash-merged PRs on main often embed old squash SHAs in their branch history.
This script cherry-picks only commits that are not already on the linear squash
line, then merges each topic with --no-ff. The result tree must match main HEAD
before replacing origin/main (force-with-lease).
"""

from __future__ import annotations

import argparse
import subprocess
import sys

BASE = "c8556fc"
MAIN = "main"

PR_SEQUENCE: list[tuple[int, str]] = [
    (98, "feat(git): add bounded graph facts contract (#94) (#98)"),
    (99, "feat(git): add historical commit diff contract (#94) (#99)"),
    (100, "feat(client): add graph history and commit review (#94) (#100)"),
    (101, "fix(client): unify native presentation chrome (#96) (#101)"),
    (102, "fix(client): terminal appearance and configuration editor chrome (#96) (#102)"),
    (103, "fix(client): theme native alert chrome (#96) (#103)"),
    (97, "feat(client): terminal-first session navigation (#95) (#97)"),
    (104, "fix(client): theme session launcher popover chrome (#104)"),
    (105, "chore(release): v0.8.0 session switcher, git graph, chrome (#105)"),
    (113, "fix(client): 修复 ⌘N 被系统新建命令截获 (#113)"),
    (112, "feat(client): 用 Xit 车道图替换自绘 Git 历史 (#112)"),
    (111, "feat(client): worktree 会话改为 Shell 优先，Agent 可脱离后重入 (#111)"),
    (110, "style(client): 收紧界面密度，侧栏、工具栏与列表面板更紧凑 (#110)"),
    (114, "chore(release): bump VERSION to 0.8.1 (#114)"),
    (115, "fix(client): history clear confirmation re-opens after Cancel (#115)"),
    (116, "chore(release): bump VERSION to 0.8.2 (#116)"),
]


def run(*args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(args, check=check, text=True, capture_output=True)


def cherry_pick(commit: str) -> None:
    result = subprocess.run(["git", "cherry-pick", commit])
    if result.returncode == 0:
        return
    status = run("git", "status").stdout
    if "nothing to commit" in status or "previous cherry-pick is now empty" in status:
        run("git", "cherry-pick", "--skip")
        return
    run("git", "cherry-pick", "--abort", check=False)
    raise SystemExit(f"cherry-pick failed: {commit}")


def fetch_pr_heads() -> None:
    for number, _ in PR_SEQUENCE:
        ref = f"refs/heads/pr-{number}"
        run(
            "git",
            "fetch",
            "origin",
            f"pull/{number}/head:{ref}",
            check=False,
        )


def rebuild(target_branch: str, dry_run: bool) -> None:
    squash_commits = set(run("git", "log", "--format=%H", f"{BASE}..{MAIN}").stdout.split())
    if not dry_run:
        run("git", "checkout", "-B", target_branch, BASE)

    for number, message in PR_SEQUENCE:
        pr = f"pr-{number}"
        print(f">>> PR {number}")
        cherry = run("git", "cherry", "HEAD", pr).stdout.splitlines()
        plus = [line.split()[1] for line in cherry if line.startswith("+")]
        plus = [commit for commit in plus if commit not in squash_commits]
        topic = f"topic-{number}"

        if not plus:
            if dry_run:
                print(f"    would merge {pr}")
                continue
            subprocess.run(["git", "merge", "--no-ff", "-m", message, pr], check=True)
            continue

        if dry_run:
            print(f"    would cherry-pick {len(plus)} commits then merge --no-ff")
            continue

        run("git", "branch", "-f", topic, "HEAD")
        run("git", "checkout", topic)
        for commit in plus:
            cherry_pick(commit)
        run("git", "checkout", target_branch)
        subprocess.run(["git", "merge", "--no-ff", "-m", message, topic], check=True)
        run("git", "branch", "-D", topic)

    if dry_run:
        return

    main_tree = run("git", "rev-parse", f"{MAIN}^{{tree}}").stdout.strip()
    rebuilt_tree = run("git", "rev-parse", "HEAD^{tree}").stdout.strip()
    if main_tree != rebuilt_tree:
        run("git", "diff", MAIN, "HEAD", "--stat", check=False)
        raise SystemExit(f"tree mismatch: main={main_tree} rebuilt={rebuilt_tree}")
    print(f"tree matches {MAIN}")


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--branch",
        default="main-reorg",
        help="branch to build (default: main-reorg)",
    )
    parser.add_argument("--fetch", action="store_true", help="fetch pull/*/head refs first")
    parser.add_argument("--dry-run", action="store_true", help="print planned merges only")
    args = parser.parse_args()
    if args.fetch:
        fetch_pr_heads()
    rebuild(args.branch, args.dry_run)


if __name__ == "__main__":
    main()
