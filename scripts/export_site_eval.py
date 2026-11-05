#!/usr/bin/env python3
"""Turn one agent-eval merged report into the website's measured-results snapshot.

The website must not read CI internals. The eval pipeline is still moving, so
the public page reads a small, versioned snapshot that a person promotes from a
finished run. This script is the only adapter: when the report schema changes,
only this file and its test change; the site contract stays put.

    python3 scripts/export_site_eval.py report.json \
      --out site/data/eval.json \
      --run-url https://github.com/SheathedSharp/option-berth/actions/runs/123 \
      --suite large
"""
from __future__ import annotations

import argparse
import json
import sys
from datetime import datetime, timezone
from pathlib import Path
from typing import Any

SCHEMA = "oberth.site-eval/v1"
ARMS = ("oberth", "native")


class ExportError(RuntimeError):
    """The report cannot back an honest public snapshot."""


def _percentile(values: list[float | int], percentile: float) -> float | int | None:
    ordered = sorted(values)
    if not ordered:
        return None
    rank = (percentile / 100) * len(ordered)
    index = max(0, min(len(ordered) - 1, int(rank + 0.999999) - 1))
    return ordered[index]


def _median(values: list[float | int]) -> float | None:
    if not values:
        return None
    ordered = sorted(values)
    middle = len(ordered) // 2
    if len(ordered) % 2:
        return float(ordered[middle])
    return (float(ordered[middle - 1]) + float(ordered[middle])) / 2


def _one(values: dict[str, Any], label: str) -> tuple[str, Any]:
    if len(values) != 1:
        raise ExportError(
            f"expected exactly one {label} in the report, found {sorted(values)}; "
            "an experiment must name one agent and one model"
        )
    return next(iter(values.items()))


def build_snapshot(report: dict[str, Any], *, run_url: str, suite: str) -> dict[str, Any]:
    if not run_url.startswith("https://github.com/"):
        raise ExportError("--run-url must point at the GitHub Actions run that produced the report")
    arms = report.get("arms") or []
    if any(arm not in arms for arm in ARMS):
        raise ExportError(f"the report must contain both arms {ARMS}, got {arms}")
    repeat = int(report.get("repeat") or 0)
    if repeat < 2:
        raise ExportError("repeat must be at least 2 for a comparable snapshot")
    runs = report.get("runs") or []
    if not runs:
        raise ExportError("the report has no runs")

    by_arm: dict[str, list[dict[str, Any]]] = {arm: [] for arm in ARMS}
    for run in runs:
        arm = run.get("arm")
        if arm in by_arm:
            by_arm[arm].append(run)
    for arm in ARMS:
        if not by_arm[arm]:
            raise ExportError(f"no runs for arm {arm!r}")

    agent_id, agent_version = _one(report.get("agents") or {}, "agent")
    # The workflow records the model for every provider so that shards can be
    # merged without guessing; the snapshot needs the model of the one agent in
    # this report, not a single entry in the whole dict.
    models = report.get("models") or {}
    if agent_id not in models:
        raise ExportError(
            f"the report does not record a model for the agent it ran ({agent_id!r}); "
            f"recorded models: {sorted(models)}"
        )
    model = models[agent_id]

    scenarios = sorted({run["scenario"] for run in runs})
    if not scenarios:
        raise ExportError("the report names no scenario")

    metrics: dict[str, Any] = {}
    for arm in ARMS:
        arm_runs = by_arm[arm]
        steps = [run["steps"] for run in arm_runs if isinstance(run.get("steps"), int)]
        durations = [
            run["duration_s"] for run in arm_runs
            if isinstance(run.get("duration_s"), (int, float))
        ]
        completed = sum(1 for run in arm_runs if run.get("completed"))
        metrics[arm] = {
            "completed": completed,
            "total": len(arm_runs),
            "steps_p50": _percentile(steps, 50),
            "steps_p95": _percentile(steps, 95),
            "duration_p50_s": _median(durations),
            "oob_total": sum(int(run.get("oob_count") or 0) for run in arm_runs),
        }

    # A scenario counts only when both arms have a median; an arm with no
    # usable runs is not evidence of a difference.
    per_scenario: dict[str, dict[str, float]] = {}
    for scenario in scenarios:
        for arm in ARMS:
            steps = [
                run["steps"] for run in by_arm[arm]
                if run.get("scenario") == scenario and isinstance(run.get("steps"), int)
            ]
            median = _percentile(steps, 50)
            if median is not None:
                per_scenario.setdefault(scenario, {})[arm] = median
    fewer = sum(
        1 for medians in per_scenario.values()
        if len(medians) == 2 and medians["oberth"] < medians["native"]
    )
    comparable_scenarios = sum(1 for medians in per_scenario.values() if len(medians) == 2)

    measured_at = report.get("generated_at")
    if not measured_at:
        measured_at = datetime.now(timezone.utc).isoformat(timespec="seconds")
    try:
        datetime.fromisoformat(str(measured_at).replace("Z", "+00:00"))
    except ValueError as exc:
        raise ExportError(f"generated_at is not an ISO timestamp: {measured_at!r}") from exc

    return {
        "schema": SCHEMA,
        "measured_at": measured_at,
        "run_url": run_url,
        "suite": suite,
        "agent": {"id": agent_id, "version": agent_version},
        "model": model,
        "repeat": repeat,
        "runs": len(runs),
        "scenarios": len(scenarios),
        "arms": list(ARMS),
        "metrics": metrics,
        "scenarios_with_fewer_turns": fewer,
        "comparable_scenarios": comparable_scenarios,
    }


def load_report(path: Path) -> dict[str, Any]:
    try:
        return json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ExportError(f"cannot read {path}: {exc}") from exc


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("report", type=Path, help="a merged agent-eval report.json")
    parser.add_argument("--out", type=Path, default=Path("site/data/eval.json"))
    parser.add_argument("--run-url", required=True, help="the GitHub Actions run this report came from")
    parser.add_argument("--suite", default="large", help="suite name the report covers")
    args = parser.parse_args(argv)
    try:
        snapshot = build_snapshot(load_report(args.report), run_url=args.run_url, suite=args.suite)
    except ExportError as exc:
        print(f"export refused: {exc}", file=sys.stderr)
        return 1
    args.out.parent.mkdir(parents=True, exist_ok=True)
    args.out.write_text(json.dumps(snapshot, indent=2, ensure_ascii=False) + "\n", encoding="utf-8")
    print(f"wrote {args.out}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
