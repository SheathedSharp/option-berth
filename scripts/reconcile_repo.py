#!/usr/bin/env python3
"""Reconcile the quality gate, open pull requests and failed runs, read-only.

`check_ci_gate.py` proves the three-way agreement between the required-check
manifest, the workflow job names and the live ruleset, but only inside CI.
This script turns the same facts into a standing report and escalates a red
`Nightly full verification` / `Verified release` run into exactly one open
issue.

What it reads, always through `gh`: `.github/quality-gate-checks.json`,
`.github/workflows/*.yml`, the live branch ruleset, open pull requests, their
checks and their age.
What it writes: one issue body plus comments on that same issue. It never
touches rulesets, branch protection, labels or pull request branches.
"""
from __future__ import annotations

import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import subprocess
import sys
from typing import Callable, NamedTuple, Sequence

sys.path.insert(0, str(Path(__file__).resolve().parent))
import check_ci_gate  # noqa: E402  (same directory; reused for its pure parsers)

DEFAULT_REPO = "SheathedSharp/option-berth"
DEFAULT_MANIFEST = Path(".github/quality-gate-checks.json")
DEFAULT_WORKFLOWS = Path(".github/workflows")
DEFAULT_LIMIT_HOURS = 24.0
SUMMARY_PREFIX = "reconcile-summary: "
ESCALATION_PREFIX = "reconcile-escalation: "
DRIFT_TITLE = "[automation] 质量门对账发现漂移"
DEPENDABOT_LOGINS = frozenset({"dependabot[bot]", "app/dependabot", "dependabot"})
DRIFT_LABELS = (
    ("manifest 缺少对应 workflow job", "missing_workflows"),
    ("ruleset 未要求", "missing_ruleset"),
    ("ruleset 多出", "extra_ruleset"),
)

ESCALATE = "escalate"
PASSED = "passed"
IGNORED = "ignored"


class Drift(NamedTuple):
    """The three lists that disagree; every one of them is reported, not just the first."""

    missing_workflows: tuple[str, ...] = ()
    missing_ruleset: tuple[str, ...] = ()
    extra_ruleset: tuple[str, ...] = ()

    @property
    def drifted(self) -> bool:
        return bool(self.missing_workflows or self.missing_ruleset or self.extra_ruleset)


class Failure(NamedTuple):
    workflow: str
    conclusion: str
    run_url: str
    failed_jobs: tuple[str, ...] = ()


class PullRequest(NamedTuple):
    number: int
    title: str
    author: str
    head_ref: str
    merge_state: str
    is_draft: bool
    age_hours: float
    # None means the checks could not be read; an empty tuple means GitHub
    # reported no checks at all.
    reported_checks: tuple[str, ...] | None


class BehindPR(NamedTuple):
    number: int
    title: str
    author: str
    # Only a Dependabot branch may be advanced by automation; a human branch is
    # a recommendation, never an action.
    automated: bool
    command: str


class StalePR(NamedTuple):
    number: int
    title: str
    author: str
    missing: tuple[str, ...]


class CommandError(RuntimeError):
    def __init__(self, argv: Sequence[str], stderr: str, stdout: str = ""):
        super().__init__(f"gh {' '.join(argv)} failed: {stderr.strip() or 'no stderr'}")
        self.stdout = stdout


