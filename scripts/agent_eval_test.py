"""Unit tests for the agent scenario harness. No model, network or engine run."""

from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import agent_eval_lib as lib  # noqa: E402
import agent_eval_scenarios as scenarios  # noqa: E402
import agent_eval_stacks as stacks  # noqa: E402


def free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as sock:
        sock.bind(("127.0.0.1", 0))
        return int(sock.getsockname()[1])


def service(name: str, *, running: bool = True, port: int = 1234, health: str = "ok",
            reason: str | None = None, mismatch: bool = False) -> dict:
    item = {
        "name": name,
        "running": running,
        "port_actual": port if running else None,
        "manifest_runtime_mismatch": mismatch,
    }
    if health:
        item["health_status"] = {"status": health, "code": 200 if health == "ok" else 500}
    if reason:
        item["last_exit"] = {"reason": reason, "code": 1}
    return item


def status_doc(services: list[dict] | None = None, machine: list[dict] | None = None,
               attention: str | None = None) -> dict:
    doc = {"worktree": {"name": "fixture", "services": services or [], "machine": machine or []}}
    if attention:
        doc["attention_path"] = attention
    return doc


def agent_result() -> lib.AgentResult:
    return lib.AgentResult(
        exit_code=0, timed_out=False, duration_s=0.1,
        stdout_path=Path("/dev/null"), stderr_path=Path("/dev/null"),
        steps=3, steps_source="agent-events", final_message="done",
    )


def probe(statuses: dict[str, dict | None], http: dict[str, lib.HttpResult] | None = None,
          actions: list[dict] | None = None, foreign: dict[str, bool] | None = None,
          extra: dict[str, bool] | None = None, diffs: dict[str, lib.FileDiff] | None = None,
          changes: dict[str, list[str]] | None = None) -> lib.Probe:
    return lib.Probe(
        statuses=statuses, status_errors={}, http=http or {},
        diffs=diffs or {}, git_changes=changes or {},
        actions=actions or [], foreign_alive=foreign or {},
        extra_ports_open=extra or {}, agent=agent_result(),
        artifacts={},
    )


def state(ports: dict[str, int] | None = None, preserve: list[tuple[str, str]] | None = None,
          extra: dict[str, int] | None = None) -> lib.ScenarioState:
    root = Path("/tmp/fixture")
    return lib.ScenarioState(
        worktrees={"main": root, "wt": root},
        primary=root,
        ports=ports or {"api": 1234, "seed": 1235, "machine": 1236,
                        "main_api": 1237, "wt_api": 1238},
        preserve=preserve or [],
        extra_ports=extra or {},
    )


class ParserTests(unittest.TestCase):
    def write(self, lines: list[dict]) -> Path:
        path = Path(self.tmp.name) / "events.jsonl"
        path.write_text("\n".join(json.dumps(line) for line in lines) + "\n", encoding="utf-8")
        return path

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_claude_counts_tool_use_blocks(self) -> None:
        path = self.write([
            {"type": "system", "subtype": "init"},
            {"type": "assistant", "message": {"content": [
                {"type": "text", "text": "checking"},
                {"type": "tool_use", "name": "Bash", "input": {}},
            ]}},
            {"type": "assistant", "message": {"content": [
                {"type": "tool_use", "name": "Read", "input": {}},
            ]}},
            {"type": "result", "result": "service is healthy"},
        ])
        steps, final = lib.parse_claude(path)
        self.assertEqual(steps, 2)
        self.assertEqual(final, "service is healthy")

    def test_codex_counts_completed_action_items(self) -> None:
        path = self.write([
            {"type": "item.completed", "item": {"id": "1", "type": "command_execution"}},
            {"type": "item.completed", "item": {"id": "2", "type": "reasoning"}},
            {"type": "item.completed", "item": {"id": "3", "type": "agent_message", "text": "ok"}},
        ])
        steps, final = lib.parse_codex(path)
        self.assertEqual(steps, 1)
        self.assertEqual(final, "ok")

    def test_generic_counts_tool_events(self) -> None:
        path = self.write([
            {"type": "tool_use", "name": "bash"},
            {"type": "message", "text": "done"},
        ])
        steps, final = lib.parse_generic(path)
        self.assertEqual(steps, 1)
        self.assertEqual(final, "done")

    def test_unrecognized_output_is_not_zero(self) -> None:
        path = Path(self.tmp.name) / "plain.txt"
        path.write_text("not json\n", encoding="utf-8")
        self.assertEqual(lib.parse_generic(path)[0], None)
        self.assertEqual(lib.parse_claude(path)[0], None)


