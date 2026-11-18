#!/usr/bin/env python3
"""Classify a failed CI run so a re-run is spent only on a failure no change can fix.

The `retry` job in .github/workflows/ci.yml runs this after an area job fails.
It answers with one of three kinds, and only two of them may be re-run:

  infrastructure    The check never looked at the change: the job never ran a
                    step (runner queue timeout, cancelled before start), or the
                    step that failed installs a tool.
  flaky-candidate   The only failure is a Go test or package listed in
                    scripts/flaky-tests.txt. Nothing is a candidate by default.
  deterministic     Everything else, and every shape this module does not
                    recognise. A deterministic failure is never re-run.

A run is a candidate only when *no* failed job is deterministic: re-running a
known flake must never also re-run a real failure. Infrastructure is reported
ahead of a flake because the flake test result cannot be believed while a tool
install step is failing.

`classify` and `retry_allowed` are pure. scripts/retry-ci.sh owns the API calls,
the annotation and the summary; this module owns the decision.
"""
from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import re
import sys

INFRASTRUCTURE = "infrastructure"
FLAKY_CANDIDATE = "flaky-candidate"
DETERMINISTIC = "deterministic"

# The original attempt plus two re-runs. An attempt number is 1-based, the same
# value GitHub exposes as github.run_attempt / GITHUB_RUN_ATTEMPT.
MAX_ATTEMPTS = 3

# Job conclusions that mean the job was recorded as not passing. `neutral` is a
# step-level success and `skipped` is a job whose area this change did not need.
NOT_FAILED = frozenset({"success", "skipped", "neutral"})
KNOWN_FAILURE = frozenset({"failure", "cancelled", "timed_out", "startup_failure"})
# A step with no conclusion never started; a skipped step never ran either.
UNRUN_STEP = frozenset({"", "skipped"})

# Install steps are matched by name. The job log is one blob per job, so a
# command line such as `sudo apt-get update` from an earlier, successful step
# cannot be attributed to the step that actually failed; the step name can.
TOOL_INSTALL_MARKERS = ("install", "apt-get", "apt update")
# Only these steps may turn a failing test into a flake candidate.
TEST_STEP_MARKERS = ("test", "scenario")

TIMESTAMP = re.compile(r"^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z ?")
GO_TEST_FAILURE = re.compile(r"^\s*--- FAIL: (\S+)")
GO_PACKAGE_FAILURE = re.compile(r"^FAIL\s+(\S+)(.*)$")
BUILD_FAILURE_MARKERS = ("[build failed]", "[setup failed]")
# A package path or package/TestName: at least one `/`, no spaces. A bare test
# name is rejected so a typo cannot look like an entry while matching nothing.
ALLOWLIST_ENTRY = re.compile(r"^[A-Za-z0-9._~+-]+(?:/[A-Za-z0-9._~+-]+)+$")


@dataclass(frozen=True)
class Step:
    name: str
    conclusion: str

    @property
    def ran(self) -> bool:
        return self.conclusion not in UNRUN_STEP


@dataclass(frozen=True)
class Job:
    id: int
    name: str
    conclusion: str
    steps: tuple[Step, ...]

    @property
    def failed(self) -> bool:
        return self.conclusion not in NOT_FAILED

    @property
    def ran_any_step(self) -> bool:
        return any(step.ran for step in self.steps)

    @property
    def failed_steps(self) -> tuple[Step, ...]:
        return tuple(step for step in self.steps if step.ran and step.conclusion != "success")


@dataclass(frozen=True)
class PackageFailure:
    """What `go test` reported for one package: the tests it failed, or the build."""

    package: str
    tests: tuple[str, ...]
    build_failed: bool


@dataclass(frozen=True)
class Verdict:
    kind: str
    evidence: str

    @property
    def retryable(self) -> bool:
        return self.kind != DETERMINISTIC


def _text(value: object, field: str) -> str:
    if value is None:
        return ""
    if not isinstance(value, str):
        raise ValueError(f"{field} must be a string, got {type(value).__name__}")
    return value


