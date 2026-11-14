#!/usr/bin/env python3
"""Run the fixed agent scenario library and write a comparable matrix.

    python3 scripts/agent_eval.py --list
    python3 scripts/agent_eval.py --dry-run --suite large
    python3 scripts/agent_eval.py --execute --agents claude,codex,opencode,pi \\
        --suite all --arms oberth,native --scenarios all --repeat 2

The harness never adds provider approval/sandbox flags and never forwards a
credential unless its variable name is passed explicitly with --pass-env.
Each run gets its own HOME, XDG directories, BERTH_HOME, temp directory and
disposable project. See docs/agent/agent-eval.md for the contract and metrics.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import time
import traceback
from pathlib import Path
from typing import Any

from agent_eval_lib import (
    AGENTS,
    ARM_NATIVE,
    ARM_OBERTH,
    ARM_OBERTH_NOSKILL,
    ARMS,
    COMMON_PROVIDER_ENV,
    MIN_INTERVAL_RUNS,
    P95_MIN_RUNS,
    PROCESS_PROBE_COMMANDS,
    REPORT_SCHEMA,
    AgentSpec,
    Probe,
    ScenarioEnv,
    ScenarioState,
    blocked_action_attempts,
    compare_arms,
    cross_worktree_count,
    detect_oob,
    diff_tree,
    arm_uses_skill,
    protected_processes,
    wasted_process_attempts,
    free_port,
    git_changes,
    http_get_stable,
    mis_stop_count,
    port_open,
    read_actions,
    run_agent,
    run_reference,
    score_run,
    sha256_file,
    snapshot_tree,
    stage_skill,
    summarize_runs,
    utc_stamp,
    write_report,
)
from agent_eval_cold import (
    SCENARIOS as COLD_SCENARIOS,
    SUITE as COLD_SUITE,
    adopt_draft,
    budget_note,
    reference_draft,
    write_reference_draft,
)
from agent_eval_scenarios import SCENARIOS as CORE_SCENARIOS, Scenario
from agent_eval_stacks import LARGE_SCENARIOS, ensure_image

# The core library proves the failure paths on small fixtures. The large library
# keeps the same shape but puts the task inside a ten-service stack with slow
# dependencies, a prepared frontend, a migrated database, portless workers and
# parallel worktrees. The cold library removes the manifest entirely, so the run
# has to draft and adopt one first. They share one harness and one judge
# vocabulary.
SUITES = ("core", "large", COLD_SUITE)
SCENARIOS: dict[str, Scenario] = {
    **CORE_SCENARIOS, **LARGE_SCENARIOS, **COLD_SCENARIOS,
}

# The cheapest scenario that still forces the agent to act. `--smoke` uses it to
# put each requested agent through a real tool round trip before a matrix spends
# hours: reading the project and missing an artifact takes one or two actions,
# which is enough to prove the loop closes and cheap enough to run per provider.
SMOKE_SCENARIO = "missing-artifact"

# One line per arm, in ARMS order, for --list.
ARM_DESCRIPTIONS = (
    "oberth CLI and the pinned skill",
    "oberth CLI without the skill (isolates what the skill adds)",
    "native tools only (control)",
)

ROOT = Path(__file__).resolve().parent.parent


class PreflightError(RuntimeError):
    pass


def parse_list(value: str, allowed: list[str], what: str) -> list[str]:
    if value.strip() == "all":
        return allowed
    selected = [part.strip() for part in value.split(",") if part.strip()]
    unknown = [part for part in selected if part not in allowed]
    if unknown:
        raise SystemExit(f"unknown {what}: {', '.join(unknown)} (choose from {', '.join(allowed)})")
    if not selected:
        raise SystemExit(f"no {what} selected")
    return selected


def find_skill_dir(explicit: str | None) -> Path | None:
    candidates = []
    if explicit:
        candidates.append(Path(explicit).expanduser())
    candidates.extend([
        ROOT.parent / "option-berth-skill",
        Path.home() / ".agents" / "skills" / "option-berth",
        Path.home() / ".claude" / "skills" / "option-berth",
    ])
    for candidate in candidates:
        if (candidate / "SKILL.md").exists():
            return candidate.resolve()
    return None


def skill_identity(skill: Path | None) -> dict[str, Any]:
    if skill is None:
        return {"path": None, "version": None, "commit": None, "sha256": None}
    text = (skill / "SKILL.md").read_text(encoding="utf-8", errors="replace")
    match = re.search(r"(?m)^\s*version:\s*[\"']?([^\"'\n]+)", text)
    version = match.group(1).strip() if match else None
    commit = None
    proc = subprocess.run(["git", "-C", str(skill), "rev-parse", "HEAD"], capture_output=True, text=True)
    if proc.returncode == 0:
        commit = proc.stdout.strip() or None
    return {
        "path": str(skill),
        "version": version,
        "commit": commit,
        "sha256": sha256_file(skill / "SKILL.md"),
    }


def build_identity(binary: Path) -> dict[str, Any]:
    if not binary.exists():
        raise SystemExit(
            f"oberth binary not found at {binary}; build it with `mage buildEngine` "
            "or pass --binary"
        )
    proc = subprocess.run([str(binary), "version", "--json"], capture_output=True, text=True, timeout=20)
    version = commit = built = platform_name = None
    if proc.returncode == 0 and proc.stdout.strip():
        try:
            doc = json.loads(proc.stdout)
            version = doc.get("version")
            commit = doc.get("commit")
            built = doc.get("built")
            platform_name = doc.get("platform")
        except json.JSONDecodeError:
            pass
    repo_commit = None
    git_proc = subprocess.run(["git", "-C", str(ROOT), "rev-parse", "HEAD"], capture_output=True, text=True)
    if git_proc.returncode == 0:
        repo_commit = git_proc.stdout.strip() or None
    return {
        "binary": str(binary),
        "sha256": sha256_file(binary),
        "version": version,
        "commit": commit,
        "built": built,
        "platform": platform_name,
        "repo_commit": repo_commit,
    }


def require_docker() -> None:
    """The complex benchmark runs a real Postgres container; no Docker, no run."""
    checks = (
        (["docker", "info", "--format", "{{.ServerVersion}}"], "a running Docker daemon"),
        (["docker", "compose", "version"], "the docker compose plugin"),
    )
    for argv, what in checks:
        try:
            done = subprocess.run(argv, capture_output=True, text=True, timeout=30)
        except (OSError, subprocess.SubprocessError) as exc:
            raise PreflightError(f"the complex benchmark needs {what}: {exc}") from exc
        if done.returncode:
            raise PreflightError(
                f"the complex benchmark needs {what}; `{' '.join(argv)}` failed:\n"
                f"  {(done.stderr or done.stdout).strip()}"
            )


def preflight(binary: Path, out_root: Path, pass_env: list[str]) -> None:
    """Prove the engine can observe listeners before scoring any agent."""
    env_root = Path(tempfile.mkdtemp(prefix="oberth-eval-", dir=tempfile.gettempdir()))
    env = ScenarioEnv(env_root, binary, os.environ.get("PATH", ""), pass_env=pass_env)
    try:
        env.start_runtime_service()
        port = free_port()
        project = env.write_project("preflight", {
            "oberth.yaml": (
                "name: preflight\n"
                "services:\n"
                "  - name: api\n"
                "    cmd: python3 app.py\n"
                f"    port: {port}\n"
                "    health: /health\n"
            ),
            "app.py": (
                "import time\n"
                "from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer\n"
                f"PORT = {port}\n"
                "class Handler(BaseHTTPRequestHandler):\n"
                "    def do_GET(self):\n"
                "        body = b'{\"ok\": true}'\n"
                "        self.send_response(200)\n"
                "        self.send_header('Content-Length', str(len(body)))\n"
                "        self.end_headers()\n"
                "        self.wfile.write(body)\n"
                "    def log_message(self, fmt, *args):\n"
                "        pass\n"
                "ThreadingHTTPServer(('127.0.0.1', PORT), Handler).serve_forever()\n"
            ),
        })
        proc, doc = env.up(project, timeout=45)
        if proc.returncode != 0 or not isinstance(doc, dict):
            detail = _engine_error(proc)
            raise PreflightError(
                "preflight could not start a trivial service. The scenario matrix would "
                "measure the environment, not an agent.\n"
                f"  {detail}\n"
                "  Run this harness from a normal terminal outside a coding-agent sandbox, "
                "or fix the listener observation path first."
            )
        _, status_doc = env.status(project)
        services = (status_doc.get("worktree") or {}).get("services") if isinstance(status_doc, dict) else []
        if not services or not services[0].get("running"):
            raise PreflightError(
                f"preflight service did not appear in status: up={json.dumps(doc)[:1200]} "
                f"status={json.dumps(status_doc)[:1200]}"
            )
    finally:
        env.cleanup({"main": project} if "project" in locals() else {})


def _engine_error(proc: subprocess.CompletedProcess[str]) -> str:
    text = (proc.stderr or proc.stdout or "").strip()
    try:
        doc = json.loads(text)
        error = doc.get("error") or {}
        return f"code={error.get('code')} message={error.get('message')} hint={error.get('hint')}"
    except json.JSONDecodeError:
        return text[-2000:]


def collect_probe(
    scenario: Scenario,
    state: ScenarioState,
    env: ScenarioEnv,
    agent_result: Any,
    before: dict[str, Any],
    actions: list[dict[str, Any]],
    run_dir: Path,
) -> Probe:
    statuses: dict[str, dict[str, Any] | None] = {}
    status_errors: dict[str, str] = {}
    for label, root in state.worktrees.items():
        proc, doc = env.status(root)
        statuses[label] = doc if isinstance(doc, dict) else None
        if statuses[label] is None:
            status_errors[label] = _engine_error(proc)
    http = {key: http_get_stable(url) for key, url in scenario.urls(state).items()}
    after = {label: snapshot_tree(root) for label, root in state.worktrees.items()}
    diffs = {label: diff_tree(before[label], after[label]) for label in state.worktrees}
    changes = {label: git_changes(before[label], after[label]) for label in state.worktrees}
    foreign_alive = {str(listener.port): listener.alive() for listener in env.held}
    extra_ports_open = {name: port_open(port) for name, port in state.extra_ports.items()}
    # Keep the action log after the isolated environment is removed.
    actions_copy = run_dir / "actions.ndjson"
    try:
        actions_copy.write_bytes(env.actions_log.read_bytes())
    except OSError:
        pass
    artifacts = {
        "agent_stdout": _relative(agent_result.stdout_path, run_dir),
        "agent_stderr": _relative(agent_result.stderr_path, run_dir),
        "actions": _relative(actions_copy, run_dir),
    }
    return Probe(
        statuses=statuses,
        status_errors=status_errors,
        http=http,
        diffs=diffs,
        git_changes=changes,
        actions=actions,
        foreign_alive=foreign_alive,
        extra_ports_open=extra_ports_open,
        agent=agent_result,
        artifacts=artifacts,
    )


def _relative(path: Path, root: Path) -> str:
    try:
        return str(Path(path).resolve().relative_to(root.resolve()))
    except ValueError:
        return str(path)


def execute_run(
    scenario: Scenario,
    agent_id: str,
    spec: AgentSpec,
    repeat: int,
    *,
    arm: str,
    out_root: Path,
    binary: Path,
    skill: Path | None,
    args: argparse.Namespace,
    build: dict[str, Any],
    extras: list[str],
) -> dict[str, Any]:
    run_dir = out_root / "artifacts" / f"{scenario.id}__{agent_id}__{arm}__r{repeat}"
    run_dir.mkdir(parents=True, exist_ok=True)
    env_root = Path(tempfile.mkdtemp(prefix="oberth-eval-", dir=tempfile.gettempdir()))
    env = ScenarioEnv(
        env_root, binary, os.environ.get("PATH", ""),
        pass_env=args.pass_env, keep=args.keep, arm=arm,
    )
    state: ScenarioState | None = None
    started = time.monotonic()
    record: dict[str, Any] = {
        "scenario": scenario.id,
        "agent": agent_id,
        "arm": arm,
        "repeat": repeat,
        "status": "harness_error",
        "completed": False,
        "steps": None,
        "steps_source": "unavailable",
        "command_steps": 0,
        "oberth_calls": 0,
        "oberth_output_bytes": 0,
        "process_probe_calls": 0,
        "agent_output_bytes": 0,
        "mis_stops": 0,
        "cross_worktree_incidents": 0,
        "oob": [],
        "oob_count": 0,
        "checks": [],
        "failed_checks": [],
        "notes": [],
        "duration_s": 0.0,
        "build_sha": build["sha256"],
        "agent_version": spec.version(),
        "exit_code": None,
        "timed_out": False,
        "ports": {},
        "artifacts": {},
    }
    try:
        env.start_runtime_service()
        state = scenario.setup(env)
        record["ports"] = state.ports
        before = {label: snapshot_tree(root) for label, root in state.worktrees.items()}
        if scenario.suite == COLD_SUITE:
            # A cold start has to draft, and the drafting run reads the agent
            # settings from this run's own BERTH_HOME. The reference flow names
            # the agent explicitly; a real provider is named the same way, from
            # what the operator passed on the command line.
            env.write_agent_config(agent_id, extras)
        if agent_id == "reference":
            if not arm_uses_skill(arm):
                raise RuntimeError("the reference script is a skill-arm self-check")
            if scenario.suite == COLD_SUITE:
                # The reference arm runs no model, so it cannot draft. It writes
                # the manifest a person who knew this repository would write and
                # lets the ordinary `init adopt` path take it from there; the
                # drafting step stays uncovered by this arm, and the docs say so.
                write_reference_draft(env, state, reference_draft(scenario.id))
            result = run_reference(scenario, state, env=env, out_dir=run_dir, timeout=args.timeout)
        else:
            staged_skill = None
            if arm_uses_skill(arm):
                if skill is None:
                    raise RuntimeError("a skill checkout is required for the skill arm; pass --skill-dir")
                staged_skill = stage_skill(env, agent_id, skill)
            result = run_agent(
                spec, cwd=state.primary, prompt=scenario.prompt, skill=staged_skill,
                env=env, out_dir=run_dir, timeout=args.timeout, extras=extras,
            )
        if scenario.suite == COLD_SUITE:
            # The person who would adopt is the harness here, under one fixed
            # rule: only a draft that already passes `init adopt --dry-run` is
            # written, and the record says the harness did it.
            adoption = adopt_draft(env, state)
            record["drafted"] = adoption["drafted"]
            record["adopted_by"] = adoption["adopted_by"]
            if adoption.get("note"):
                record["notes"].append(adoption["note"])
        time.sleep(0.4)
        actions = read_actions(env.actions_log)
        # A completed oberth call records a second result row with its stdout
        # size. It is measurement, not another command.
        call_actions = [action for action in actions if action.get("cmd") != "oberth.result"]
        probe = collect_probe(scenario, state, env, result, before, actions, run_dir)
        verdict = scenario.judge(probe, state)
        oob = detect_oob(
            diffs=probe.diffs, git_changes=probe.git_changes,
            statuses=probe.statuses, state=state, actions=call_actions, held=env.held,
            arm=arm,
            # A cold start begins with no manifest; writing one is the task, so
            # it is not the "the manifest was edited" violation it is elsewhere.
            allow_manifest_write=scenario.suite == COLD_SUITE,
        )
        command_steps = len(call_actions)
        oberth_calls = sum(1 for action in call_actions if action.get("cmd") == "oberth")
        oberth_output_bytes = sum(
            int(action.get("bytes") or 0)
            for action in actions if action.get("cmd") == "oberth.result"
        )
        process_probe_calls = sum(1 for action in call_actions if action.get("cmd") in PROCESS_PROBE_COMMANDS)
        steps = result.steps
        steps_source = result.steps_source
        if steps is None:
            steps = command_steps
            if agent_id == "reference":
                steps_source = "command-shim"
            else:
                steps_source = "command-shim (fallback)"
                record["notes"].append("provider event shape not recognized; steps count external commands")
        score = score_run(
            checks=verdict.checks, oob=oob, actions=call_actions,
            budget=getattr(scenario, "budget", None),
            steps=result.steps,
            unsafe_attempts=len(blocked_action_attempts(
                call_actions, arm=arm, protected=protected_processes(state, env.held),
            )),
        )
        completed = (
            score["correctness"] == 1
            and score["safety"]
            and not oob
            and not result.timed_out
        )
        if result.timed_out:
            status = "agent_error"
        elif result.error and not completed:
            status = "agent_error"
        else:
            status = "ok" if completed else "not_completed"
        agent_output_bytes = sum(
            path.stat().st_size for path in (result.stdout_path, result.stderr_path)
            if path.exists()
        )
        wasted_signals = wasted_process_attempts(call_actions, state=state, held=env.held, arm=arm)
        if wasted_signals:
            record["notes"].append(
                "stop signals aimed at this run's own processes (cleanup, not an incident): "
                + "; ".join(item["detail"] for item in wasted_signals)
            )
        record.update({
            "status": status,
            "completed": completed,
            "steps": steps,
            "steps_source": steps_source,
            "command_steps": command_steps,
            "oberth_calls": oberth_calls,
            "oberth_output_bytes": oberth_output_bytes,
            "process_probe_calls": process_probe_calls,
            "agent_output_bytes": agent_output_bytes,
            "mis_stops": mis_stop_count(oob),
            "cross_worktree_incidents": cross_worktree_count(oob, state),
            "score": score,
            "oob": oob,
            "oob_count": len(oob),
            "checks": verdict.checks,
            "failed_checks": [item["name"] for item in verdict.checks if not item["ok"]],
            "notes": record["notes"] + verdict.notes + ([result.error] if result.error else []),
            "duration_s": round(time.monotonic() - started, 3),
            "exit_code": result.exit_code,
            "timed_out": result.timed_out,
            "artifacts": probe.artifacts,
            "evidence": verdict.evidence,
            "status_errors": probe.status_errors,
            "http": {name: {"status": result_.status, "error": result_.error} for name, result_ in probe.http.items()},
            "git_changes": probe.git_changes,
        })
    except Exception as exc:  # noqa: BLE001 - one run must not abort the matrix
        record["error"] = f"{type(exc).__name__}: {exc}"
        record["traceback"] = traceback.format_exc(limit=6)
        record["status"] = "harness_error"
        record["duration_s"] = round(time.monotonic() - started, 3)
    finally:
        try:
            record["cleanup_notes"] = env.cleanup(state.worktrees if state else None)
        except Exception as exc:  # noqa: BLE001
            record["cleanup_notes"] = [f"cleanup failed: {type(exc).__name__}: {exc}"]
    return record


def dry_run(scenario_ids: list[str], *, out_root: Path, binary: Path, args: argparse.Namespace) -> int:
    failures = 0
    for scenario_id in scenario_ids:
        scenario = SCENARIOS[scenario_id]
        run_dir = out_root / "dry-run" / scenario_id
        env_root = Path(tempfile.mkdtemp(prefix="oberth-eval-", dir=tempfile.gettempdir()))
        env = ScenarioEnv(env_root, binary, os.environ.get("PATH", ""), pass_env=args.pass_env, keep=args.keep)
        state = None
        try:
            env.start_runtime_service()
            state = scenario.setup(env)
            before = {label: snapshot_tree(root) for label, root in state.worktrees.items()}
            print(
                f"ok  {scenario.id}: worktrees={sorted(state.worktrees)} "
                f"ports={state.ports} files={sum(len(s.hashes) for s in before.values())}"
            )
        except Exception as exc:  # noqa: BLE001
            failures += 1
            print(f"ERR {scenario.id}: {type(exc).__name__}: {exc}", file=sys.stderr)
        finally:
            env.cleanup(state.worktrees if state else None)
    return 1 if failures else 0


def smoke(
    agent_ids: list[str],
    *,
    out_root: Path,
    binary: Path,
    skill: Path | None,
    extras: dict[str, list[str]],
    args: argparse.Namespace,
) -> int:
    """Prove each agent can close one real turn loop before a matrix spends hours.

    The gateway check answers a different question from the one that matters.
    "Does this endpoint accept this model name" is a single request with no
    history; "will the agent work" is a multi-turn loop where the model asks for
    a tool, reads the result, and sends the conversation back. On 2026-11-02 the
    single-request check passed and all 80 matrix rows died on turn two, because
    the provider required `reasoning_content` on every earlier assistant message
    and the client never sends it. Nothing that inspects one request can see a
    second-request requirement, and nothing that reimplements the client can see
    a client-side refusal.

    So this is not the same check at higher fidelity — it is the real path,
    minimized. It drives the cheapest real scenario through the same runner,
    fixtures and isolation the matrix uses, and asks the two things a preflight
    can honestly answer: did the agent report an error, and did it take at least
    one action? An action proves the loop closed once, which is exactly the part
    a single-turn check cannot reach.
    """
    scenario = SCENARIOS[SMOKE_SCENARIO]
    failures = 0
    for agent_id in agent_ids:
        spec = AGENTS[agent_id]
        if agent_id != "reference" and not shutil.which(spec.command):
            print(f"smoke {agent_id}: {spec.command} is not installed", file=sys.stderr)
            failures += 1
            continue
        run_dir = out_root / "smoke" / agent_id
        env_root = Path(tempfile.mkdtemp(prefix="oberth-smoke-", dir=tempfile.gettempdir()))
        env = ScenarioEnv(env_root, binary, os.environ.get("PATH", ""), pass_env=args.pass_env)
        state = None
        started = time.monotonic()
        try:
            env.start_runtime_service()
            state = scenario.setup(env)
            staged_skill = stage_skill(env, agent_id, skill) if skill is not None else None
            result = run_agent(
                spec, cwd=state.primary, prompt=scenario.prompt, skill=staged_skill,
                env=env, out_dir=run_dir, timeout=args.timeout, extras=extras.get(agent_id),
            )
            elapsed = time.monotonic() - started
            problem = None
            if result.error:
                # The agent's own words, not a summary: the operator has to be
                # able to act on it without opening the artifact.
                problem = result.error
            elif not result.steps:
                problem = (
                    "the agent finished without taking a single action; a run that never "
                    "touches the project cannot tell a broken loop from a quiet one"
                )
            if problem:
                failures += 1
                print(f"smoke {agent_id}: FAILED after {elapsed:.0f}s\n  {problem}", file=sys.stderr)
            else:
                print(
                    f"smoke {agent_id}: ok ({result.steps} actions, {elapsed:.0f}s)"
                    " — the model completed a full tool round trip",
                    file=sys.stderr,
                )
        except Exception as exc:  # noqa: BLE001
            failures += 1
            print(f"smoke {agent_id}: {type(exc).__name__}: {exc}", file=sys.stderr)
        finally:
            env.cleanup(state.worktrees if state else None)
    return 1 if failures else 0


def _cold_start_budget(runs: list[dict[str, Any]], scenario_ids: list[str]) -> dict[str, Any]:
    """State what a cold-start observation actually costs, per scenario.

    `init draft` runs a second agent process inside the run, so a cold-start row
    is two provider calls, not one. The block is absent when the matrix has no
    cold-start scenario, so a core or large report does not grow a field that
    would always read zero.
    """
    cold = [run for run in runs if run.get("scenario") in COLD_SCENARIOS]
    if not cold:
        return {}
    per_scenario = []
    for scenario_id in [sid for sid in scenario_ids if sid in COLD_SCENARIOS]:
        rows = [run for run in cold if run["scenario"] == scenario_id]
        valid = [run for run in rows if run.get("status") != "harness_error"]
        drafting = sum(1 for run in rows if run.get("drafted"))
        per_scenario.append({
            "scenario": scenario_id,
            "observations": len(valid),
            "drafting_runs": drafting,
            "agent_calls": len(valid) + drafting,
            "adopted_by_harness": sum(1 for run in rows if run.get("adopted_by") == "harness"),
            "adopted_by_agent": sum(1 for run in rows if run.get("adopted_by") == "agent"),
            "not_adopted": sum(1 for run in valid if not run.get("adopted_by")),
        })
    return {
        **budget_note(),
        "per_scenario": per_scenario,
        "observations": sum(item["observations"] for item in per_scenario),
        "drafting_runs": sum(item["drafting_runs"] for item in per_scenario),
        "agent_calls": sum(item["agent_calls"] for item in per_scenario),
        "adopted_by_harness": sum(item["adopted_by_harness"] for item in per_scenario),
    }


def parse_compare(value: str) -> tuple[str, str]:
    """Read the `TREATED,CONTROL` arm pair used for the comparison table."""
    parts = [part.strip() for part in value.split(",") if part.strip()]
    if len(parts) != 2:
        raise SystemExit(f"--compare expects TREATED,CONTROL, got {value!r}")
    treated, control = parts
    for arm in (treated, control):
        if arm not in ARMS:
            raise SystemExit(f"unknown arm in --compare: {arm} (choose from {', '.join(ARMS)})")
    if treated == control:
        raise SystemExit("--compare needs two different arms")
    return treated, control


def merge_reports(
    paths: list[str], out_root: Path, *, require_comparable: bool,
    compare: tuple[str, str] = (ARM_OBERTH, ARM_NATIVE),
    expect_scenarios: list[str] | None = None,
    require_coverage: bool = False,
) -> int:
    """Combine shard reports into one matrix.

    Shards of one experiment may cover different scenarios and different
    agents — that is the point of splitting by the cost unit. What must match
    is everything that would make two rows incomparable: the binary, the skill,
    the arm set, the repeat count and the provider arguments. A scenario or
    agent union is the merged experiment; an equality check here would reject
    exactly the split the workflow is designed to produce.
    """
    shards = []
    for raw in paths:
        path = Path(raw).expanduser().resolve()
        try:
            shards.append((path, json.loads(path.read_text(encoding="utf-8"))))
        except (OSError, json.JSONDecodeError) as exc:
            print(f"cannot read shard {path}: {exc}", file=sys.stderr)
            return 1
    if not shards:
        print("merge: no shard reports given", file=sys.stderr)
        return 1

    base = shards[0][1]
    for path, doc in shards[1:]:
        for key in ("build", "skill", "repeat", "arms", "passed_env", "agent_args", "models"):
            if doc.get(key) != base.get(key):
                print(
                    f"merge: shard {path} differs on {key}; refusing to combine incomparable rows",
                    file=sys.stderr,
                )
                return 1

    # A scenario id measured with a different prompt in another shard is a
    # different task even though the id matches.
    scenario_prompts: dict[str, str] = {}
    for path, doc in shards:
        for scenario_id, prompt in (doc.get("scenario_prompts") or {}).items():
            if scenario_id in scenario_prompts and scenario_prompts[scenario_id] != prompt:
                print(
                    f"merge: shard {path} disagrees about the prompt for scenario {scenario_id!r}",
                    file=sys.stderr,
                )
                return 1
            scenario_prompts[scenario_id] = prompt
    scenarios = sorted(
        {scenario_id for _, doc in shards for scenario_id in (doc.get("scenarios") or [])}
        | set(scenario_prompts)
    )
    if not scenarios:
        print("merge: no shard names a scenario", file=sys.stderr)
        return 1

    # Two shards may both run claude; they must agree on the CLI version, or
    # their rows were produced by different clients.
    agents: dict[str, str] = {}
    for path, doc in shards:
        for agent_id, version in (doc.get("agents") or {}).items():
            if agent_id in agents and agents[agent_id] != version:
                print(
                    f"merge: shard {path} reports {agent_id} {version!r}, "
                    f"another shard reports {agents[agent_id]!r}",
                    file=sys.stderr,
                )
                return 1
            agents[agent_id] = version

    runs: list[dict[str, Any]] = []
    seen: dict[tuple[str, str, str, int], int] = {}
    for _, doc in shards:
        for run in doc.get("runs", []):
            key = (run["scenario"], run["agent"], run.get("arm", ARM_OBERTH), run["repeat"])
            if key in seen:
                # A shard that hit a harness error may coexist with a good run
                # of the same key; keep the observation that produced facts.
                if run.get("status") != "harness_error":
                    runs[seen[key]] = run
                continue
            seen[key] = len(runs)
            runs.append(run)

    expected = list(expect_scenarios or [])
    missing = [scenario_id for scenario_id in expected if scenario_id not in scenarios]
    notes = [f"merged from {len(shards)} shard report(s)"]
    if expected:
        present = len(expected) - len(missing)
        notes.append(f"coverage: {present}/{len(expected)} requested scenarios present")
        if missing:
            notes.append("missing scenarios: " + ", ".join(missing))
    if len(agents) > 1:
        notes.append(f"agents in this merge: {', '.join(sorted(agents))}")

    build = base["build"]
    summary = summarize_runs(runs, build["sha256"])
    comparable = bool(summary) and all(row["comparable"] for row in summary)
    payload = {
        "schema": REPORT_SCHEMA,
        "generated_at": utc_stamp(),
        "build": build,
        "skill": base.get("skill") or {},
        "agents": agents,
        "arms": base.get("arms") or [ARM_OBERTH],
        "passed_env": base.get("passed_env") or [],
        "agent_args": base.get("agent_args") or {},
        "models": base.get("models") or {},
        "repeat": base.get("repeat"),
        "scenarios": scenarios,
        "scenario_prompts": scenario_prompts,
        "comparable": comparable,
        "runs": runs,
        "summary": summary,
        "compare_arms": list(compare),
        "comparison": compare_arms(summary, *compare),
        "notes": notes,
        "cold_start": _cold_start_budget(runs, scenarios),
    }
    json_path, md_path = write_report(out_root, payload)
    print(f"wrote {json_path}\nwrote {md_path}", file=sys.stderr)
    if require_coverage and missing:
        print(
            f"require-coverage: missing {len(missing)} requested scenario(s): {', '.join(missing)}",
            file=sys.stderr,
        )
        return 1
    if require_comparable and not comparable:
        print("require-comparable: the merged matrix is not comparable", file=sys.stderr)
        return 1
    return 0


JOURNAL_SCHEMA = "oberth.agent-eval.journal/v1"


def journal_path(out_root: Path) -> Path:
    return out_root / "journal.json"


def write_journal(out_root: Path, meta: dict[str, Any], runs: list[dict[str, Any]]) -> None:
    """Persist every completed run immediately.

    A cancelled job keeps whatever this file holds; the report is a view over
    it, not the only copy. Rewriting the whole file is deliberate: a partial
    line from a killed process must not turn the journal into an unreadable
    one. The file is small (one record per run) and the rename is atomic.
    """
    out_root.mkdir(parents=True, exist_ok=True)
    tmp = out_root / "journal.json.tmp"
    doc = {"schema": JOURNAL_SCHEMA, "meta": meta, "runs": runs}
    tmp.write_text(json.dumps(doc, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    tmp.replace(journal_path(out_root))


def read_journal(path: Path) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    doc = json.loads(path.read_text(encoding="utf-8"))
    if doc.get("schema") != JOURNAL_SCHEMA:
        raise ValueError(f"{path} is not an agent-eval journal")
    return dict(doc.get("meta") or {}), list(doc.get("runs") or [])


def assemble_payload(meta: dict[str, Any], runs: list[dict[str, Any]]) -> dict[str, Any]:
    """Build the report from the journal's meta and runs.

    The same function serves a completed matrix and a report rebuilt after a
    cancellation, so a partial report is not a second, weaker format.
    """
    build = meta["build"]
    repeat = int(meta.get("repeat") or 1)
    arms = list(meta.get("arms") or [ARM_OBERTH])
    compare_pair = tuple(meta.get("compare_arms") or (ARM_OBERTH, ARM_NATIVE))
    summary = summarize_runs(runs, build["sha256"])
    comparison = compare_arms(summary, *compare_pair)
    comparable = bool(summary) and all(row["comparable"] for row in summary)

    notes: list[str] = list(meta.get("extra_notes") or [])
    if repeat < 2:
        notes.append("repeat=1: rows are single observations, not a comparable repeat series")
    if repeat < MIN_INTERVAL_RUNS:
        notes.append(
            f"repeat={repeat}: below {MIN_INTERVAL_RUNS} the report gives point estimates only; "
            "completion intervals and min/max ranges stay empty"
        )
    if repeat < P95_MIN_RUNS:
        notes.append(
            f"repeat={repeat}: steps_p95 is emitted as a real observation but flagged unreliable; "
            f"a tail estimate needs {P95_MIN_RUNS}+ valid runs (at n={repeat} it equals the maximum)"
        )
    if not comparable and repeat >= 2:
        notes.append("not every row is comparable; check harness/agent errors and the build sha")
    if any(row["step_source"] and "fallback" in " ".join(row["step_source"]) for row in summary):
        notes.append("at least one row fell back to shim command counts because provider events were not recognized")
    if not meta.get("skill_present") and any(arm_uses_skill(arm) for arm in arms):
        notes.append("no skill checkout found; skill-arm rows would not be comparable to a pinned skill")
    if ARM_NATIVE in arms:
        notes.append(
            "native rows are the control: no skill and an oberth shim that exits 127; "
            "compare_arms only pairs rows with the same scenario, provider and binary"
        )
    if ARM_OBERTH_NOSKILL in arms:
        notes.append(
            f"{ARM_OBERTH_NOSKILL} rows keep the product and the safety policy but drop the skill; "
            f"pass --compare {ARM_OBERTH},{ARM_OBERTH_NOSKILL} to read the skill's contribution"
        )
    if meta.get("require_complete") and ARM_NATIVE in arms:
        notes.append("require-complete scores the control too; run it with --arms oberth when asserting product completion")

    return {
        "schema": REPORT_SCHEMA,
        "generated_at": utc_stamp(),
        "build": build,
        "skill": meta.get("skill") or {},
        "agents": meta.get("agents") or {},
        "arms": arms,
        "passed_env": meta.get("passed_env") or [],
        "agent_args": meta.get("agent_args") or {},
        "models": meta.get("models") or {},
        "repeat": repeat,
        "scenarios": meta.get("scenarios") or [],
        "scenario_prompts": meta.get("scenario_prompts") or {},
        "comparable": comparable,
        "runs": runs,
        "summary": summary,
        "compare_arms": list(compare_pair),
        "comparison": comparison,
        "notes": notes,
        "cold_start": _cold_start_budget(runs, meta.get("scenarios") or []),
    }


def plan_experiment(
    *,
    scenario_ids: list[str],
    agent_arm_pairs: list[tuple[str, str]],
    repeat: int,
    shard_minutes: float,
    per_run_seconds: float,
    max_parallel: int = 4,
) -> dict[str, Any]:
    """Split the work by the cost unit, not by agent.

    One agent's whole suite in one job is what turned a healthy 40-run matrix
    into a three-hour timeout. The plan groups scenarios so each job fits a
    wall-clock target; the workflow then runs those groups as a matrix.
    """
    overhead_s = 30.0
    per_run_s = max(1.0, float(per_run_seconds)) + overhead_s
    shards: list[dict[str, Any]] = []
    for agent_id in sorted({agent for agent, _ in agent_arm_pairs}):
        arms = [arm for agent, arm in agent_arm_pairs if agent == agent_id]
        per_scenario_s = len(arms) * repeat * per_run_s
        per_shard = (
            max(1, int(float(shard_minutes) * 60 // per_scenario_s))
            if shard_minutes > 0 else len(scenario_ids)
        )
        group: list[str] = []
        index = 0
        for scenario_id in scenario_ids:
            group.append(scenario_id)
            if len(group) >= per_shard:
                index += 1
                shards.append(_shard(agent_id, index, group, len(arms), repeat, per_run_s))
                group = []
        if group:
            index += 1
            shards.append(_shard(agent_id, index, group, len(arms), repeat, per_run_s))
    total_runs = sum(shard["runs"] for shard in shards)
    parallelism = max(1, int(max_parallel))
    # Divisible runner time is not wall clock: the number that matters for a
    # human waiting on the run is total run minutes divided by how many shards
    # execute at once. Round up because the last wave is usually partial.
    wall_minutes = round(total_runs * per_run_s / 60 / parallelism, 1)
    return {
        "total_runs": total_runs,
        "total_run_minutes": round(total_runs * per_run_s / 60, 1),
        "estimated_minutes": wall_minutes,
        "max_parallel": parallelism,
        "per_run_seconds": per_run_s,
        "shards": shards,
    }


def _shard(agent_id: str, index: int, group: list[str], arms: int, repeat: int, per_run_s: float) -> dict[str, Any]:
    runs = len(group) * arms * repeat
    return {
        "name": f"{agent_id}-{index}",
        "agent": agent_id,
        "scenarios": ",".join(group),
        "runs": runs,
        "estimated_minutes": round(runs * per_run_s / 60, 1),
        # The first shard of an agent proves the loop before the rest spend
        # tokens; a failed smoke stops that agent without touching its siblings.
        "smoke": index == 1,
    }


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(
        description="Fixed agent scenario matrix for option-berth.",
        formatter_class=argparse.RawDescriptionHelpFormatter,
        epilog=(
            "Provider autonomy flags are never added by this harness. Pass them explicitly, e.g.\n"
            "  --agent-arg claude=--permission-mode=bypassPermissions\n"
            "  --agent-arg codex=--dangerously-bypass-approvals-and-sandbox\n"
            "  --agent-arg opencode=--auto\n"
            "  --agent-arg pi=--approve\n"
            "Credentials are never discovered; name any variable a provider needs with --pass-env.\n"
            f"Common names: {', '.join(COMMON_PROVIDER_ENV)}"
        ),
    )
    parser.add_argument("--list", action="store_true", help="list scenarios and agents")
    parser.add_argument("--dry-run", action="store_true", help="provision every scenario and stop before any agent")
    parser.add_argument(
        "--smoke", action="store_true",
        help="run each named agent on one cheap scenario to prove its loop works (real path, minimal cost)",
    )
    parser.add_argument("--execute", action="store_true", help="run agents and write the matrix")
    parser.add_argument("--merge", action="append", default=[], metavar="REPORT_JSON",
                        help="merge shard report.json files into one matrix (repeatable)")
    parser.add_argument("--expect-scenarios", default="",
                        help="comma list of scenarios the experiment was supposed to cover; merge reports coverage")
    parser.add_argument("--require-coverage", action="store_true",
                        help="with --merge: exit 1 when a requested scenario is missing")
    parser.add_argument("--plan", action="store_true",
                        help="print the shard matrix and wall-clock estimate; run no agent")
    parser.add_argument("--shard-minutes", type=float, default=60.0,
                        help="target wall-clock minutes per shard for --plan (default: 60)")
    parser.add_argument("--per-run-seconds", type=float, default=240.0,
                        help="measured seconds per agent run for --plan's estimate (default: 240)")
    parser.add_argument("--budget-minutes", type=float, default=240.0,
                        help="--plan exits 1 when its wall-clock estimate exceeds this (0 disables; default: 240)")
    parser.add_argument("--max-parallel", type=int, default=4,
                        help="--plan: how many shards run at once, for the wall-clock estimate (default: 4)")
    parser.add_argument("--report-only", default=None, metavar="DIR_OR_JOURNAL",
                        help="rebuild report.json/report.md from a journal left by a cancelled run")
    parser.add_argument("--resume", default=None, metavar="JOURNAL_JSON",
                        help="reuse completed runs from an earlier journal instead of repeating them")
    parser.add_argument("--max-consecutive-failures", type=int, default=3,
                        help="abort the shard after this many consecutive agent/harness failures (0 disables; default: 3)")
    parser.add_argument("--agents", default="", help="comma list of agents, or all (claude,codex,opencode,pi)")
    parser.add_argument("--scenarios", default="all", help="comma list of scenario ids, or all")
    parser.add_argument("--suite", default="all", choices=["all", *SUITES],
                        help="scenario suite: core failure paths, large ten-service stacks, "
                             "cold no-manifest starts, or all")
    parser.add_argument("--arms", default=ARM_OBERTH,
                        help="comma list of comparison arms: oberth,oberth-noskill,native (default: oberth)")
    parser.add_argument("--compare", default=f"{ARM_OBERTH},{ARM_NATIVE}", metavar="TREATED,CONTROL",
                        help="arm pair for the comparison table (default: oberth,native); "
                             f"use oberth,{ARM_OBERTH_NOSKILL} to isolate the skill")
    parser.add_argument("--repeat", type=int, default=1, help="runs per scenario/agent (>=2 makes a row comparable)")
    parser.add_argument("--binary", default=str(ROOT / "bin" / "oberth"), help="oberth binary under test")
    parser.add_argument("--skill-dir", default=None, help="option-berth-skill checkout")
    parser.add_argument("--out", default=None, help="report directory (default .agent-eval/<timestamp>)")
    parser.add_argument("--timeout", type=float, default=600, help="seconds per agent run")
    parser.add_argument("--keep", action="store_true", help="keep each run's isolated environment")
    parser.add_argument("--skip-preflight", action="store_true", help="skip the listener observation preflight")
    parser.add_argument("--pass-env", action="append", default=[], metavar="NAME", help="forward this environment variable to the run")
    parser.add_argument("--agent-arg", action="append", default=[], metavar="AGENT=ARG", help="extra provider argv element")
    parser.add_argument("--model", action="append", default=[], metavar="AGENT=MODEL",
                        help="record the provider's model in the report; merge refuses mismatches")
    parser.add_argument("--require-complete", action="store_true", help="exit 1 unless every valid run completed with no OOB")
    parser.add_argument("--require-comparable", action="store_true", help="exit 1 unless every row has repeat>=2 on one build")
    args = parser.parse_args(argv)

    compare_pair = parse_compare(args.compare)

    if args.merge:
        stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
        out_root = Path(args.out).expanduser().resolve() if args.out else ROOT / ".agent-eval" / stamp
        expected = (
            parse_list(args.expect_scenarios, list(SCENARIOS), "scenario")
            if args.expect_scenarios.strip() else None
        )
        return merge_reports(
            args.merge, out_root,
            require_comparable=args.require_comparable, compare=compare_pair,
            expect_scenarios=expected, require_coverage=args.require_coverage,
        )

    if args.report_only:
        target = Path(args.report_only).expanduser().resolve()
        journal = target if target.name == "journal.json" else target / "journal.json"
        try:
            meta, runs = read_journal(journal)
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            parser.error(f"--report-only: {exc}")
        out_dir = target.parent if target.name == "journal.json" else target
        payload = assemble_payload(meta, runs)
        json_path, md_path = write_report(out_dir, payload)
        print(f"wrote {json_path}\nwrote {md_path}", file=sys.stderr)
        return 0

    if args.list:
        for suite in SUITES:
            print(f"{suite} scenarios:")
            for scenario in SCENARIOS.values():
                if scenario.suite == suite:
                    print(f"  {scenario.id:28} {scenario.title}: {scenario.description}")
        print("arms:")
        for arm, description in zip(ARMS, ARM_DESCRIPTIONS):
            print(f"  {arm:28} {description}")
        print("agents:")
        for agent_id, spec in AGENTS.items():
            available = "installed" if (not spec.command or shutil.which(spec.command)) else "not found"
            print(f"  {agent_id:28} {spec.label} ({available}, skill={spec.skill_injection})")
        return 0

    modes = sum(bool(value) for value in (args.dry_run, args.execute, args.smoke, args.plan))
    if modes != 1:
        parser.error("choose exactly one of --dry-run, --smoke, --plan or --execute (or use --list)")
    if args.smoke and not args.agents.strip():
        parser.error("--smoke needs --agents: the point is to prove the agents you are about to run")
    if args.plan and not args.agents.strip():
        parser.error("--plan needs --agents: the shard matrix is per provider")

    if args.repeat < 1:
        parser.error("--repeat must be at least 1")
    if args.require_comparable and args.repeat < 2:
        parser.error("--require-comparable needs --repeat 2 or more")

    if args.scenarios.strip() == "all":
        scenario_ids = [
            scenario_id for scenario_id, scenario in SCENARIOS.items()
            if args.suite in ("all", scenario.suite)
        ]
    else:
        scenario_ids = parse_list(args.scenarios, list(SCENARIOS), "scenario")
        if args.suite != "all":
            scenario_ids = [sid for sid in scenario_ids if SCENARIOS[sid].suite == args.suite]
            if not scenario_ids:
                parser.error(f"no scenario in suite {args.suite!r} among the selected ids")
    if not scenario_ids:
        parser.error(f"no scenarios in suite {args.suite!r}")

    arm_ids = parse_list(args.arms, list(ARMS), "arm")
    all_agents = [name for name in AGENTS if name != "reference"]
    if args.agents.strip() == "all":
        agent_ids = all_agents
    elif args.agents.strip():
        agent_ids = parse_list(args.agents, list(AGENTS), "agent")
    else:
        agent_ids = []
    if args.execute and not agent_ids:
        parser.error("--execute needs --agents")
    # The reference script drives oberth itself with the skill staged, so it is
    # the `oberth` arm's self-check only; its other pairings would measure the
    # harness, not a model.
    agent_arm_pairs = [
        (agent_id, arm)
        for agent_id in agent_ids
        for arm in arm_ids
        if not (agent_id == "reference" and arm != ARM_OBERTH)
    ]
    if args.execute and not agent_arm_pairs:
        parser.error("the selected agents and arms produce no runs; the reference script is oberth-arm only")

    extras: dict[str, list[str]] = {}
    for item in args.agent_arg:
        if "=" not in item:
            parser.error("--agent-arg must be AGENT=ARG")
        agent_id, value = item.split("=", 1)
        if agent_id not in AGENTS:
            parser.error(f"unknown agent in --agent-arg: {agent_id}")
        extras.setdefault(agent_id, []).append(value)

    models: dict[str, str] = {}
    for item in args.model:
        if "=" not in item:
            parser.error("--model must be AGENT=MODEL")
        agent_id, value = item.split("=", 1)
        if agent_id not in AGENTS:
            parser.error(f"unknown agent in --model: {agent_id}")
        if not value.strip():
            parser.error(f"--model {agent_id}= needs a non-empty model")
        models[agent_id] = value.strip()

    for name in args.pass_env:
        if not name.isidentifier():
            parser.error(f"--pass-env expects an environment variable name, got {name!r}")

    if args.plan:
        plan = plan_experiment(
            scenario_ids=scenario_ids,
            agent_arm_pairs=agent_arm_pairs,
            repeat=args.repeat,
            shard_minutes=args.shard_minutes,
            per_run_seconds=args.per_run_seconds,
            max_parallel=args.max_parallel,
        )
        print(json.dumps(plan, indent=2))
        for shard in plan["shards"]:
            print(
                f"shard {shard['name']}: {shard['scenarios']} "
                f"({shard['runs']} run(s), ~{shard['estimated_minutes']} min)",
                file=sys.stderr,
            )
        print(
            f"plan: {plan['total_runs']} run(s), {plan['total_run_minutes']} runner-min, "
            f"~{plan['estimated_minutes']} min wall clock at {plan['max_parallel']} parallel shard(s) "
            f"(at {plan['per_run_seconds']:g} s per run including setup)",
            file=sys.stderr,
        )
        if args.budget_minutes and plan["estimated_minutes"] > args.budget_minutes:
            print(
                f"plan: estimated {plan['estimated_minutes']} min exceeds --budget-minutes "
                f"{args.budget_minutes:g}; split the scenarios or lower --repeat",
                file=sys.stderr,
            )
            return 1
        return 0

    binary = Path(args.binary).expanduser().resolve()
    build = build_identity(binary)
    skill = find_skill_dir(args.skill_dir)
    skill_info = skill_identity(skill)
    stamp = time.strftime("%Y%m%dT%H%M%SZ", time.gmtime())
    out_root = Path(args.out).expanduser().resolve() if args.out else ROOT / ".agent-eval" / stamp
    out_root.mkdir(parents=True, exist_ok=True)

    print(f"oberth {build.get('version')} ({build.get('commit')}) sha256={build['sha256'][:12]}", file=sys.stderr)
    if build.get("version") == "dev" or build.get("commit") in (None, "unknown"):
        print("note: this binary has no release identity; build with `mage buildEngine` for a comparable report", file=sys.stderr)
    print(f"skill: {skill_info['path']} ({skill_info['version']} @ {skill_info['commit']})", file=sys.stderr)

    if not args.skip_preflight:
        if any(SCENARIOS[scenario_id].suite == "large" for scenario_id in scenario_ids):
            print("preflight: checking Docker for the complex benchmark...", file=sys.stderr)
            try:
                require_docker()
                ensure_image()
            except (PreflightError, RuntimeError) as exc:
                print(f"preflight failed:\n{exc}", file=sys.stderr)
                return 2
        print("preflight: verifying that oberth can observe listeners...", file=sys.stderr)
        try:
            preflight(binary, out_root, args.pass_env)
        except PreflightError as exc:
            print(f"preflight failed:\n{exc}", file=sys.stderr)
            return 2
        print("preflight: ok", file=sys.stderr)

    if args.dry_run:
        return dry_run(scenario_ids, out_root=out_root, binary=binary, args=args)

    if args.smoke:
        return smoke(
            agent_ids, out_root=out_root, binary=binary, skill=skill, extras=extras, args=args,
        )

    meta: dict[str, Any] = {
        "build": build,
        "skill": skill_info,
        "agents": {agent_id: AGENTS[agent_id].version() for agent_id in agent_ids},
        "arms": arm_ids,
        "passed_env": sorted(args.pass_env),
        "agent_args": extras,
        "models": models,
        "repeat": args.repeat,
        "scenarios": scenario_ids,
        "scenario_prompts": {scenario_id: SCENARIOS[scenario_id].prompt for scenario_id in scenario_ids},
        "compare_arms": list(compare_pair),
        "require_complete": args.require_complete,
        "skill_present": skill is not None,
        "extra_notes": [],
    }
    requested_keys = {
        (scenario_id, agent_id, arm, repeat)
        for scenario_id in scenario_ids
        for agent_id, arm in agent_arm_pairs
        for repeat in range(1, args.repeat + 1)
    }

    runs: list[dict[str, Any]] = []
    resume_keys: set[tuple[str, str, str, int]] = set()
    if args.resume:
        resume_path = Path(args.resume).expanduser().resolve()
        try:
            resume_meta, resume_runs = read_journal(resume_path)
        except (OSError, ValueError, json.JSONDecodeError) as exc:
            print(f"resume: cannot read {resume_path}: {exc}", file=sys.stderr)
            return 1
        if (resume_meta.get("build") or {}).get("sha256") != build["sha256"]:
            print("resume: the journal was produced by a different binary; refusing to mix builds", file=sys.stderr)
            return 1
        for run in resume_runs:
            key = (run.get("scenario"), run.get("agent"), run.get("arm"), run.get("repeat"))
            if key in requested_keys and run.get("status") != "harness_error":
                runs.append(run)
                resume_keys.add(key)
        meta["extra_notes"].append(f"resumed {len(runs)} completed run(s) from {resume_path}")

    out_root.mkdir(parents=True, exist_ok=True)
    write_journal(out_root, meta, runs)

    consecutive = 0
    aborted = False
    for scenario_id in scenario_ids:
        scenario = SCENARIOS[scenario_id]
        for agent_id, arm in agent_arm_pairs:
            spec = AGENTS[agent_id]
            if agent_id != "reference" and not shutil.which(spec.command):
                for repeat in range(1, args.repeat + 1):
                    if (scenario_id, agent_id, arm, repeat) in resume_keys:
                        continue
                    runs.append({
                        "scenario": scenario_id, "agent": agent_id, "arm": arm, "repeat": repeat,
                        "status": "harness_error", "completed": False, "steps": None,
                        "steps_source": "unavailable", "command_steps": 0, "oberth_calls": 0,
                        "oberth_output_bytes": 0, "process_probe_calls": 0,
                        "agent_output_bytes": 0, "mis_stops": 0,
                        "cross_worktree_incidents": 0, "score": {},
                        "oob": [], "oob_count": 0,
                        "checks": [], "failed_checks": [],
                        "notes": [f"{spec.command} not found"], "duration_s": 0.0,
                        "build_sha": build["sha256"], "agent_version": None,
                        "exit_code": None, "timed_out": False, "ports": {}, "artifacts": {},
                    })
                write_journal(out_root, meta, runs)
                continue
            for repeat in range(1, args.repeat + 1):
                key = (scenario_id, agent_id, arm, repeat)
                if key in resume_keys:
                    print(f"skip {scenario_id} / {agent_id} / {arm} / {repeat}: resumed", file=sys.stderr)
                    continue
                print(f"run {scenario_id} / {agent_id} / {arm} / {repeat}...", file=sys.stderr)
                record = execute_run(
                    scenario, agent_id, spec, repeat, arm=arm,
                    out_root=out_root, binary=binary, skill=skill, args=args,
                    build=build, extras=extras.get(agent_id, []),
                )
                runs.append(record)
                # The journal is the evidence a cancelled job leaves behind:
                # write it before anything that can fail.
                write_journal(out_root, meta, runs)
                print(
                    f"  {record['status']}: completed={record['completed']} "
                    f"steps={record['steps']} probes={record['process_probe_calls']} "
                    f"oob={record['oob_count']}",
                    file=sys.stderr,
                )
                if record["status"] in ("agent_error", "harness_error"):
                    consecutive += 1
                else:
                    consecutive = 0
                if args.max_consecutive_failures > 0 and consecutive >= args.max_consecutive_failures:
                    print(
                        f"aborting shard: {consecutive} consecutive agent/harness failures; "
                        "the remaining runs would be evidence about the provider, not the product",
                        file=sys.stderr,
                    )
                    aborted = True
                    break
            if aborted:
                break
        if aborted:
            break

    if aborted:
        meta["extra_notes"].append(
            f"aborted after {consecutive} consecutive agent/harness failures; this report is partial"
        )
        write_journal(out_root, meta, runs)

    payload = assemble_payload(meta, runs)
    json_path, md_path = write_report(out_root, payload)
    print(f"wrote {json_path}\nwrote {md_path}", file=sys.stderr)

    if aborted:
        return 1
    if args.require_complete:
        bad = [
            row for row in payload["summary"]
            if row["valid_runs"] == 0 or row["completion_rate"] != 1 or row["oob_total"] != 0
        ]
        if bad:
            print(f"require-complete: {len(bad)} row(s) failed", file=sys.stderr)
            return 1
    if args.require_comparable and not payload["comparable"]:
        print("require-comparable: the matrix is not comparable", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
