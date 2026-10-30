#!/usr/bin/env python3
"""Check that required status checks, workflow jobs and the live ruleset agree."""
from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import re
import sys
from urllib.error import HTTPError
from urllib.request import Request, urlopen

DEFAULT_MANIFEST = Path(".github/quality-gate-checks.json")
DEFAULT_WORKFLOWS = Path(".github/workflows")
DEFAULT_REPO = "SheathedSharp/option-berth"
MATRIX_OS = re.compile(r"^\s*os:\s*\[(.*)\]\s*$")


def load_manifest(path: Path) -> tuple[str, ...]:
    data = json.loads(path.read_text())
    checks = data.get("required_checks")
    if not isinstance(checks, list) or not checks or not all(isinstance(item, str) and item for item in checks):
        raise ValueError(f"{path} must contain a non-empty required_checks list of strings")
    if len(checks) != len(set(checks)):
        raise ValueError(f"{path} contains duplicate required checks")
    return tuple(checks)


def job_names(text: str) -> set[str]:
    """Extract job names, expanding the simple matrix.os form used in this repo."""
    names: set[str] = set()
    current_name: str | None = None
    matrix_os: list[str] = []
    in_jobs = False

    def flush() -> None:
        if current_name is None:
            return
        if "${{" in current_name:
            if "matrix.os" in current_name and matrix_os:
                for value in matrix_os:
                    names.add(current_name.replace("${{ matrix.os }}", value))
            else:
                names.add(current_name)
        else:
            names.add(current_name)

    for line in text.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if line.startswith("jobs:"):
            flush()
            in_jobs = True
            current_name, matrix_os = None, []
            continue
        if not in_jobs:
            continue
        indent = len(line) - len(line.lstrip(" "))
        if indent == 2 and stripped.endswith(":") and not stripped.startswith("-"):
            flush()
            current_name, matrix_os = None, []
            continue
        if indent == 4 and stripped.startswith("name:"):
            current_name = stripped.split(":", 1)[1].strip().strip("\"'")
            continue
        match = MATRIX_OS.match(line)
        if match:
            matrix_os = [item.strip().strip("\"'") for item in match.group(1).split(",") if item.strip()]
    flush()
    return names


def workflow_job_names(directory: Path) -> set[str]:
    names: set[str] = set()
    for path in sorted(directory.glob("*.yml")) + sorted(directory.glob("*.yaml")):
        names |= job_names(path.read_text())
    return names


def required_contexts(rulesets: list[dict]) -> set[str]:
    contexts: set[str] = set()
    for ruleset in rulesets:
        if ruleset.get("enforcement") != "active" or ruleset.get("target") != "branch":
            continue
        include = ruleset.get("conditions", {}).get("ref_name", {}).get("include", [])
        if "~DEFAULT_BRANCH" not in include:
            continue
        for rule in ruleset.get("rules", []):
            if rule.get("type") == "required_status_checks":
                for check in rule.get("parameters", {}).get("required_status_checks", []):
                    context = check.get("context")
                    if context:
                        contexts.add(context)
    return contexts


def fetch_rulesets(repo: str, token: str | None = None) -> list[dict]:
    headers = {"Accept": "application/vnd.github+json", "User-Agent": "option-berth-ci-gate"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    base = f"https://api.github.com/repos/{repo}/rulesets"

    def get(url: str) -> dict:
        with urlopen(Request(url, headers=headers), timeout=20) as response:
            return json.load(response)

    try:
        listed = get(base)
    except HTTPError as error:
        # GITHUB_TOKEN may lack administration:read. Public repositories expose
        # the same ruleset read without authentication.
        if token and error.code in (401, 403, 404):
            return fetch_rulesets(repo, token=None)
        raise
    return [get(f"{base}/{item['id']}") for item in listed]


def compare(manifest: tuple[str, ...], names: set[str], contexts: set[str] | None = None) -> None:
    missing_workflows = sorted(set(manifest) - names)
    if missing_workflows:
        raise ValueError("required checks have no workflow job: " + ", ".join(missing_workflows))
    if contexts is None:
        return
    missing_ruleset = sorted(set(manifest) - contexts)
    if missing_ruleset:
        raise ValueError("ruleset is missing required checks: " + ", ".join(missing_ruleset))
    extra_ruleset = sorted(contexts - set(manifest))
    if extra_ruleset:
        raise ValueError("ruleset has checks missing from the manifest: " + ", ".join(extra_ruleset))


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, default=DEFAULT_MANIFEST)
    parser.add_argument("--workflows-dir", type=Path, default=DEFAULT_WORKFLOWS)
    parser.add_argument("--ruleset-file", type=Path)
    parser.add_argument("--check-ruleset", action="store_true")
    parser.add_argument("--repo", default=DEFAULT_REPO)
    args = parser.parse_args(argv)
    try:
        manifest = load_manifest(args.manifest)
        names = workflow_job_names(args.workflows_dir)
        contexts = None
        if args.ruleset_file:
            data = json.loads(args.ruleset_file.read_text())
            contexts = required_contexts(data if isinstance(data, list) else [data])
        elif args.check_ruleset:
            contexts = required_contexts(fetch_rulesets(args.repo, os.environ.get("GITHUB_TOKEN")))
        compare(manifest, names, contexts)
    except (OSError, ValueError) as error:
        print(f"quality gate check failed: {error}", file=sys.stderr)
        return 1
    suffix = f"; {len(contexts)} ruleset checks" if contexts is not None else ""
    print(f"quality gate: {len(manifest)} required checks match workflows{suffix}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