def parse_jobs(payload: object) -> tuple[Job, ...]:
    """Read a workflow run's job list as the REST API returns it.

    Unknown shapes raise: a job this module cannot read is not evidence that
    the failure is safe to re-run.
    """
    if not isinstance(payload, dict) or not isinstance(payload.get("jobs"), list):
        raise ValueError("job list must be an object with a jobs array")
    total = payload.get("total_count")
    listed = payload["jobs"]
    if isinstance(total, int) and total > len(listed):
        raise ValueError(f"job list is truncated: total_count={total}, listed={len(listed)}")
    jobs = []
    for entry in listed:
        if not isinstance(entry, dict):
            raise ValueError("each job must be an object")
        if not isinstance(entry.get("name"), str) or not entry["name"]:
            raise ValueError("each job needs a non-empty name")
        if "conclusion" not in entry:
            raise ValueError(f"job {entry['name']!r} has no conclusion")
        job_id = entry.get("id")
        if not isinstance(job_id, int):
            raise ValueError(f"job {entry['name']!r} has no numeric id")
        steps = entry.get("steps") or []
        if not isinstance(steps, list):
            raise ValueError(f"job {entry['name']!r} has a non-list steps field")
        parsed_steps = []
        for step in steps:
            if not isinstance(step, dict):
                raise ValueError(f"job {entry['name']!r} has a step that is not an object")
            parsed_steps.append(Step(name=_text(step.get("name"), "step name"),
                                     conclusion=_text(step.get("conclusion"), "step conclusion")))
        jobs.append(Job(
            id=job_id,
            name=entry["name"],
            conclusion=_text(entry.get("conclusion"), "conclusion"),
            steps=tuple(parsed_steps),
        ))
    return tuple(jobs)


def parse_allowlist(text: str) -> frozenset[str]:
    """Read flaky-tests.txt: one package or package/TestName per line, `#` comments."""
    entries = set()
    for number, raw in enumerate(text.splitlines(), 1):
        entry = raw.split("#", 1)[0].strip()
        if not entry:
            continue
        if not ALLOWLIST_ENTRY.match(entry):
            raise ValueError(f"line {number}: expected package or package/TestName, got {entry!r}")
        entries.add(entry)
    return frozenset(entries)


def load_allowlist(path: Path) -> frozenset[str]:
    return parse_allowlist(path.read_text(encoding="utf-8"))


def parse_go_failures(log: str) -> tuple[PackageFailure, ...]:
    """Find every failing package in a job log, with the tests that failed in it.

    Lines carry the runner's ISO timestamp prefix. Test names are attributed to
    the next `FAIL <package>` line, which is where `go test` prints the import
    path for the block it just finished. A name with no following package line
    is dropped: an unattributable test cannot be matched against the allowlist.
    """
    tests: list[str] = []
    order: list[str] = []
    found: dict[str, tuple[list[str], bool]] = {}
    for raw in log.splitlines():
        line = TIMESTAMP.sub("", raw)
        match = GO_TEST_FAILURE.match(line)
        if match:
            tests.append(match.group(1))
            continue
        match = GO_PACKAGE_FAILURE.match(line)
        if not match:
            continue
        package, rest = match.group(1), match.group(2)
        build_failed = any(marker in rest for marker in BUILD_FAILURE_MARKERS)
        if package not in found:
            order.append(package)
            found[package] = ([], build_failed)
        names, previous = found[package]
        found[package] = (names + tests, previous or build_failed)
        tests = []
    return tuple(PackageFailure(package, tuple(found[package][0]), found[package][1]) for package in order)


def allowlisted(failure: PackageFailure, allowlist: frozenset[str]) -> str | None:
    """The allowlist entry that covers this failure, or None.

    A build failure is never covered: the allowlist pins a test that flakes, not
    a package that stopped compiling.
    """
    if failure.build_failed:
        return None
    for test in failure.tests:
        if f"{failure.package}/{test}" in allowlist:
            return f"{failure.package}/{test}"
    if failure.package in allowlist:
        return failure.package
    return None


def is_tool_install(step: Step) -> bool:
    name = step.name.lower()
    return any(marker in name for marker in TOOL_INSTALL_MARKERS)


def is_test_step(step: Step) -> bool:
    name = step.name.lower()
    return any(marker in name for marker in TEST_STEP_MARKERS)