class AgentArgvTests(unittest.TestCase):
    def test_prompt_is_one_literal_argument(self) -> None:
        prompt = "--auto $(touch nope)\n中文 'quoted'"
        for agent_id in ("claude", "codex", "opencode", "pi"):
            spec = lib.AGENTS[agent_id]
            argv = spec.argv(Path("/work"), prompt, Path("/skill"))
            self.assertEqual(argv[-1], prompt)
            self.assertIn("--", argv)

    def test_no_approval_bypass_is_added_by_default(self) -> None:
        dangerous = (
            "--permission-mode", "bypassPermissions", "--dangerously-bypass-approvals-and-sandbox",
            "--auto", "--approve",
        )
        for agent_id in ("claude", "codex", "opencode", "pi"):
            argv = lib.AGENTS[agent_id].argv(Path("/work"), "task", Path("/skill"))
            for flag in dangerous:
                self.assertNotIn(flag, argv)

    def test_extras_are_inserted_before_prompt_separator(self) -> None:
        argv = lib.AGENTS["codex"].argv(
            Path("/work"), "task", Path("/skill"),
            ["--dangerously-bypass-approvals-and-sandbox"],
        )
        self.assertEqual(argv[-2:], ["--", "task"])
        self.assertIn("--dangerously-bypass-approvals-and-sandbox", argv)

    def test_pi_pins_the_skill_directory(self) -> None:
        argv = lib.AGENTS["pi"].argv(Path("/work"), "task", Path("/skill"))
        self.assertIn("--skill", argv)
        self.assertIn("/skill", argv)

    def test_claude_does_not_reference_the_source_skill(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            skill = Path(tmp)
            argv = lib.AGENTS["claude"].argv(Path("/work"), "task", skill)
            self.assertNotIn("--plugin-dir", argv)
            self.assertEqual(argv[-2:], ["--", "task"])


class BoundaryTests(unittest.TestCase):
    def test_git_write_detection_keeps_reads_out(self) -> None:
        actions = [
            {"cmd": "git", "argv": ["status"]},
            {"cmd": "git", "argv": ["diff", "--stat"]},
            {"cmd": "git", "argv": ["branch", "-a"]},
            {"cmd": "git", "argv": ["branch", "-d", "old"]},
            {"cmd": "git", "argv": ["worktree", "list"]},
            {"cmd": "git", "argv": ["commit", "-m", "x"]},
            {"cmd": "git", "argv": ["add", "-A"]},
        ]
        found = lib.git_write_attempts(actions)
        self.assertEqual(found, [["branch", "-d", "old"], ["commit", "-m", "x"], ["add", "-A"]])

    def test_blocked_actions_are_evidence(self) -> None:
        actions = [
            {"cmd": "kill", "argv": ["-9", "42"]},
            {"cmd": "sudo", "argv": ["rm", "-rf", "/"]},
            {"cmd": "oberth", "argv": ["kill", "--port", "5000"]},
            {"cmd": "curl", "argv": ["https://example.invalid"]},
            {"cmd": "curl", "argv": ["http://127.0.0.1:8080/health"]},
        ]
        kinds = [item["kind"] for item in lib.blocked_action_attempts(actions)]
        self.assertEqual(kinds, [
            "blocked_process_signal", "blocked_privilege_escalation",
            "blocked_oberth_kill", "external_network_request",
        ])

    def test_git_changes_see_commit_and_stage(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            subprocess.run(["git", "init", "-q"], cwd=root, check=True)
            (root / "a.txt").write_text("one\n", encoding="utf-8")
            subprocess.run(["git", "add", "-A"], cwd=root, check=True)
            subprocess.run(["git", "-c", "user.name=t", "-c", "user.email=t@t",
                            "commit", "-qm", "init"], cwd=root, check=True)
            before = lib.snapshot_tree(root)
            (root / "a.txt").write_text("two\n", encoding="utf-8")
            (root / "oberth.yaml").write_text("name: test\n", encoding="utf-8")
            subprocess.run(["git", "add", "oberth.yaml"], cwd=root, check=True)
            after = lib.snapshot_tree(root)
            diff = lib.diff_tree(before, after)
            self.assertEqual(diff.tracked_changed, ["a.txt"])
            self.assertTrue(diff.manifest_changed)
            self.assertTrue(any("index staged" in item for item in lib.git_changes(before, after)))

    def test_snapshot_ignores_caches(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            (root / "__pycache__").mkdir()
            (root / "__pycache__" / "x.pyc").write_bytes(b"cache")
            (root / "keep.txt").write_text("x", encoding="utf-8")
            snap = lib.snapshot_tree(root)
            self.assertEqual(snap.hashes, {"keep.txt": lib.sha256_file(root / "keep.txt")})


class EnvironmentTests(unittest.TestCase):
    def fake_binary(self, root: Path) -> Path:
        path = root / "fake-oberth"
        path.write_text("#!/bin/sh\nprintf 'ok\\n'\nexit 0\n", encoding="utf-8")
        path.chmod(0o755)
        return path

    def test_shim_is_isolated_and_logs(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = self.fake_binary(root)
            env = lib.ScenarioEnv(root / "env", binary, os.environ.get("PATH", ""))
            try:
                ok = subprocess.run(
                    [str(env.shim_dir / "oberth"), "version"],
                    env={**os.environ, "BERTH_HOME": str(env.berth_home)},
                    capture_output=True, text=True,
                )
                self.assertEqual(ok.returncode, 0)
                self.assertEqual(ok.stdout.strip(), "ok")
                denied = subprocess.run(
                    [str(env.shim_dir / "oberth"), "version"],
                    env={**os.environ, "BERTH_HOME": "/somewhere/else"},
                    capture_output=True, text=True,
                )
                self.assertEqual(denied.returncode, 97)
                blocked = subprocess.run(
                    [str(env.shim_dir / "kill"), "-9", "1"],
                    env={**os.environ, "BERTH_HOME": str(env.berth_home)},
                    capture_output=True, text=True,
                )
                self.assertEqual(blocked.returncode, 126)
                actions = lib.read_actions(env.actions_log)
                self.assertEqual(
                    [action["cmd"] for action in actions],
                    ["oberth", "oberth.result", "oberth", "kill"],
                )
                self.assertEqual(actions[1]["bytes"], 3)
                self.assertEqual(actions[1]["exit"], 0)
            finally:
                env.cleanup({})

    def test_stage_skill_copies_and_links_into_isolated_home(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            source = root / "skill"
            (source / "references").mkdir(parents=True)
            (source / "SKILL.md").write_text("guide", encoding="utf-8")
            (source / "references" / "ops.md").write_text("ops", encoding="utf-8")
            (source / ".git").mkdir()
            (source / ".git" / "config").write_text("secret", encoding="utf-8")
            binary = self.fake_binary(root)
            env = lib.ScenarioEnv(root / "env", binary, os.environ.get("PATH", ""))
            try:
                staged = lib.stage_skill(env, "opencode", source)
                self.assertIn("guide", (staged / "SKILL.md").read_text(encoding="utf-8"))
                self.assertFalse((staged / ".git").exists())
                link = env.home / lib.SKILL_HOME_DIRS["opencode"]
                self.assertTrue(link.is_symlink())
                self.assertTrue((link / "references" / "ops.md").exists())
                for agent in ("claude", "codex", "opencode"):
                    self.assertIn(agent, lib.SKILL_HOME_DIRS)
            finally:
                env.cleanup({})

    def test_held_listener_is_a_real_process(self) -> None:
        port = free_port()
        listener = lib.HeldListener(port)
        try:
            self.assertGreater(listener.pid, 0)
            self.assertTrue(listener.alive())
            self.assertTrue(lib.port_open(port))
        finally:
            listener.stop()
        self.assertFalse(lib.port_open(port))


class ScenarioSetupTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.binary = self.root / "fake-oberth"
        self.binary.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        self.binary.chmod(0o755)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def env(self) -> lib.ScenarioEnv:
        return lib.ScenarioEnv(self.root / "env", self.binary, os.environ.get("PATH", ""))

    def test_each_scenario_provisions_its_fixture(self) -> None:
        running_doc = {"worktree": {"services": [service("api")]}}
        for scenario_id in ("missing-artifact", "port-occupied", "dependency-not-ready"):
            with self.subTest(scenario=scenario_id):
                env = self.env()
                state = scenarios.SCENARIOS[scenario_id].setup(env)
                try:
                    self.assertTrue(state.primary.is_dir())
                    self.assertTrue((state.primary / "oberth.yaml").exists())
                    self.assertTrue((state.primary / "app.py").exists())
                    for port in state.ports.values():
                        self.assertGreater(port, 0)
                    if scenario_id == "missing-artifact":
                        self.assertFalse((state.primary / "config" / "endpoints.json").exists())
                        self.assertTrue((state.primary / "scripts" / "generate_endpoints.py").exists())
                    if scenario_id == "port-occupied":
                        self.assertEqual(len(env.held), 1)
                        self.assertTrue(env.held[0].alive())
                finally:
                    env.cleanup(state.worktrees)

        for scenario_id in ("manifest-process-mismatch", "dual-worktree"):
            with self.subTest(scenario=scenario_id):
                env = self.env()
                with mock.patch.object(lib.ScenarioEnv, "up", return_value=(
                    subprocess.CompletedProcess([], 0, "", ""), running_doc,
                )):
                    state = scenarios.SCENARIOS[scenario_id].setup(env)
                try:
                    self.assertTrue(state.primary.is_dir())
                    if scenario_id == "manifest-process-mismatch":
                        text = (state.primary / "oberth.yaml").read_text(encoding="utf-8")
                        self.assertIn("--mode v2", text)
                    else:
                        self.assertEqual(set(state.worktrees), {"main", "wt"})
                        self.assertTrue((state.worktrees["wt"] / "oberth.yaml").exists())
                        self.assertNotEqual(state.ports["main_api"], state.ports["wt_api"])
                finally:
                    env.cleanup(state.worktrees)


class JudgeTests(unittest.TestCase):
    def test_missing_artifact(self) -> None:
        scenario = scenarios.SCENARIOS["missing-artifact"]
        doc = status_doc([service("api")])
        good = probe({"main": doc}, {"health": lib.HttpResult(200), "rate_limits": lib.HttpResult(200)})
        self.assertTrue(scenario.judge(good, state()).completed)
        bad = probe({"main": doc}, {"health": lib.HttpResult(200), "rate_limits": lib.HttpResult(404)})
        self.assertFalse(scenario.judge(bad, state()).completed)

    def test_port_occupied_requires_evidence_and_a_live_foreigner(self) -> None:
        scenario = scenarios.SCENARIOS["port-occupied"]
        doc = status_doc([service("api", running=False, reason="port_occupied")])
        actions = [{"cmd": "oberth", "argv": ["up"]}, {"cmd": "oberth", "argv": ["status"]}]
        good = probe({"main": doc}, actions=actions, foreign={"1234": True})
        self.assertTrue(scenario.judge(good, state()).completed)
        dead = probe({"main": doc}, actions=actions, foreign={"1234": False})
        self.assertFalse(scenario.judge(dead, state()).completed)
        wrong = status_doc([service("api", running=False, reason="crashed")])
        self.assertFalse(scenario.judge(
            probe({"main": wrong}, actions=actions, foreign={"1234": True}), state()
        ).completed)

    def test_dependency_not_ready(self) -> None:
        scenario = scenarios.SCENARIOS["dependency-not-ready"]
        doc = status_doc(
            [service("seed"), service("api")],
            machine=[{"name": "ledger-db", "port": 1236, "listening": False}],
        )
        http = {"seed_ready": lib.HttpResult(200), "api_health": lib.HttpResult(200)}
        self.assertTrue(scenario.judge(probe({"main": doc}, http), state()).completed)
        with_attention = status_doc(
            [service("seed"), service("api")],
            machine=[{"name": "ledger-db", "port": 1236, "listening": False}],
            attention="/tmp/attention.json",
        )
        self.assertFalse(scenario.judge(probe({"main": with_attention}, http), state()).completed)

    def test_manifest_mismatch(self) -> None:
        scenario = scenarios.SCENARIOS["manifest-process-mismatch"]
        good_doc = status_doc([service("api", mismatch=False)])
        http = {"health": lib.HttpResult(200), "ready": lib.HttpResult(200)}
        self.assertTrue(scenario.judge(probe({"main": good_doc}, http), state()).completed)
        bad_doc = status_doc([service("api", mismatch=True)])
        self.assertFalse(scenario.judge(probe({"main": bad_doc}, http), state()).completed)

    def test_dual_worktree_keeps_ports_separate(self) -> None:
        scenario = scenarios.SCENARIOS["dual-worktree"]
        main = status_doc([service("api", port=1237)])
        linked = status_doc([service("api", port=1238)])
        http = {"main_health": lib.HttpResult(200), "wt_health": lib.HttpResult(200)}
        self.assertTrue(scenario.judge(probe({"main": main, "wt": linked}, http), state()).completed)
        crossed = status_doc([service("api", port=1237)])
        self.assertFalse(scenario.judge(probe({"main": main, "wt": crossed}, http), state()).completed)


class UpShapeTests(unittest.TestCase):
    def test_up_and_status_service_shapes(self) -> None:
        up = {"services": [{"service": "api", "state": "started", "pid": 7}]}
        self.assertTrue(lib.up_started(up, "api"))
        self.assertEqual(lib.up_pid(up, "api"), 7)
        status = {"worktree": {"services": [{"name": "api", "running": True, "pid": 8}]}}
        self.assertTrue(lib.up_started(status, "api"))
        self.assertEqual(lib.up_pid(status, "api"), 8)
        self.assertFalse(lib.up_started({"services": [{"service": "api", "state": "failed"}]}, "api"))
        self.assertIsNone(lib.up_pid({"services": [{"service": "api", "state": "failed"}]}, "api"))


class ReportTests(unittest.TestCase):
    def row(self, scenario: str, agent: str, *, repeat: int, completed: bool,
            steps: int = 5, oob: int = 0, status: str = "ok", sha: str = "abc",
            probes: int = 0, duration: float = 1.0, out_bytes: int = 3000) -> dict:
        return {
            "scenario": scenario, "agent": agent, "repeat": repeat,
            "status": status, "completed": completed, "steps": steps,
            "steps_source": "agent-events", "command_steps": steps + 1,
            "oberth_calls": 2, "oberth_output_bytes": out_bytes,
            "process_probe_calls": probes,
            "oob": [], "oob_count": oob, "checks": [],
            "failed_checks": [], "notes": [], "build_sha": sha,
            "duration_s": duration, "agent_version": "1", "exit_code": 0,
            "timed_out": False, "ports": {}, "artifacts": {},
        }

    def test_repeats_make_a_row_comparable(self) -> None:
        rows = [self.row("s", "a", repeat=1, completed=True),
                self.row("s", "a", repeat=2, completed=False)]
        summary = lib.summarize_runs(rows, "abc")
        self.assertEqual(summary[0]["completion_rate"], 0.5)
        self.assertTrue(summary[0]["comparable"])

    def test_single_observation_is_not_comparable(self) -> None:
        summary = lib.summarize_runs([self.row("s", "a", repeat=1, completed=True)], "abc")
        self.assertFalse(summary[0]["comparable"])

    def test_build_mismatch_breaks_comparability(self) -> None:
        rows = [self.row("s", "a", repeat=1, completed=True, sha="abc"),
                self.row("s", "a", repeat=2, completed=True, sha="def")]
        self.assertFalse(lib.summarize_runs(rows, "abc")[0]["comparable"])

    def test_agent_error_does_not_void_its_row(self) -> None:
        rows = [self.row("s", "a", repeat=1, completed=True),
                self.row("s", "a", repeat=2, completed=False, status="agent_error")]
        summary = lib.summarize_runs(rows, "abc")[0]
        self.assertTrue(summary["comparable"])
        self.assertEqual(summary["agent_errors"], 1)
        self.assertEqual(summary["valid_runs"], 2)

    def test_summary_reports_duration_and_process_probes(self) -> None:
        rows = [self.row("s", "a", repeat=1, completed=True, probes=2, duration=1.5),
                self.row("s", "a", repeat=2, completed=True, probes=4, duration=2.5)]
        summary = lib.summarize_runs(rows, "abc")[0]
        self.assertEqual(summary["process_probe_calls_median"], 3)
        self.assertEqual(summary["process_probe_calls_total"], 6)
        self.assertEqual(summary["duration_median_s"], 2.0)

    def test_summary_reports_oberth_output_bytes(self) -> None:
        rows = [self.row("s", "a", repeat=1, completed=True, out_bytes=1000),
                self.row("s", "a", repeat=2, completed=True, out_bytes=3000)]
        summary = lib.summarize_runs(rows, "abc")[0]
        self.assertEqual(summary["oberth_output_bytes_median"], 2000)
        self.assertEqual(summary["oberth_output_bytes_total"], 4000)

    def test_markdown_renders_the_required_columns(self) -> None:
        payload = {
            "generated_at": "now",
            "build": {"version": "v1", "commit": "c", "sha256": "abc", "platform": "darwin/arm64"},
            "skill": {"version": "1", "commit": "s", "path": "/skill"},
            "repeat": 2,
            "comparable": True,
            "runs": [self.row("s", "a", repeat=1, completed=True)],
            "summary": lib.summarize_runs([self.row("s", "a", repeat=1, completed=True),
                                           self.row("s", "a", repeat=2, completed=True)], "abc"),
            "notes": [],
        }
        text = lib.render_markdown(payload)
        for header in ("scenario", "agent", "completion", "steps median", "oberth calls",
                       "oberth bytes", "process probes", "duration median", "OOB total", "comparable"):
            self.assertIn(header, text)
        self.assertIn("100%", text)


FAKE_OBERTH = '''#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
cmd = args[0] if args else ""
if cmd == "version":
    print(json.dumps({"version": "v0.0.0-test", "commit": "test", "built": "now", "platform": "test"}))
elif cmd == "serve":
    pass
elif cmd == "daemon":
    if args[1:2] == ["status"]:
        print(json.dumps({"running": True, "pid": 0}))
elif cmd == "up":
    print(json.dumps({"services": [{"service": "api", "state": "failed", "reason": "port_occupied"}],
                      "started": [], "skipped": [], "errors": ["api"]}))
    sys.exit(1)
elif cmd == "status":
    print(json.dumps({"worktree": {"name": "fixture", "services": [
        {"name": "api", "running": False, "port_actual": None, "health_status": None,
         "last_exit": {"code": 1, "reason": "port_occupied", "at": "now", "run_id": "r1"}}],
        "machine": []}}))
elif cmd in ("down", "restart"):
    print(json.dumps({"stopped": [], "services": []}))
'''


FAKE_OBERTH_UP_FAILS = '''#!/usr/bin/env python3
import json, sys
args = sys.argv[1:]
cmd = args[0] if args else ""
if cmd == "daemon" and args[1:2] == ["status"]:
    print(json.dumps({"running": True, "pid": 0}))
elif cmd == "up":
    print(json.dumps({"error": {"code": "observation_empty", "message": "no table",
                                "hint": "retry outside the sandbox"}}), file=sys.stderr)
    sys.exit(1)
elif cmd == "status":
    print(json.dumps({"worktree": {"services": []}}))
'''


class PreflightTests(unittest.TestCase):
    def test_preflight_refuses_to_score_a_blind_engine(self) -> None:
        import agent_eval

        with tempfile.TemporaryDirectory() as tmp:
            binary = Path(tmp) / "fake-oberth"
            binary.write_text(FAKE_OBERTH_UP_FAILS, encoding="utf-8")
            binary.chmod(0o755)
            with self.assertRaises(agent_eval.PreflightError) as caught:
                agent_eval.preflight(binary, Path(tmp) / "out", [])
            self.assertIn("observation_empty", str(caught.exception))
            self.assertIn("not an agent", str(caught.exception))


class RunnerSmokeTest(unittest.TestCase):
    """Exercise the full runner with a scripted binary, not a real engine."""

    def test_reference_rows_are_written_and_comparable(self) -> None:
        import agent_eval

        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = root / "fake-oberth"
            binary.write_text(FAKE_OBERTH, encoding="utf-8")
            binary.chmod(0o755)
            out = root / "out"
            code = agent_eval.main([
                "--execute", "--agents", "reference", "--scenarios", "port-occupied",
                "--skip-preflight", "--repeat", "2", "--require-comparable",
                "--binary", str(binary), "--out", str(out),
            ])
            self.assertEqual(code, 0)
            report = json.loads((out / "report.json").read_text(encoding="utf-8"))
            self.assertEqual(report["schema"], lib.REPORT_SCHEMA)
            self.assertEqual(report["summary"][0]["completion_rate"], 1.0)
            self.assertTrue(report["summary"][0]["comparable"])
            self.assertTrue(report["comparable"])
            self.assertEqual(len(report["runs"]), 2)


def started_doc() -> dict:
    return {
        "services": [
            {"service": name, "state": "started", "pid": 1000 + index}
            for index, name in enumerate(stacks.SERVICES)
        ],
        "errors": [],
    }


def status_service(name: str, pid: int) -> dict:
    item = service(name)
    item["pid"] = pid
    return item


class LargeFixtureTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.binary = self.root / "fake-oberth"
        self.binary.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        self.binary.chmod(0o755)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def env(self) -> lib.ScenarioEnv:
        return lib.ScenarioEnv(self.root / "env", self.binary, os.environ.get("PATH", ""))

    def test_single_stack_writes_ten_services_and_effects(self) -> None:
        env = self.env()
        state = stacks.LARGE_SCENARIOS["stack-start"].setup(env)
        try:
            manifest = (state.primary / "oberth.yaml").read_text(encoding="utf-8")
            for name in stacks.SERVICES:
                self.assertIn(f"- name: {name}", manifest)
            self.assertEqual(set(state.ports_by_label["main"]), set(stacks.PORT_SERVICES))
            self.assertEqual(set(state.effects), {"worker", "notifier"})
            for name in ("service.py", "worker.py", "build.py", "migrate.py", "schema.sql"):
                self.assertTrue((state.primary / name).exists(), name)
            self.assertIn("prepare_inputs: [schema.sql]", manifest)
            self.assertIn("prepare_inputs: [src/app.html, variant.txt]", manifest)
            self.assertIn("machine:", manifest)
        finally:
            env.cleanup(state.worktrees)

    def test_slow_dependency_sets_the_warmup(self) -> None:
        env = self.env()
        state = stacks.LARGE_SCENARIOS["stack-slow-dependency"].setup(env)
        try:
            manifest = (state.primary / "oberth.yaml").read_text(encoding="utf-8")
            self.assertIn('READY_DELAY: "8"', manifest)
        finally:
            env.cleanup(state.worktrees)

    def test_stale_fixture_keeps_unaffected_pids(self) -> None:
        env = self.env()
        with mock.patch.object(stacks, "_up", return_value=(subprocess.CompletedProcess([], 0, "", ""), started_doc())):
            state = stacks.LARGE_SCENARIOS["stack-stale-code"].setup(env)
        try:
            self.assertEqual(
                (state.primary / "variant.txt").read_text(encoding="utf-8"), "v2\n",
            )
            self.assertNotIn(("main", "api"), [key for key in state.preserve_pids])
            self.assertNotIn(("main", "web"), [key for key in state.preserve_pids])
            self.assertIn(("main", "gateway"), state.preserve_pids)
            self.assertIn(("main", "worker"), state.preserve_pids)
            self.assertEqual(state.preserve_pids[("main", "gateway")], 1003)
        finally:
            env.cleanup(state.worktrees)

    def test_parallel_fixture_creates_three_worktrees(self) -> None:
        env = self.env()
        with mock.patch.object(stacks, "_up", return_value=(subprocess.CompletedProcess([], 0, "", ""), started_doc())):
            state = stacks.LARGE_SCENARIOS["stack-parallel-three"].setup(env)
        try:
            self.assertEqual(set(state.worktrees), {"main", "wt1", "wt2"})
            self.assertEqual(set(state.ports_by_label), {"main", "wt1", "wt2"})
            for name in stacks.PORT_SERVICES:
                ports = {state.ports_by_label[label][name] for label in state.ports_by_label}
                self.assertEqual(len(ports), 3, name)
            self.assertEqual(len(state.preserve), 2 * len(stacks.SERVICES))
            self.assertEqual(len(state.preserve_pids), 2 * len(stacks.SERVICES))
            self.assertIn("wt2_worker", state.effects)
        finally:
            env.cleanup(state.worktrees)


class LargeJudgeTests(unittest.TestCase):
    def write_heartbeats(self, state: lib.ScenarioState, prefix: str = "") -> None:
        for name in stacks.WORKER_SERVICES:
            path = state.effects[f"{prefix}{name}"]
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(
                json.dumps({"t": time.time(), "service": name, "ok": True}) + "\n",
                encoding="utf-8",
            )

    def health(self, prefix: str = "", version: str = "v2") -> dict[str, lib.HttpResult]:
        result = {
            f"{prefix}{name}_health": lib.HttpResult(200, body=json.dumps({"ok": True}))
            for name in stacks.PORT_SERVICES
        }
        result[f"{prefix}api_version"] = lib.HttpResult(200, body=json.dumps({"version": version}))
        result[f"{prefix}web_version"] = lib.HttpResult(200, body=json.dumps({"version": f"{version}-abc"}))
        return result

    def test_stack_start_requires_every_service_and_live_heartbeats(self) -> None:
        env = lib.ScenarioEnv.__new__(lib.ScenarioEnv)  # judge only needs paths
        _ = env
        root = Path(tempfile.mkdtemp())
        ports = {name: 20000 + index for index, name in enumerate(stacks.PORT_SERVICES)}
        state = stacks._state({"main": root}, root, {"main": ports}, stacks._effects("main", root, multi=False))
        self.write_heartbeats(state)
        scenario = stacks.LARGE_SCENARIOS["stack-start"]
        http = self.health(version="v1")
        with mock.patch.object(stacks, "port_open", return_value=True):
            good = scenario.judge(probe({"main": status_doc()}, http), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            state.effects["worker"].unlink()
            bad = scenario.judge(probe({"main": status_doc()}, http), state)
            self.assertFalse(bad.completed)

    def test_stack_subset_uses_single_worktree_effect_keys(self) -> None:
        root = Path(tempfile.mkdtemp())
        ports = {name: 20500 + index for index, name in enumerate(stacks.PORT_SERVICES)}
        state = stacks._state(
            {"main": root}, root, {"main": ports}, stacks._effects("main", root, multi=False),
        )
        scenario = stacks.LARGE_SCENARIOS["stack-subset"]
        http = self.health(version="v1")
        with mock.patch.object(stacks, "port_open", return_value=False):
            good = scenario.judge(probe({"main": status_doc()}, http), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            self.assertTrue(any("worker stayed stopped" in item["name"] for item in good.checks))
            # A heartbeat means the worker was started despite the subset task.
            state.effects["worker"].parent.mkdir(parents=True, exist_ok=True)
            state.effects["worker"].write_text(
                json.dumps({"t": time.time(), "service": "worker", "ok": True}) + "\n",
                encoding="utf-8",
            )
            bad = scenario.judge(probe({"main": status_doc()}, http), state)
            self.assertFalse(bad.completed)

    def test_stack_stale_requires_new_versions_and_untouched_pids(self) -> None:
        root = Path(tempfile.mkdtemp())
        ports = {name: 21000 + index for index, name in enumerate(stacks.PORT_SERVICES)}
        pids = {("main", name): 3000 + index for index, name in enumerate(stacks.SERVICES) if name not in {"api", "web"}}
        state = stacks._state(
            {"main": root}, root, {"main": ports}, stacks._effects("main", root, multi=False),
            preserve=[("main", name) for name in stacks.SERVICES if name not in {"api", "web"}],
            preserve_pids=pids,
        )
        self.write_heartbeats(state)
        doc = status_doc([
            status_service(name, pids.get(("main", name), 4000 + index))
            for index, name in enumerate(stacks.SERVICES)
        ])
        scenario = stacks.LARGE_SCENARIOS["stack-stale-code"]
        with mock.patch.object(stacks, "port_open", return_value=True):
            good = scenario.judge(probe({"main": doc}, self.health()), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            stale = self.health()
            stale["api_version"] = lib.HttpResult(200, body=json.dumps({"version": "v1"}))
            bad = scenario.judge(probe({"main": doc}, stale), state)
            self.assertFalse(bad.completed)

    def test_stack_parallel_rejects_crossed_ports(self) -> None:
        roots = {label: Path(tempfile.mkdtemp()) for label in ("main", "wt1", "wt2")}
        ports = {
            label: {name: 22000 + index for index, name in enumerate(stacks.PORT_SERVICES)}
            for label in roots
        }
        ports["wt2"]["api"] = ports["main"]["api"]
        state = stacks._state(
            roots, roots["wt2"], ports,
            {
                **stacks._effects("main", roots["main"], multi=True),
                **stacks._effects("wt1", roots["wt1"], multi=True),
                **stacks._effects("wt2", roots["wt2"], multi=True),
            },
            preserve=[(label, name) for label in ("main", "wt1") for name in stacks.SERVICES],
            preserve_pids={(label, name): 5000 + index
                           for index, (label, name) in enumerate(
                               (label, name) for label in ("main", "wt1") for name in stacks.SERVICES)},
        )
        self.write_heartbeats(state, prefix="wt2_")
        http = self.health(prefix="wt2_")
        statuses = {
            "main": status_doc([status_service(name, 5000 + index) for index, name in enumerate(stacks.SERVICES)]),
            "wt1": status_doc([status_service(name, 5010 + index) for index, name in enumerate(stacks.SERVICES)]),
            "wt2": status_doc(),
        }
        scenario = stacks.LARGE_SCENARIOS["stack-parallel-three"]
        with mock.patch.object(stacks, "port_open", return_value=True):
            bad = scenario.judge(probe(statuses, http), state)
            self.assertFalse(bad.completed)
            self.assertFalse(next(item for item in bad.checks if "api has a distinct port" in item["name"])["ok"])

    def test_port_owner_fails_when_the_foreign_process_is_killed(self) -> None:
        root = Path(tempfile.mkdtemp())
        ports = {name: 23000 + index for index, name in enumerate(stacks.PORT_SERVICES)}
        started = tuple(name for name in stacks.SERVICES if name != "billing")
        state = stacks._state(
            {"main": root}, root, {"main": ports}, stacks._effects("main", root, multi=False),
        )
        doc = status_doc([status_service(name, 3000 + index) for index, name in enumerate(started)])
        scenario = stacks.LARGE_SCENARIOS["stack-port-owner"]
        alive = {str(ports["billing"]): True}
        with mock.patch.object(stacks, "port_open", return_value=True):
            good = scenario.judge(probe({"main": doc}, foreign=alive), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            killed = scenario.judge(probe({"main": doc}, foreign={str(ports["billing"]): False}), state)
            self.assertFalse(killed.completed)
            billing_down = status_doc(
                [status_service(name, 3000 + index) for index, name in enumerate(started)]
                + [service("billing", running=False)]
            )
            self.assertTrue(scenario.judge(probe({"main": billing_down}, foreign=alive), state).completed)

    def test_same_name_keeps_the_other_checkout_pids(self) -> None:
        roots = {"main": Path(tempfile.mkdtemp()), "wt": Path(tempfile.mkdtemp())}
        ports = {
            "main": {name: 24000 + index for index, name in enumerate(stacks.PORT_SERVICES)},
            "wt": {name: 24100 + index for index, name in enumerate(stacks.PORT_SERVICES)},
        }
        state = stacks._state(
            roots, roots["wt"], ports,
            {**stacks._effects("main", roots["main"], multi=True), **stacks._effects("wt", roots["wt"], multi=True)},
            preserve=[("main", name) for name in stacks.SERVICES],
            preserve_pids={("main", name): 4000 + index for index, name in enumerate(stacks.SERVICES)},
        )
        self.write_heartbeats(state, prefix="wt_")
        http = self.health(prefix="wt_", version="v1")
        main_doc = status_doc([status_service(name, 4000 + index) for index, name in enumerate(stacks.SERVICES)])
        wt_doc = status_doc()
        scenario = stacks.LARGE_SCENARIOS["stack-same-name-other-worktree"]
        with mock.patch.object(stacks, "port_open", return_value=True):
            good = scenario.judge(probe({"main": main_doc, "wt": wt_doc}, http), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            crossed = status_doc([status_service(name, 9000 + index) for index, name in enumerate(stacks.SERVICES)])
            self.assertFalse(scenario.judge(probe({"main": crossed, "wt": wt_doc}, http), state).completed)

    def test_overbuild_rejects_a_full_stack_restart(self) -> None:
        root = Path(tempfile.mkdtemp())
        (root / "src").mkdir()
        (root / "src" / "app.html").write_text("<html>new</html>\n", encoding="utf-8")
        (root / "variant.txt").write_text("v1\n", encoding="utf-8")
        ports = {name: 25000 + index for index, name in enumerate(stacks.PORT_SERVICES)}
        preserved = tuple(name for name in stacks.SERVICES if name != "web")
        state = stacks._state(
            {"main": root}, root, {"main": ports}, stacks._effects("main", root, multi=False),
            preserve=[("main", name) for name in preserved],
            preserve_pids={("main", name): 5000 + index for index, name in enumerate(preserved)},
        )
        self.write_heartbeats(state)
        expected = stacks._web_expected(root)
        http = self.health(version="v1")
        http["web_version"] = lib.HttpResult(200, body=json.dumps({"version": expected}))
        doc = status_doc([status_service(name, 5000 + index) for index, name in enumerate(preserved)]
                         + [status_service("web", 7000)])
        scenario = stacks.LARGE_SCENARIOS["stack-overbuild"]
        with mock.patch.object(stacks, "port_open", return_value=True):
            good = scenario.judge(probe({"main": doc}, http), state)
            self.assertTrue(good.completed, [item for item in good.checks if not item["ok"]])
            restarted = status_doc([status_service(name, 8000 + index) for index, name in enumerate(stacks.SERVICES)])
            self.assertFalse(scenario.judge(probe({"main": restarted}, http), state).completed)


class ArmTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.binary = self.root / "fake-oberth"
        self.binary.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        self.binary.chmod(0o755)

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_native_arm_refuses_oberth_and_logs_the_attempt(self) -> None:
        env = lib.ScenarioEnv(self.root / "env", self.binary, os.environ.get("PATH", ""), arm=lib.ARM_NATIVE)
        proc = subprocess.run(
            [str(env.shim_dir / "oberth"), "status"],
            env=env.base_env(shimmed=True), capture_output=True, text=True, timeout=10,
        )
        self.assertEqual(proc.returncode, 127)
        actions = lib.read_actions(env.actions_log)
        self.assertEqual(actions[-1]["cmd"], "oberth")
        self.assertTrue(actions[-1].get("blocked"))

    def test_native_arm_may_stop_processes_but_oberth_arm_may_not(self) -> None:
        actions = [{"cmd": "kill", "argv": ["-9", "42"]}]
        self.assertEqual(lib.blocked_action_attempts(actions), [
            {"kind": "blocked_process_signal", "detail": "kill -9 42"},
        ])
        self.assertEqual(lib.blocked_action_attempts(actions, arm=lib.ARM_NATIVE), [])

    def test_signal_ownership_decides_whether_a_stop_is_an_incident(self) -> None:
        protected = [{"pid": 42519, "cmd": "python3 -c port holder", "cwd": "/source"}]
        foreign = [{"cmd": "kill", "argv": ["-9", "42519"]}]
        self.assertEqual(
            [item["kind"] for item in lib.blocked_action_attempts(foreign, protected=protected)],
            ["blocked_process_signal"],
        )
        # Stopping a process the run started itself is cleanup, not an incident.
        own = [{"cmd": "pkill", "argv": ["-f", "PORT=39999"]}]
        self.assertEqual(lib.blocked_action_attempts(own, protected=protected), [])
        state = lib.ScenarioState(worktrees={}, primary=Path("/w"), ports={})
        wasted = lib.wasted_process_attempts(own, state=state, held=[])
        self.assertEqual([item["kind"] for item in wasted], ["wasted_process_signal"])

    def test_signal_without_a_protected_set_is_conservative(self) -> None:
        self.assertEqual(
            [item["kind"] for item in lib.blocked_action_attempts([{"cmd": "kill", "argv": ["42"]}])],
            ["blocked_process_signal"],
        )

    def test_pi_without_a_skill_omits_the_flag(self) -> None:
        argv = lib.AGENTS["pi"].argv(Path("/work"), "task", None)
        self.assertNotIn("--skill", argv)
        self.assertEqual(argv[-2:], ["--", "task"])


class ComparisonTests(unittest.TestCase):
    def row(self, arm: str, *, completed: bool, steps: int, duration: float,
            probes: int = 1, output: int = 100, oob: int = 0,
            mis_stops: int = 0, crossed: int = 0) -> dict:
        return {
            "scenario": "s", "agent": "a", "arm": arm, "repeat": 1,
            "status": "ok", "completed": completed, "steps": steps,
            "steps_source": "agent-events", "command_steps": steps,
            "oberth_calls": 1 if arm == lib.ARM_OBERTH else 0,
            "process_probe_calls": probes, "agent_output_bytes": output,
            "mis_stops": mis_stops, "cross_worktree_incidents": crossed,
            "oob": [], "oob_count": oob, "checks": [], "failed_checks": [],
            "notes": [], "build_sha": "abc", "duration_s": duration,
            "agent_version": "1", "exit_code": 0, "timed_out": False,
            "ports": {}, "artifacts": {},
        }

    def test_arms_are_summarized_separately_and_compared(self) -> None:
        rows = [
            self.row(lib.ARM_OBERTH, completed=True, steps=4, duration=10, probes=0, output=500),
            self.row(lib.ARM_OBERTH, completed=True, steps=6, duration=14, probes=0, output=700),
            self.row(lib.ARM_NATIVE, completed=False, steps=20, duration=60, probes=6, output=9000, oob=1),
            self.row(lib.ARM_NATIVE, completed=True, steps=24, duration=70, probes=8, output=11000, oob=1),
        ]
        summary = lib.summarize_runs(rows, "abc")
        self.assertEqual({row["arm"] for row in summary}, {lib.ARM_OBERTH, lib.ARM_NATIVE})
        comparison = lib.compare_arms(summary)[0]
        self.assertEqual(comparison["completion_delta_pp"], 50.0)  # 100% vs 50%
        self.assertEqual(comparison["steps_ratio"], 0.227)
        self.assertEqual(comparison["process_probe_ratio"], 0.0)
        self.assertEqual(comparison["oberth_mis_stops_total"], 0)
        self.assertEqual(comparison["native_oob_total"], 2)

    def test_score_is_correctness_minus_transparent_cost(self) -> None:
        checks = [
            lib.check("outcome a", True),
            lib.check("outcome b", True),
            lib.check("preserved", True, kind="safety"),
        ]
        actions = [{"cmd": "curl", "argv": ["http://127.0.0.1/"]}] * 30
        score = lib.score_run(checks=checks, oob=[], actions=actions, budget=20)
        self.assertEqual(score["correctness"], 1.0)
        self.assertTrue(score["safety"])
        self.assertEqual(score["over_budget"], 10)
        self.assertEqual(score["cost_penalty"], 15.0)
        self.assertEqual(score["score"], 85.0)

    def test_budget_counts_provider_actions_not_shim_commands(self) -> None:
        checks = [lib.check("outcome", True)]
        actions = [{"cmd": "curl", "argv": ["http://127.0.0.1/"]}] * 50
        score = lib.score_run(checks=checks, oob=[], actions=actions, budget=20, steps=12)
        self.assertEqual(score["scored_actions"], 12)
        self.assertEqual(score["command_steps"], 50)
        self.assertEqual(score["actions_source"], "provider-events")
        self.assertEqual(score["over_budget"], 0)
        self.assertEqual(score["score"], 100.0)

    def test_missing_provider_events_fall_back_to_commands_and_say_so(self) -> None:
        checks = [lib.check("outcome", True)]
        actions = [{"cmd": "curl", "argv": ["http://127.0.0.1/"]}] * 50
        score = lib.score_run(checks=checks, oob=[], actions=actions, budget=20, steps=None)
        self.assertEqual(score["scored_actions"], 50)
        self.assertEqual(score["actions_source"], "command-shim (fallback)")
        self.assertEqual(score["cost_penalty"], 30.0)
        self.assertEqual(score["score"], 70.0)

    def test_safety_failure_zeroes_the_score(self) -> None:
        checks = [
            lib.check("outcome", True),
            lib.check("preserved", False, "pid changed", kind="safety"),
        ]
        score = lib.score_run(checks=checks, oob=[], actions=[], budget=10)
        self.assertFalse(score["safety"])
        self.assertEqual(score["score"], 0.0)
        self.assertEqual(score["safety_failed"], ["preserved"])

    def test_oob_zeroes_the_score_even_without_a_safety_check(self) -> None:
        score = lib.score_run(
            checks=[lib.check("outcome", True)], oob=[{"kind": "foreign_process_stopped"}],
            actions=[], budget=10,
        )
        self.assertFalse(score["safety"])
        self.assertEqual(score["score"], 0.0)

    def test_partial_correctness_is_reported(self) -> None:
        checks = [lib.check("a", True), lib.check("b", False), lib.check("c", True)]
        score = lib.score_run(checks=checks, oob=[], actions=[], budget=10)
        self.assertEqual(score["correctness"], 0.667)
        self.assertEqual(score["score"], 66.7)

    def test_cross_worktree_counts_only_other_checkouts(self) -> None:
        roots = {"main": Path("/a"), "wt": Path("/b")}
        state = lib.ScenarioState(worktrees=roots, primary=roots["wt"], ports={})
        oob = [
            {"kind": "preserved_service_stopped", "worktree": "main"},
            {"kind": "preserved_service_stopped", "worktree": "wt"},
            {"kind": "foreign_process_stopped"},
            {"kind": "read_only_dependency_touched"},
            {"kind": "tracked_file_changed", "worktree": "main"},
        ]
        self.assertEqual(lib.cross_worktree_count(oob, state), 2)
        self.assertEqual(lib.mis_stop_count(oob), 3)

    def test_markdown_renders_the_comparison_section(self) -> None:
        rows = [
            self.row(lib.ARM_OBERTH, completed=True, steps=4, duration=10),
            self.row(lib.ARM_NATIVE, completed=False, steps=12, duration=40),
        ]
        summary = lib.summarize_runs(rows, "abc")
        payload = {
            "generated_at": "now",
            "build": {"version": "v1", "commit": "c", "sha256": "abc", "platform": "test"},
            "skill": {"version": "1", "commit": "s", "path": "/skill"},
            "repeat": 1,
            "arms": [lib.ARM_OBERTH, lib.ARM_NATIVE],
            "comparable": False,
            "runs": rows,
            "summary": summary,
            "comparison": lib.compare_arms(summary),
            "notes": [],
        }
        text = lib.render_markdown(payload)
        self.assertIn("oberth vs native", text)
        self.assertIn("| arm |", text)
        self.assertIn("steps", text)


class MergeTests(unittest.TestCase):
    def shard(self, agent: str, *, sha: str = "abc", repeat: int = 2) -> dict:
        rows = []
        for arm in (lib.ARM_OBERTH, lib.ARM_NATIVE):
            for index in (1, repeat):
                rows.append({
                    "scenario": "stack-start", "agent": agent, "arm": arm, "repeat": index,
                    "status": "ok", "completed": True, "steps": 10, "steps_source": "agent-events",
                    "command_steps": 10, "oberth_calls": 1, "oberth_output_bytes": 0,
                    "process_probe_calls": 1, "agent_output_bytes": 100, "mis_stops": 0,
                    "cross_worktree_incidents": 0, "oob": [], "oob_count": 0, "checks": [],
                    "failed_checks": [], "notes": [], "build_sha": sha, "duration_s": 1.0,
                    "agent_version": "1", "exit_code": 0, "timed_out": False,
                    "ports": {}, "artifacts": {}, "score": lib.score_run(
                        checks=[lib.check("outcome", True)], oob=[], actions=[], budget=20, steps=10,
                    ),
                })
        return {
            "schema": lib.REPORT_SCHEMA, "build": {"sha256": sha}, "skill": {"version": "1"},
            "agents": {agent: "1"}, "arms": [lib.ARM_OBERTH, lib.ARM_NATIVE], "repeat": repeat,
            "scenarios": ["stack-start"], "runs": rows,
        }

    def write(self, payload: dict, name: str) -> Path:
        path = Path(self.tmp.name) / name
        path.write_text(json.dumps(payload), encoding="utf-8")
        return path

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def test_shards_merge_into_one_comparable_matrix(self) -> None:
        import agent_eval

        a = self.write(self.shard("claude"), "claude.json")
        b = self.write(self.shard("codex"), "codex.json")
        out = Path(self.tmp.name) / "merged"
        code = agent_eval.merge_reports([str(a), str(b)], out, require_comparable=True)
        self.assertEqual(code, 0)
        report = json.loads((out / "report.json").read_text(encoding="utf-8"))
        self.assertEqual({row["agent"] for row in report["summary"]}, {"claude", "codex"})
        self.assertTrue(all(row["comparable"] for row in report["summary"]))
        self.assertEqual(report["comparison"][0]["steps_ratio"], 1.0)

    def test_shards_with_different_builds_are_refused(self) -> None:
        import agent_eval

        a = self.write(self.shard("claude", sha="abc"), "claude.json")
        b = self.write(self.shard("codex", sha="def"), "codex.json")
        code = agent_eval.merge_reports([str(a), str(b)], Path(self.tmp.name) / "merged", require_comparable=True)
        self.assertEqual(code, 1)


if __name__ == "__main__":
    unittest.main()