def run_gh(argv: Sequence[str], allow_failure: bool = False) -> str:
    result = subprocess.run(["gh", *argv], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
    if result.returncode != 0 and not allow_failure:
        raise CommandError(argv, result.stderr, result.stdout)
    return result.stdout


class GitHub:
    """The only place that talks to `gh`; `run` is injectable so tests stay offline."""

    def __init__(self, repo: str, *, run: Callable[..., str] = run_gh):
        self.repo = repo
        self._run = run

    def _json(self, argv: Sequence[str], allow_failure: bool = False):
        text = self._run(list(argv), allow_failure=allow_failure)
        if not text.strip():
            return None
        try:
            return json.loads(text)
        except json.JSONDecodeError:
            return None

    def rulesets(self) -> list[dict]:
        listed = self._json(["api", f"repos/{self.repo}/rulesets"]) or []
        return [self._json(["api", f"repos/{self.repo}/rulesets/{item['id']}"]) or {} for item in listed]

    def open_issue_number(self, title: str) -> int | None:
        issues = self._json(
            ["issue", "list", "--repo", self.repo, "--state", "open", "--json", "number,title", "--limit", "200"]
        ) or []
        for issue in issues:
            if issue.get("title") == title:
                return int(issue["number"])
        return None

    def issue_body(self, number: int) -> str:
        data = self._json(["issue", "view", str(number), "--repo", self.repo, "--json", "body"]) or {}
        return str(data.get("body") or "")

    def create_issue(self, title: str, body: str, *, dry_run: bool) -> None:
        self._write(["issue", "create", "--repo", self.repo, "--title", title, "--body", body],
                    dry_run, f"创建 issue：{title}", body)

    def comment(self, number: int, body: str, *, dry_run: bool) -> None:
        self._write(["issue", "comment", str(number), "--repo", self.repo, "--body", body],
                    dry_run, f"评论 issue #{number}", body)

    def _write(self, argv: Sequence[str], dry_run: bool, note: str, body: str) -> None:
        if dry_run:
            print(f"[dry-run] 将{note}")
            print("  " + body.replace("\n", "\n  "))
            return
        self._run(argv)
        print(f"[apply] 已{note}")

    def open_pulls(self) -> list[dict]:
        return self._json([
            "pr", "list", "--repo", self.repo, "--state", "open", "--limit", "100",
            "--json", "number,title,mergeStateStatus,isDraft,headRefName,author",
        ]) or []

    def pull_facts(self, number: int, now: datetime) -> tuple[float, tuple[str, ...] | None]:
        """Age in hours and the check names reported for this pull request."""
        view = self._json(
            ["pr", "view", str(number), "--repo", self.repo, "--json", "createdAt,statusCheckRollup"]
        )
        if view is None:
            return 0.0, None
        created = parse_timestamp(view.get("createdAt"))
        age_hours = max(0.0, (now - created).total_seconds() / 3600.0) if created else 0.0
        names = set(check_names(view.get("statusCheckRollup") or []))
        listed = self._json(
            ["pr", "checks", str(number), "--repo", self.repo, "--json", "name,state"], allow_failure=True
        )
        for item in listed or []:
            if item.get("name"):
                names.add(str(item["name"]))
        return age_hours, tuple(sorted(names))


def parse_timestamp(text: str | None) -> datetime | None:
    if not text:
        return None
    try:
        parsed = datetime.fromisoformat(text.replace("Z", "+00:00"))
    except ValueError:
        return None
    return parsed if parsed.tzinfo else parsed.replace(tzinfo=timezone.utc)


def check_names(rollup: Sequence[dict]) -> tuple[str, ...]:
    """Check-run names and legacy status contexts, both of which can satisfy a required check."""
    names = set()
    for item in rollup:
        name = item.get("name") or item.get("context")
        if name:
            names.add(str(name))
    return tuple(sorted(names))


def gate_drift(manifest: Sequence[str], workflow_names: Sequence[str], ruleset_contexts: Sequence[str]) -> Drift:
    required = set(manifest)
    return Drift(
        missing_workflows=tuple(sorted(required - set(workflow_names))),
        missing_ruleset=tuple(sorted(required - set(ruleset_contexts))),
        extra_ruleset=tuple(sorted(set(ruleset_contexts) - required)),
    )


def drift_sections(drift: Drift) -> list[str]:
    return [
        f"- {label}：" + "、".join(f"`{name}`" for name in getattr(drift, field))
        for label, field in DRIFT_LABELS
        if getattr(drift, field)
    ]


def drift_body(drift: Drift) -> str:
    sections = drift_sections(drift)
    if not sections:
        return ""
    return "\n".join([
        "`scripts/reconcile_repo.py` 比较 `.github/quality-gate-checks.json`、workflow 里的 job 名与线上 ruleset 后发现不一致。",
        "",
        *sections,
        "",
        "修正 workflow 或 ruleset，使三者一致；本 issue 由脚本按标题去重维护，不自动关闭。",
    ])


def drift_recovered_comment() -> str:
    return "本次对账三者已一致。脚本不自动关闭本 issue，请人工确认后关闭。"


def drift_changed_comment(drift: Drift) -> str:
    return "本次对账的漂移内容有变化：\n\n" + drift_body(drift)


def outcome_route(conclusion: str) -> str:
    """Success comments, a cancellation is neither success nor a failure to escalate."""
    text = (conclusion or "").strip().lower()
    if text == "success":
        return PASSED
    if text in {"cancelled", "skipped"}:
        return IGNORED
    return ESCALATE


def parse_failed_jobs(value: str | Sequence[str] | None) -> tuple[str, ...]:
    text = "\n".join(str(item) for item in value) if isinstance(value, (list, tuple)) else str(value or "")
    items = text.replace(",", "\n").splitlines()
    names: list[str] = []
    for item in items:
        name = item.strip()
        if name and name not in names:
            names.append(name)
    return tuple(names)


def failure(workflow: str, conclusion: str, run_url: str, failed_jobs: Sequence[str] = ()) -> Failure:
    name = (workflow or "").strip()
    if not name:
        raise ValueError("workflow name is required")
    url = (run_url or "").strip()
    if not url:
        raise ValueError("run_url is required")
    return Failure(
        workflow=name,
        conclusion=(conclusion or "unknown").strip().lower(),
        run_url=url,
        failed_jobs=parse_failed_jobs(list(failed_jobs)),
    )


def failure_from_payload(data: dict) -> Failure:
    return failure(
        data.get("workflow") or data.get("workflow_name") or "",
        data.get("conclusion") or "unknown",
        data.get("run_url") or "",
        parse_failed_jobs(data.get("failed_jobs")),
    )


def failure_title(workflow: str) -> str:
    return f"[automation] {workflow} 失败"


def failed_job_line(event: Failure) -> str:
    return "、".join(f"`{job}`" for job in event.failed_jobs) if event.failed_jobs else "未报告具体 job"


def failure_body(event: Failure) -> str:
    return "\n".join([
        f"工作流 **{event.workflow}** 最近一次运行的结论是 `{event.conclusion}`。",
        "",
        f"- 运行：{event.run_url}",
        f"- 未通过的 job：{failed_job_line(event)}",
        "",
        "本 issue 由 `scripts/reconcile_repo.py` 按标题去重维护：后续失败只追加评论，恢复后追加一条通过评论；关闭由人工完成。",
    ])


def failure_comment(event: Failure) -> str:
    return f"新的失败运行：{event.run_url}（结论 `{event.conclusion}`，未通过的 job：{failed_job_line(event)}）"


def passed_comment(event: Failure) -> str:
    return f"下一次运行已通过：{event.run_url}（结论 `success`）。脚本不自动关闭本 issue，请人工确认后关闭。"


def escalate(github: GitHub, event: Failure, *, dry_run: bool) -> str:
    """Create or update the single open issue that belongs to this workflow."""
    route = outcome_route(event.conclusion)
    if route == IGNORED:
        print(f"结论 `{event.conclusion}` 不是成功，但也不算失败，不写入 issue。")
        return "ignored"
    title = failure_title(event.workflow)
    number = github.open_issue_number(title)
    if route == PASSED:
        if number is None:
            print("成功运行且没有对应的未关闭 issue，不写入任何内容。")
            return "passed"
        github.comment(number, passed_comment(event), dry_run=dry_run)
        return "passed-comment"
    if number is None:
        github.create_issue(title, failure_body(event), dry_run=dry_run)
        return "created"
    github.comment(number, failure_comment(event), dry_run=dry_run)
    return "commented"


def is_dependabot(pr: PullRequest) -> bool:
    return pr.author in DEPENDABOT_LOGINS or pr.head_ref.startswith("dependabot/")


def update_branch_command(number: int) -> str:
    return f"gh pr update-branch {number}"


def pull_request(data: dict, *, age_hours: float, reported_checks: tuple[str, ...] | None) -> PullRequest:
    author = data.get("author") or {}
    login = author.get("login") if isinstance(author, dict) else author
    return PullRequest(
        number=int(data["number"]),
        title=str(data.get("title") or ""),
        author=str(login or ""),
        head_ref=str(data.get("headRefName") or ""),
        merge_state=str(data.get("mergeStateStatus") or "UNKNOWN").upper(),
        is_draft=bool(data.get("isDraft")),
        age_hours=float(age_hours),
        reported_checks=reported_checks,
    )


def behind_prs(prs: Sequence[PullRequest]) -> tuple[BehindPR, ...]:
    return tuple(
        BehindPR(
            number=pr.number,
            title=pr.title,
            author=pr.author,
            automated=is_dependabot(pr),
            command=update_branch_command(pr.number),
        )
        for pr in prs
        if pr.merge_state == "BEHIND"
    )


def behind_line(entry: BehindPR) -> str:
    if entry.automated:
        return f"- #{entry.number} {entry.title}（{entry.author}，Dependabot）：可推进 — `{entry.command}`"
    return f"- #{entry.number} {entry.title}（{entry.author}，人工 PR）：建议由作者执行 `{entry.command}`（本脚本不自动执行）"


def unreported_checks(
    required: Sequence[str], reported: Sequence[str], *, age_hours: float, limit_hours: float
) -> tuple[str, ...]:
    missing = tuple(sorted(set(required) - set(reported)))
    if not missing or age_hours <= limit_hours:
        return ()
    return missing


def stale_prs(prs: Sequence[PullRequest], required: Sequence[str], limit_hours: float) -> tuple[StalePR, ...]:
    stale = []
    for pr in prs:
        # A draft is expected to wait, and unreadable checks are not evidence
        # that something is stuck.
        if pr.is_draft or pr.reported_checks is None:
            continue
        missing = unreported_checks(required, pr.reported_checks, age_hours=pr.age_hours, limit_hours=limit_hours)
        if missing:
            stale.append(StalePR(number=pr.number, title=pr.title, author=pr.author, missing=missing))
    return tuple(stale)


def render_report(
    *, drift: Drift, behind: Sequence[BehindPR], stale: Sequence[StalePR], open_prs: int, limit_hours: float
) -> str:
    lines = [
        "# 仓库对账（只读）",
        "",
        f"- 未关闭 PR：{open_prs}",
        f"- 落后 base：{len(behind)}",
        f"- 超过 {limit_hours:g} 小时仍未上报必需检查：{len(stale)}",
        "",
        "## 质量门三方一致",
    ]
    if drift.drifted:
        lines[-1] = "## 质量门三方漂移"
        lines.extend(drift_sections(drift))
    else:
        lines.append("manifest、workflow job 名与线上 ruleset 一致。")
    lines.extend(["", "## 落后 base 的 PR"])
    lines.extend([behind_line(entry) for entry in behind] or ["无。"])
    lines.extend(["", f"## 超过 {limit_hours:g} 小时仍未上报必需检查的 PR"])
    if stale:
        for entry in stale:
            names = "、".join(f"`{name}`" for name in entry.missing)
            lines.append(f"- #{entry.number} {entry.title}（{entry.author}）：未上报 {names}")
    else:
        lines.append("无。")
    return "\n".join(lines)


def build_summary(
    *, drift: Drift, behind: Sequence[BehindPR], stale: Sequence[StalePR], open_prs: int
) -> dict:
    return {
        "behind": [entry.number for entry in behind],
        "drift": {
            "extra_ruleset": list(drift.extra_ruleset),
            "missing_ruleset": list(drift.missing_ruleset),
            "missing_workflows": list(drift.missing_workflows),
        },
        "open_prs": open_prs,
        "stale": [entry.number for entry in stale],
    }


def format_summary(summary: dict) -> str:
    return SUMMARY_PREFIX + json.dumps(summary, sort_keys=True, ensure_ascii=False)


def apply_drift(github: GitHub, drift: Drift, *, dry_run: bool) -> str:
    """One open issue for the gate drift; running twice in a row writes once."""
    number = github.open_issue_number(DRIFT_TITLE)
    if not drift.drifted:
        if number is None:
            return "clean"
        github.comment(number, drift_recovered_comment(), dry_run=dry_run)
        return "clean-comment"
    body = drift_body(drift)
    if number is None:
        github.create_issue(DRIFT_TITLE, body, dry_run=dry_run)
        return "created"
    if github.issue_body(number).strip() == body.strip():
        print(f"质量门漂移 issue 已存在且内容未变：#{number}，本次不写入。")
        return "unchanged"
    github.comment(number, drift_changed_comment(drift), dry_run=dry_run)
    return "changed-comment"


def run_reconcile(args: argparse.Namespace) -> int:
    manifest = check_ci_gate.load_manifest(args.manifest)
    workflow_names = check_ci_gate.workflow_job_names(args.workflows_dir)
    github = GitHub(args.repo)
    drift = gate_drift(manifest, workflow_names, check_ci_gate.required_contexts(github.rulesets()))
    now = datetime.now(timezone.utc)
    prs = []
    for data in github.open_pulls():
        age_hours, reported = github.pull_facts(int(data["number"]), now)
        prs.append(pull_request(data, age_hours=age_hours, reported_checks=reported))
    behind = behind_prs(prs)
    stale = stale_prs(prs, manifest, args.limit_hours)
    print(render_report(drift=drift, behind=behind, stale=stale, open_prs=len(prs), limit_hours=args.limit_hours))
    print()
    action = apply_drift(github, drift, dry_run=args.dry_run)
    print(f"质量门 issue：{action}")
    print()
    print(format_summary(build_summary(drift=drift, behind=behind, stale=stale, open_prs=len(prs))))
    return 0


def read_failure(args: argparse.Namespace) -> Failure:
    if args.payload:
        text = sys.stdin.read() if args.payload == "-" else Path(args.payload).read_text()
        return failure_from_payload(json.loads(text))
    return failure(args.workflow_name or "", args.conclusion or "", args.run_url or "", args.failed_jobs)


def run_failure(args: argparse.Namespace) -> int:
    event = read_failure(args)
    action = escalate(GitHub(args.repo), event, dry_run=args.dry_run)
    print(ESCALATION_PREFIX + json.dumps(
        {"action": action, "conclusion": event.conclusion, "title": failure_title(event.workflow),
         "workflow": event.workflow},
        sort_keys=True, ensure_ascii=False,
    ))
    return 0


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--repo", default=DEFAULT_REPO)
    parser.add_argument("--dry-run", action="store_true", help="print the plan and write nothing")
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--workflows-dir", type=Path, default=DEFAULT_WORKFLOWS)
    parser.add_argument("--limit-hours", type=float, default=DEFAULT_LIMIT_HOURS,
                        help="hours before an unreported required check counts as stuck")
    parser.add_argument("--failure", action="store_true", help="escalate one workflow run instead of reconciling")
    parser.add_argument("--workflow-name")
    parser.add_argument("--run-url")
    parser.add_argument("--conclusion")
    parser.add_argument("--failed-jobs", action="append", default=[], help="repeatable; commas and newlines split")
    parser.add_argument("--payload", help="JSON file with workflow, conclusion, run_url and failed_jobs; - reads stdin")
    args = parser.parse_args(argv)
    try:
        return run_failure(args) if args.failure else run_reconcile(args)
    except (OSError, ValueError, CommandError, json.JSONDecodeError) as error:
        print(f"reconcile failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