def classify_job(job: Job, allowlist: frozenset[str], log: str) -> Verdict:
    if job.conclusion not in KNOWN_FAILURE:
        return Verdict(DETERMINISTIC, f"{job.name}: unknown job conclusion {job.conclusion!r}")
    if not job.ran_any_step:
        return Verdict(INFRASTRUCTURE, f"{job.name}: the job never ran a step ({job.conclusion})")
    steps = job.failed_steps
    if not steps:
        return Verdict(DETERMINISTIC, f"{job.name}: the job failed without a failed step ({job.conclusion})")
    for step in steps:
        if is_tool_install(step):
            return Verdict(INFRASTRUCTURE, f'{job.name}: step "{step.name}" installs a tool')
    test_steps = [step for step in steps if is_test_step(step)]
    if not test_steps:
        return Verdict(DETERMINISTIC, f'{job.name}: step "{steps[0].name}" failed')
    parsed = parse_go_failures(log)
    for failure in parsed:
        entry = allowlisted(failure, allowlist)
        if entry:
            return Verdict(FLAKY_CANDIDATE, f"{job.name}: {entry} is in the flaky allowlist")
    failed = parsed[0] if parsed else None
    if failed is None:
        return Verdict(DETERMINISTIC,
                       f'{job.name}: step "{test_steps[0].name}" failed; no Go test failure in its log')
    if failed.build_failed:
        return Verdict(DETERMINISTIC, f"{job.name}: {failed.package} did not compile")
    named = f"{failed.package}/{failed.tests[0]}" if failed.tests else failed.package
    return Verdict(DETERMINISTIC, f"{job.name}: {named} failed and is not in the flaky allowlist")


def classify(jobs: tuple[Job, ...], allowlist: frozenset[str], logs: dict[int, str]) -> Verdict:
    failed = [job for job in jobs if job.failed]
    if not failed:
        return Verdict(DETERMINISTIC, "the run has no failed job")
    verdicts = [classify_job(job, allowlist, logs.get(job.id, "")) for job in failed]
    for kind in (DETERMINISTIC, INFRASTRUCTURE):
        for verdict in verdicts:
            if verdict.kind == kind:
                return verdict
    return verdicts[0]


def retry_allowed(verdict: Verdict, attempt: int, max_attempts: int = MAX_ATTEMPTS) -> bool:
    """Whether this attempt may re-run the failed jobs. Deterministic never may."""
    if attempt < 1:
        raise ValueError(f"attempt must be 1 or greater, got {attempt}")
    if max_attempts < 1:
        raise ValueError(f"max_attempts must be 1 or greater, got {max_attempts}")
    return verdict.retryable and attempt < max_attempts


def read_logs(directory: Path, jobs: tuple[Job, ...]) -> dict[int, str]:
    """Read `<job id>.log` for each failed job; a missing log is empty, not fatal."""
    logs = {}
    for job in jobs:
        path = directory / f"{job.id}.log"
        if path.is_file():
            logs[job.id] = path.read_text(encoding="utf-8", errors="replace")
    return logs


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    parser.add_argument("--jobs", type=Path, required=True, help="REST jobs payload for one attempt")
    parser.add_argument("--log-dir", type=Path, help="directory holding <job id>.log files")
    parser.add_argument("--allowlist", type=Path, help="flaky-tests.txt")
    parser.add_argument("--list-failed-jobs", action="store_true",
                        help="print the id of every failed job and exit; the caller fetches their logs")
    parser.add_argument("--attempt", type=int, help="1-based attempt number of the calling job; adds retry=")
    parser.add_argument("--max-attempts", type=int, default=MAX_ATTEMPTS)
    parser.add_argument("--output", type=Path, help="append key=value lines here; defaults to $GITHUB_OUTPUT")
    args = parser.parse_args(argv)
    try:
        jobs = parse_jobs(json.loads(args.jobs.read_text(encoding="utf-8")))
        if args.list_failed_jobs:
            for job in jobs:
                if job.failed:
                    print(job.id)
            return 0
        allowlist = load_allowlist(args.allowlist) if args.allowlist else frozenset()
        logs = read_logs(args.log_dir, jobs) if args.log_dir else {}
        verdict = classify(jobs, allowlist, logs)
        retry = None if args.attempt is None else retry_allowed(verdict, args.attempt, args.max_attempts)
    except (OSError, ValueError) as error:
        print(f"ci failure classification failed: {error}", file=sys.stderr)
        return 2
    lines = [f"kind={verdict.kind}", f"max_attempts={args.max_attempts}"]
    if retry is not None:
        lines.append(f"retry={'true' if retry else 'false'}")
    lines.append(f"evidence={verdict.evidence}")
    text = "\n".join(lines) + "\n"
    target = args.output or (Path(os.environ["GITHUB_OUTPUT"]) if os.environ.get("GITHUB_OUTPUT") else None)
    if target:
        with open(target, "a", encoding="utf-8") as handle:
            handle.write(text)
    else:
        sys.stdout.write(text)
    print(f"retry classification: {verdict.kind} ({verdict.evidence})")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
