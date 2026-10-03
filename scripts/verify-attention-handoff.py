#!/usr/bin/env python3
"""Run the attention handoff through an isolated worktree.

This is an acceptance harness, not a product command. With no arguments it
pauses for a human to choose one of the fixed option IDs. ``--choice`` makes
the same path suitable for a repeatable local check.
"""

from __future__ import annotations

import argparse
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import time


# The fixture is a service failure, whose current event-specific subset also
# includes inspect_manifest. Keep the complete vocabulary here so a future
# fixture can opt into another documented action without accepting arbitrary
# model text.
FIXED_OPTIONS = {
    "inspect_logs",
    "inspect_port",
    "inspect_dependency",
    "inspect_manifest",
    "restart_service",
    "start_service",
    "stop_service",
    "adopt_manifest",
    "continue_without_action",
    "ask_user_for_context",
}
OPTIONS = {"inspect_logs", "inspect_manifest", "restart_service", "continue_without_action"}


def run(binary: Path, cwd: Path, home: Path, *args: str, check: bool = True) -> subprocess.CompletedProcess[str]:
    env = os.environ.copy()
    env["BERTH_HOME"] = str(home)
    result = subprocess.run(
        [str(binary), *args],
        cwd=cwd,
        env=env,
        text=True,
        capture_output=True,
    )
    if check and result.returncode != 0:
        raise RuntimeError(
            f"{' '.join(args)} failed with {result.returncode}:\n"
            f"stdout={result.stdout}\nstderr={result.stderr}"
        )
    return result


def read_status(binary: Path, project: Path, home: Path) -> dict:
    result = run(binary, project, home, "status", "--json")
    return json.loads(result.stdout)


def wait_for_attention(binary: Path, project: Path, home: Path) -> tuple[dict, dict]:
    deadline = time.monotonic() + 12
    while time.monotonic() < deadline:
        status = read_status(binary, project, home)
        path = status.get("attention_path")
        if path:
            artifact = json.loads(Path(path).read_text())
            return status, artifact
        time.sleep(0.25)
    raise RuntimeError("status did not publish an attention artifact")


def choose(explicit: str | None) -> str:
    choice = explicit
    if choice is None:
        print("attention: worker failed to start")
        print("choose one: inspect_logs, restart_service, continue_without_action", flush=True)
        choice = input("choice> ").strip()
    if choice not in OPTIONS:
        raise RuntimeError(f"unsupported choice {choice!r}; expected one of {sorted(OPTIONS)}")
    return choice


def verify_subscription(adapter: Path, project: Path, home: Path) -> None:
    """Exercise the built binary's long-lived NDJSON subscription path."""
    env = os.environ.copy()
    env["BERTH_HOME"] = str(home)
    process = subprocess.Popen(
        [
            str(adapter),
            "--subscribe",
            "--project",
            str(project),
            "--interval",
            "100ms",
            "--task",
            "make the worker available for the development task",
            "--authorize",
            "inspect_logs,inspect_manifest,restart_service,continue_without_action",
            "--api-key",
            "",
        ],
        cwd=project,
        env=env,
        text=True,
        stdout=subprocess.PIPE,
        stderr=subprocess.PIPE,
    )
    time.sleep(0.8)
    process.terminate()
    try:
        stdout, stderr = process.communicate(timeout=3)
    except subprocess.TimeoutExpired:
        process.kill()
        stdout, stderr = process.communicate(timeout=3)
    if process.returncode not in (0, -15):
        raise RuntimeError(f"subscription failed with {process.returncode}: {stderr}")
    records = [json.loads(line) for line in stdout.splitlines() if line.strip()]
    statuses = [record.get("status") for record in records]
    if "subscribed" not in statuses or "unavailable" not in statuses or "stopped" not in statuses:
        raise RuntimeError(f"subscription did not publish the expected lifecycle: {records}")
    ledger = home / "quality" / "events.ndjson"
    if not ledger.is_file():
        raise RuntimeError("subscription did not create the quality ledger")
    observations = [json.loads(line) for line in ledger.read_text().splitlines() if line.strip()]
    if not any(item.get("source") == "subscription" and item.get("latency_ms", 0) > 0 for item in observations):
        raise RuntimeError("subscription quality observations did not record latency")


