"""Unit tests for the website's measured-results snapshot. No network, no run."""

from __future__ import annotations

import json
import sys
import tempfile
import unittest
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import export_site_eval as export  # noqa: E402


def run(scenario: str, arm: str, repeat: int, steps: int, *,
        completed: bool = True, duration: float = 10.0, oob: int = 0) -> dict:
    return {
        "scenario": scenario, "agent": "codex", "arm": arm, "repeat": repeat,
        "steps": steps, "completed": completed, "duration_s": duration,
        "oob_count": oob, "status": "ok" if completed else "not_completed",
    }


def report() -> dict:
    return {
        "schema": "oberth.agent-eval.report/v1",
        "generated_at": "2026-11-04T08:34:50Z",
        "agents": {"codex": "codex-cli 0.166.0"},
        # The workflow records every provider's model; the snapshot uses the
        # one agent that actually ran.
        "models": {"codex": "gpt-6-luna", "claude": "qwen-3.8-flash"},
        "arms": ["oberth", "native"],
        "repeat": 2,
        "runs": [
            run("s1", "oberth", 1, 4), run("s1", "oberth", 2, 6),
            run("s1", "native", 1, 8), run("s1", "native", 2, 10, completed=False),
            run("s2", "oberth", 1, 5), run("s2", "oberth", 2, 7),
            run("s2", "native", 1, 9), run("s2", "native", 2, 11),
        ],
    }


class SnapshotTests(unittest.TestCase):
    def test_snapshot_names_one_agent_and_its_model(self) -> None:
        snapshot = export.build_snapshot(report(), run_url="https://github.com/o/r/actions/runs/1", suite="large")
        self.assertEqual(snapshot["schema"], export.SCHEMA)
        self.assertEqual(snapshot["agent"]["id"], "codex")
        self.assertEqual(snapshot["model"], "gpt-6-luna")
        self.assertEqual(snapshot["runs"], 8)
        self.assertEqual(snapshot["scenarios"], 2)
        self.assertEqual(snapshot["metrics"]["oberth"]["completed"], 4)
        self.assertEqual(snapshot["metrics"]["native"]["completed"], 3)
        self.assertEqual(snapshot["scenarios_with_fewer_turns"], 2)
        self.assertEqual(snapshot["comparable_scenarios"], 2)

    def test_the_model_must_belong_to_the_agent_that_ran(self) -> None:
        payload = report()
        payload["models"] = {"claude": "qwen-3.8-flash"}
        with self.assertRaises(export.ExportError):
            export.build_snapshot(payload, run_url="https://github.com/o/r/actions/runs/1", suite="large")

    def test_more_than_one_agent_is_refused(self) -> None:
        payload = report()
        payload["agents"] = {"codex": "1", "claude": "2"}
        with self.assertRaises(export.ExportError):
            export.build_snapshot(payload, run_url="https://github.com/o/r/actions/runs/1", suite="large")

    def test_missing_arm_is_refused(self) -> None:
        payload = report()
        payload["runs"] = [r for r in payload["runs"] if r["arm"] != "native"]
        with self.assertRaises(export.ExportError):
            export.build_snapshot(payload, run_url="https://github.com/o/r/actions/runs/1", suite="large")

    def test_single_repeat_is_refused(self) -> None:
        payload = report()
        payload["repeat"] = 1
        with self.assertRaises(export.ExportError):
            export.build_snapshot(payload, run_url="https://github.com/o/r/actions/runs/1", suite="large")

    def test_cli_writes_the_snapshot_and_refuses_a_bad_report(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "report.json"
            source.write_text(json.dumps(report()), encoding="utf-8")
            out = root / "eval.json"
            code = export.main([
                str(source), "--out", str(out),
                "--run-url", "https://github.com/o/r/actions/runs/1", "--suite", "large",
            ])
            self.assertEqual(code, 0)
            self.assertEqual(json.loads(out.read_text(encoding="utf-8"))["runs"], 8)

            broken = root / "broken.json"
            broken.write_text("{}", encoding="utf-8")
            code = export.main([
                str(broken), "--out", str(out),
                "--run-url", "https://github.com/o/r/actions/runs/1",
            ])
            self.assertEqual(code, 1)


class SiteBuildSnapshotTests(unittest.TestCase):
    def load_build(self):
        import importlib.util
        path = Path(__file__).resolve().parent.parent / "site" / "build.py"
        spec = importlib.util.spec_from_file_location("site_build_for_test", path)
        module = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(module)
        return module

    def snapshot(self) -> dict:
        return export.build_snapshot(
            report(), run_url="https://github.com/o/r/actions/runs/1", suite="large"
        )

    def test_build_turns_the_snapshot_into_display_values(self) -> None:
        build = self.load_build()
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "eval.json"
            path.write_text(json.dumps(self.snapshot()), encoding="utf-8")
            values = build.snapshot_values(build.load_snapshot(path))
        self.assertEqual(values["proof_completion_with"], "4/4")
        self.assertEqual(values["proof_completion_without"], "3/4")
        self.assertEqual(values["proof_rate_without"], "75%")
        self.assertEqual(values["proof_run_count"], "8")
        self.assertEqual(values["proof_scenario_count"], "2")
        self.assertEqual(values["proof_comparable_scenarios"], "2")
        self.assertEqual(values["proof_measured_date"], "2026-11-04")
        self.assertTrue(values["proof_run_url"].startswith("https://github.com/"))

    def test_build_refuses_a_snapshot_without_a_run_url(self) -> None:
        build = self.load_build()
        payload = self.snapshot()
        payload["run_url"] = ""
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / "eval.json"
            path.write_text(json.dumps(payload), encoding="utf-8")
            with self.assertRaises(ValueError):
                build.load_snapshot(path)


if __name__ == "__main__":
    unittest.main()