def verify_quality_telemetry(
    adapter: Path,
    project: Path,
    home: Path,
    artifact: dict,
    choice: str,
    worktree_id: str,
    event_id: str,
    state_revision: str,
) -> dict:
    """Record one labelled interaction and verify the local quality report."""
    ledger = home / "quality" / "events.ndjson"
    if not ledger.is_file():
        raise RuntimeError("runner did not create the local quality ledger")
    feedback = project / ".attention-feedback.json"
    feedback.write_text(
        json.dumps(
            {
                "schema": "oberth.attention.feedback/v1",
                "event_id": event_id,
                "state_revision": state_revision,
                "worktree_id": worktree_id,
                "project": project.name,
                "human_needed": True,
                "task_relevant": True,
                "user_decision": "selected",
                "chosen_option": choice,
                "action_status": "succeeded" if choice == "restart_service" else "not_run",
                "resolved": choice == "restart_service",
            }
        )
    )
    recorded = run(adapter, project, home, "--feedback", str(feedback), "--project", str(project))
    feedback_result = json.loads(recorded.stdout)
    if feedback_result.get("status") != "feedback_recorded":
        raise RuntimeError(f"feedback was not recorded: {feedback_result}")
    report_result = run(adapter, project, home, "--quality-report", "--quality-project", project.name)
    report = json.loads(report_result.stdout)
    if report.get("feedback_records", 0) < 1 or report.get("labeled_cases", 0) < 1:
        raise RuntimeError(f"quality report did not join the labelled event: {report}")
    if not ledger.read_text().strip():
        raise RuntimeError("quality ledger is empty after feedback")
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--choice", choices=sorted(OPTIONS), help="fixed choice for a non-interactive check")
    args = parser.parse_args()

    repo = Path(__file__).resolve().parents[1]
    binary = repo / "bin" / "oberth"
    adapter = repo / "bin" / "jev-attention"
    if not binary.is_file() or not adapter.is_file():
        raise RuntimeError("build first with `mage build`")

    project = Path(tempfile.mkdtemp(prefix="oberth-hil-", dir=Path.home()))
    home = Path(tempfile.mkdtemp(prefix="oberth-home-"))
    try:
        (project / "oberth.yaml").write_text(
            """name: hil-check
services:
  - name: worker
    cmd: \"sh -c 'if test -e .once; then exec sleep 60; else touch .once; exit 1; fi'\"\n"""
        )

        first = run(binary, project, home, "up", "--json", check=False)
        if first.returncode == 0:
            raise RuntimeError("fixture unexpectedly started on the first attempt")

        status, artifact = wait_for_attention(binary, project, home)
        if artifact.get("schema") != "oberth.attention/v1":
            raise RuntimeError(f"unexpected attention schema: {artifact.get('schema')!r}")
        if artifact.get("state_revision") != status.get("attention_revision"):
            raise RuntimeError("status and attention state revisions differ")
        if artifact.get("event", {}).get("service") != "worker":
            raise RuntimeError(f"unexpected event: {artifact.get('event')!r}")
        options = {item.get("id") for item in artifact.get("options", [])}
        if not options or not options <= FIXED_OPTIONS:
            raise RuntimeError(f"artifact contains an unknown option: {sorted(options)}")

        adapter_result = run(
            adapter,
            project,
            home,
            "--auto",
            "--project",
            str(project),
            "--task",
            "make the worker available for the development task",
            "--authorize",
            "inspect_logs,inspect_manifest,restart_service,continue_without_action",
            "--api-key",
            "",
        )
        assessed = json.loads(adapter_result.stdout)
        if assessed.get("status") != "unavailable":
            raise RuntimeError(f"missing-key fallback was not used: {assessed}")
        after_fallback = json.loads(Path(status["attention_path"]).read_text())
        if "assessment" in after_fallback:
            raise RuntimeError("fallback unexpectedly changed the fact-only artifact")
        verify_subscription(adapter, project, home)

        choice = choose(args.choice)
        if choice not in options:
            raise RuntimeError(f"choice {choice!r} is not offered by this artifact: {sorted(options)}")
        if choice == "inspect_logs":
            run(binary, project, home, "logs", "worker", "--once", "--json")
            final = read_status(binary, project, home)
            if not final.get("attention_path"):
                raise RuntimeError("inspection unexpectedly cleared attention")
        elif choice == "inspect_manifest":
            manifest = project / "oberth.yaml"
            if not manifest.is_file() or "name: hil-check" not in manifest.read_text():
                raise RuntimeError("manifest inspection did not read the fixture manifest")
            final = read_status(binary, project, home)
            if not final.get("attention_path"):
                raise RuntimeError("manifest inspection unexpectedly cleared attention")
        elif choice == "continue_without_action":
            final = read_status(binary, project, home)
            if not final.get("attention_path"):
                raise RuntimeError("continuing unexpectedly cleared attention")
        else:
            run(binary, project, home, "restart", "--only", "worker", "--json")
            deadline = time.monotonic() + 5
            while True:
                final = read_status(binary, project, home)
                service = final["worktree"]["services"][0]
                if service.get("running") and not final.get("attention_path"):
                    break
                if time.monotonic() >= deadline:
                    raise RuntimeError(f"restart did not recover status: {final}")
                time.sleep(0.25)

        quality = verify_quality_telemetry(
            adapter,
            project,
            home,
            artifact,
            choice,
            assessed.get("worktree_id", ""),
            assessed.get("event_id", ""),
            assessed.get("attention_revision", ""),
        )

        print(
            json.dumps(
                {
                    "status": "passed",
                    "choice": choice,
                    "event_id": artifact["event_id"],
                    "attention_cleared": not bool(final.get("attention_path")),
                    "quality_labeled_cases": quality.get("labeled_cases"),
                },
                sort_keys=True,
            )
        )
        return 0
    finally:
        run(binary, project, home, "down", "--json", check=False)
        run(binary, project, home, "daemon", "stop", check=False)
        shutil.rmtree(project, ignore_errors=True)
        shutil.rmtree(home, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (RuntimeError, OSError, json.JSONDecodeError) as error:
        print(f"verify-attention-handoff: {error}", file=sys.stderr)
        raise SystemExit(1)
