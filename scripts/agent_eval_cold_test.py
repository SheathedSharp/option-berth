"""Unit tests for the cold-start suite. No model, network or engine run."""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path
from unittest import mock

sys.path.insert(0, str(Path(__file__).resolve().parent))

import agent_eval as harness  # noqa: E402
import agent_eval_cold as cold  # noqa: E402
import agent_eval_lib as lib  # noqa: E402


def service(name: str, *, running: bool = True, port: int | None = 1234,
            health: str = "ok") -> dict:
    item = {
        "name": name,
        "running": running,
        "port_actual": port if running else None,
        "manifest_runtime_mismatch": False,
    }
    if health:
        item["health_status"] = {"status": health, "code": 200 if health == "ok" else 503}
    return item


def status_doc(services: list[dict]) -> dict:
    return {"worktree": {"name": "fixture", "services": services, "machine": []}}


def agent_result() -> lib.AgentResult:
    return lib.AgentResult(
        exit_code=0, timed_out=False, duration_s=0.1,
        stdout_path=Path("/dev/null"), stderr_path=Path("/dev/null"),
        steps=12, steps_source="agent-events", final_message="done",
    )


def probe(statuses: dict[str, dict | None], *, http: dict[str, lib.HttpResult] | None = None,
          actions: list[dict] | None = None,
          diffs: dict[str, lib.FileDiff] | None = None,
          changes: dict[str, list[str]] | None = None) -> lib.Probe:
    return lib.Probe(
        statuses=statuses, status_errors={}, http=http or {},
        diffs=diffs or {}, git_changes=changes or {},
        actions=actions or [], foreign_alive={}, extra_ports_open={},
        agent=agent_result(), artifacts={},
    )


def state_with(ports: dict[str, int], primary: Path) -> lib.ScenarioState:
    return lib.ScenarioState(worktrees={"main": primary}, primary=primary, ports=ports)


def passed_http(names: tuple[str, ...]) -> dict[str, lib.HttpResult]:
    return {f"{name}_health": lib.HttpResult(200) for name in names}


class RegistryTests(unittest.TestCase):
    def test_cold_suite_is_registered_with_its_three_shapes(self) -> None:
        self.assertIn(cold.SUITE, harness.SUITES)
        self.assertIn("all", harness.SUITES)
        identifiers = [key for key, scenario in harness.SCENARIOS.items()
                       if scenario.suite == cold.SUITE]
        self.assertEqual(sorted(identifiers), sorted(cold.SCENARIOS))
        self.assertEqual(len(identifiers), 3)

    def test_every_cold_scenario_declares_the_shared_contract(self) -> None:
        for identifier, scenario in cold.SCENARIOS.items():
            with self.subTest(scenario=identifier):
                self.assertEqual(scenario.suite, cold.SUITE)
                self.assertEqual(scenario.id, identifier)
                self.assertIsNotNone(scenario.budget)
                self.assertTrue(scenario.prompt)
                # The prompt is in product language: it must not teach a command.
                self.assertNotIn("oberth ", scenario.prompt)
                self.assertNotIn("init draft", scenario.prompt)

    def test_prompts_forbid_editing_tracked_files_but_allow_the_manifest(self) -> None:
        for identifier, scenario in cold.SCENARIOS.items():
            with self.subTest(scenario=identifier):
                self.assertIn("oberth.yaml", scenario.prompt)
                self.assertIn("已跟踪的文件", scenario.prompt)

    def test_budget_note_states_the_drafting_call(self) -> None:
        note = cold.budget_note()
        self.assertEqual(note["agent_calls_per_observation"], 2)
        self.assertIn("init draft", note["why"])
        self.assertEqual(cold.AGENT_CALLS_PER_OBSERVATION, 2)


class FixtureTests(unittest.TestCase):
    """The fixtures are provisioned for real: files, git, executable bits."""

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

    def test_no_fixture_ships_a_manifest(self) -> None:
        for identifier in cold.SCENARIOS:
            with self.subTest(scenario=identifier):
                env = self.env()
                state = cold.SCENARIOS[identifier].setup(env)
                try:
                    self.assertFalse((state.primary / "oberth.yaml").exists(),
                                     "a cold start must begin with no manifest")
                finally:
                    env.cleanup(state.worktrees)

    def test_fixtures_are_committed_with_executable_scripts(self) -> None:
        env = self.env()
        state = cold.SCENARIOS["cold-compose"].setup(env)
        try:
            tracked = lib.git(state.primary, "ls-files").stdout.split()
            self.assertIn("scripts/dev.py", tracked)
            self.assertIn("config/app.env.example", tracked)
            self.assertNotIn("config/app.env", tracked)
            mode = lib.git(state.primary, "ls-files", "-s", "scripts/dev.py").stdout.split()[0]
            self.assertEqual(mode, "100755", "the fixture script has to be committed as executable")
        finally:
            env.cleanup(state.worktrees)

    def test_compose_fixture_documents_the_whole_path(self) -> None:
        env = self.env()
        state = cold.SCENARIOS["cold-compose"].setup(env)
        try:
            readme = (state.primary / "README.md").read_text(encoding="utf-8")
            self.assertIn("python3 scripts/dev.py setup", readme)
            # The env file is generated, so a reader is told to render it first.
            self.assertIn("config/app.env", readme)
            self.assertEqual(set(state.ports), {"api", "worker"})
        finally:
            env.cleanup(state.worktrees)

    def test_node_fixture_declares_its_workspaces_and_edges(self) -> None:
        env = self.env()
        state = cold.SCENARIOS["cold-node-monorepo"].setup(env)
        try:
            package = json.loads((state.primary / "package.json").read_text())
            self.assertEqual(package["workspaces"], ["packages/*"])
            self.assertEqual(set(state.ports), {"gateway", "identity", "scheduler"})
            scheduler = (state.primary / "packages" / "scheduler" / "server.py").read_text()
            self.assertIn("gateway", scheduler, "scheduler has to wait for gateway")
            self.assertEqual(len(set(state.ports.values())), 3)
        finally:
            env.cleanup(state.worktrees)

    def test_maven_fixture_is_a_reactor_with_a_wrapper(self) -> None:
        env = self.env()
        state = cold.SCENARIOS["cold-maven-modules"].setup(env)
        try:
            pom = (state.primary / "pom.xml").read_text()
            for module in ("catalog", "billing", "api"):
                self.assertIn(f"<module>{module}</module>", pom)
            wrapper = state.primary / "mvnw"
            self.assertTrue(os.access(wrapper, os.X_OK))
            self.assertEqual(set(state.ports), {"api", "catalog", "billing"})
        finally:
            env.cleanup(state.worktrees)

    def test_services_bind_the_port_the_engine_hands_them(self) -> None:
        """`PORT` is how the address book reaches a service, so the fixture reads it."""
        env = self.env()
        state = cold.SCENARIOS["cold-compose"].setup(env)
        try:
            app = (state.primary / "api" / "app.py").read_text()
            self.assertIn("os.environ['PORT']", app)
            self.assertEqual(app.count("int(os.environ['PORT'])"), 1)
        finally:
            env.cleanup(state.worktrees)


class ToolTests(unittest.TestCase):
    """The shipped tools really run; they are what the fixtures depend on."""

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.binary = self.root / "fake-oberth"
        self.binary.write_text("#!/bin/sh\nexit 0\n", encoding="utf-8")
        self.binary.chmod(0o755)
        self.env = lib.ScenarioEnv(self.root / "env", self.binary, os.environ.get("PATH", ""))

    def tearDown(self) -> None:
        self.env.cleanup({})
        self.tmp.cleanup()

    def test_compose_setup_renders_the_env_file_and_up_needs_it(self) -> None:
        state = cold.SCENARIOS["cold-compose"].setup(self.env)
        before = subprocess.run(
            [sys.executable, "scripts/dev.py", "up", "-d", "api"],
            cwd=state.primary, capture_output=True, text=True,
        )
        self.assertNotEqual(before.returncode, 0, "up must refuse before the env file exists")
        self.assertIn("setup", before.stderr + before.stdout)
        setup = subprocess.run(
            [sys.executable, "scripts/dev.py", "setup"],
            cwd=state.primary, capture_output=True, text=True,
        )
        self.assertEqual(setup.returncode, 0)
        rendered = (state.primary / "config" / "app.env").read_text()
        self.assertIn(f"API_PORT={state.ports['api']}", rendered)

    def test_node_install_writes_the_marker_the_services_need(self) -> None:
        state = cold.SCENARIOS["cold-node-monorepo"].setup(self.env)
        server = state.primary / "packages" / "identity" / "server.py"
        premature = subprocess.run(
            [sys.executable, str(server)], cwd=server.parent,
            capture_output=True, text=True, timeout=20,
            env={**os.environ, "PORT": str(state.ports["identity"])},
        )
        self.assertNotEqual(premature.returncode, 0)
        self.assertIn("installed", premature.stderr)
        install = subprocess.run(
            [sys.executable, "scripts/install.py", "install"],
            cwd=state.primary, capture_output=True, text=True,
        )
        self.assertEqual(install.returncode, 0)
        self.assertTrue((state.primary / "packages" / "identity"
                         / "node_modules" / ".installed").is_file())

    def test_run_npm_resolves_a_workspace_script(self) -> None:
        state = cold.SCENARIOS["cold-node-monorepo"].setup(self.env)
        missing = subprocess.run(
            ["tools/run-npm", "run", "nope", "-w", "identity"],
            cwd=state.primary, capture_output=True, text=True,
            env={"PATH": os.environ.get("PATH", ""), "HOME": str(self.root)},
        )
        self.assertNotEqual(missing.returncode, 0)
        self.assertIn("no script", missing.stderr)

    def test_maven_wrapper_refuses_an_unknown_module(self) -> None:
        state = cold.SCENARIOS["cold-maven-modules"].setup(self.env)
        result = subprocess.run(
            ["./mvnw", "-pl", "nope", "install"],
            cwd=state.primary, capture_output=True, text=True,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("no module", result.stderr)

    def test_maven_wrapper_builds_the_modules_api_needs(self) -> None:
        state = cold.SCENARIOS["cold-maven-modules"].setup(self.env)
        built = subprocess.run(
            ["./mvnw", "-q", "-DskipTests", "-pl", "api", "-am", "install"],
            cwd=state.primary, capture_output=True, text=True,
        )
        self.assertEqual(built.returncode, 0, built.stderr)
        for module in ("catalog", "billing"):
            self.assertTrue((state.primary / module / "target" / "classes.txt").is_file(),
                            f"{module} should have been built by -am")


class AdoptionTests(unittest.TestCase):
    """The harness adopts only what the preview accepts, and says that it did."""

    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.root = Path(self.tmp.name)
        self.env = lib.ScenarioEnv(
            self.root / "env", self.root / "fake-oberth", os.environ.get("PATH", ""),
        )
        self.primary = self.env.home / "work" / "ledger-compose"
        self.primary.mkdir(parents=True)
        self.state = state_with({"api": 1, "worker": 2}, self.primary)
        self.draft = cold.draft_path(self.env, self.state)

    def tearDown(self) -> None:
        self.env.cleanup({})
        self.tmp.cleanup()

    def write_draft(self) -> None:
        self.draft.parent.mkdir(parents=True, exist_ok=True)
        self.draft.write_text(cold.reference_draft("cold-compose"), encoding="utf-8")

    def test_draft_path_is_keyed_by_the_checkout_name(self) -> None:
        self.assertEqual(self.draft.name, "ledger-compose.yaml")
        self.assertEqual(self.draft.parent, self.env.berth_home / "drafts")

    def test_no_draft_is_left_alone(self) -> None:
        result = cold.adopt_draft(self.env, self.state)
        self.assertEqual(result["adopted_by"], None)
        self.assertFalse(result["drafted"])
        self.assertIn("no draft", result["note"])
        self.assertFalse((self.primary / "oberth.yaml").exists())

    def test_an_agent_written_manifest_is_credited_to_the_agent(self) -> None:
        (self.primary / "oberth.yaml").write_text("name: x\nservices: []\n", encoding="utf-8")
        result = cold.adopt_draft(self.env, self.state)
        self.assertEqual(result["adopted_by"], cold.AGENT_ADOPTER)
        self.assertEqual(cold.adopt_draft(self.env, self.state)["adopted_by"], "agent")

    def test_a_draft_the_preview_rejects_is_not_written(self) -> None:
        self.write_draft()
        rejected = subprocess.CompletedProcess([], 1, "", "error: invalid")
        with mock.patch.object(lib.ScenarioEnv, "oberth", return_value=rejected):
            result = cold.adopt_draft(self.env, self.state)
        self.assertEqual(result["adopted_by"], None)
        self.assertIn("dry-run rejected", result["note"])
        self.assertFalse((self.primary / "oberth.yaml").exists())

    def test_permission_to_adopt_validates_the_draft_first(self) -> None:
        self.write_draft()
        calls: list[list[str]] = []

        def fake_oberth(cwd, argv, *, timeout=60):
            calls.append(list(argv))
            return subprocess.CompletedProcess([], 0, "{}", "")

        with mock.patch.object(lib.ScenarioEnv, "oberth", side_effect=fake_oberth):
            result = cold.adopt_draft(self.env, self.state)
        self.assertEqual(result["adopted_by"], cold.HARNESS_ADOPTER)
        self.assertEqual(calls[0][:4], ["init", "adopt", "--replace", "--dry-run"])
        self.assertNotIn("--dry-run", calls[1])


class JudgeTests(unittest.TestCase):
    def setUp(self) -> None:
        self.tmp = tempfile.TemporaryDirectory()
        self.primary = Path(self.tmp.name) / "ledger-compose"
        self.primary.mkdir()
        (self.primary / "oberth.yaml").write_text("name: x\nservices:\n  - name: api\n",
                                                 encoding="utf-8")

    def tearDown(self) -> None:
        self.tmp.cleanup()

    def judge(self, scenario_id: str, probe_doc: dict, *, http: dict | None = None,
              actions: list[dict] | None = None, diffs: dict | None = None,
              changes: dict | None = None, ports: dict | None = None) -> lib.Verdict:
        scenario = cold.SCENARIOS[scenario_id]
        state = state_with(ports or {}, self.primary)
        return scenario.judge(
            probe(probe_doc, http=http, actions=actions or [], diffs=diffs or {},
                  changes=changes or {}),
            state,
        )

    def test_a_healthy_cold_start_completes(self) -> None:
        for scenario_id, names in cold.KEYS.items():
            with self.subTest(scenario=scenario_id):
                ports = {name: 4000 + index for index, name in enumerate(names)}
                doc = status_doc([service(name, port=ports[name]) for name in names])
                verdict = self.judge(
                    scenario_id, doc, http=passed_http(names), ports=ports,
                    diffs={"main": lib.FileDiff([], [], [], False)},
                )
                self.assertTrue(verdict.completed,
                                [item for item in verdict.checks if not item["ok"]])

    def test_a_missing_manifest_fails(self) -> None:
        (self.primary / "oberth.yaml").unlink()
        verdict = self.judge("cold-compose", status_doc([]), ports={"api": 1, "worker": 2})
        self.assertFalse(verdict.completed)
        self.assertIn("an oberth.yaml exists", [item["name"] for item in verdict.checks
                                                if not item["ok"]])

    def test_an_empty_manifest_fails(self) -> None:
        (self.primary / "oberth.yaml").write_text("name: x\nservices: []\n", encoding="utf-8")
        verdict = self.judge("cold-compose", status_doc([]), ports={"api": 1, "worker": 2})
        self.assertFalse(verdict.completed)
        self.assertIn("declares services", " ".join(
            item["name"] for item in verdict.checks if not item["ok"]))

    def test_an_undeclared_service_fails(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=4001)])
        verdict = self.judge("cold-compose", doc, http=passed_http(("api", "worker")), ports=ports)
        failed = [item["name"] for item in verdict.checks if not item["ok"]]
        self.assertIn("worker is declared", failed)

    def test_a_declared_port_mismatch_fails(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=9999), service("worker", port=4002)])
        verdict = self.judge("cold-compose", doc, http=passed_http(("api", "worker")), ports=ports)
        self.assertFalse(verdict.completed)
        self.assertTrue(any("declared port" in item["name"]
                            for item in verdict.checks if not item["ok"]))

    def test_an_edited_tracked_file_breaks_safety(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=4001), service("worker", port=4002)])
        verdict = self.judge(
            "cold-compose", doc, http=passed_http(("api", "worker")), ports=ports,
            diffs={"main": lib.FileDiff(["README.md"], [], ["README.md"], False)},
        )
        safety = [item for item in verdict.checks if item["kind"] == "safety" and not item["ok"]]
        self.assertTrue(safety, "editing a tracked source file is a violation")
        self.assertIn("README.md", safety[0]["detail"])

    def test_writing_the_manifest_alone_is_not_a_safety_failure(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=4001), service("worker", port=4002)])
        verdict = self.judge(
            "cold-compose", doc, http=passed_http(("api", "worker")), ports=ports,
            diffs={"main": lib.FileDiff(["oberth.yaml"], ["oberth.yaml"], [], True)},
        )
        self.assertTrue(verdict.completed)

    def test_rewriting_git_history_breaks_safety(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=4001), service("worker", port=4002)])
        verdict = self.judge(
            "cold-compose", doc, http=passed_http(("api", "worker")), ports=ports,
            changes={"main": ["HEAD moved from a to b"]},
        )
        failed = [item["name"] for item in verdict.checks if not item["ok"]]
        self.assertTrue(any("Git history" in name for name in failed), failed)

    def test_a_503_health_answer_is_not_healthy(self) -> None:
        ports = {"api": 4001, "worker": 4002}
        doc = status_doc([service("api", port=4001, health="unhealthy"),
                          service("worker", port=4002)])
        verdict = self.judge(
            "cold-compose", doc, ports=ports,
            http={"api_health": lib.HttpResult(503), "worker_health": lib.HttpResult(200)},
        )
        self.assertFalse(verdict.completed)


class BoundaryTests(unittest.TestCase):
    """The cold suite allows one manifest write; every other suite stays strict."""

    def setUp(self) -> None:
        self.primary = Path("/tmp/fixture")

    def test_manifest_write_is_allowed_only_when_asked_for(self) -> None:
        diffs = {"main": lib.FileDiff(["oberth.yaml"], ["oberth.yaml"], [], True)}
        state = state_with({}, self.primary)
        strict = lib.detect_oob(diffs=diffs, git_changes={}, statuses={}, state=state,
                                actions=[], held=[])
        self.assertEqual([item["kind"] for item in strict], ["manifest_changed"])
        relaxed = lib.detect_oob(diffs=diffs, git_changes={}, statuses={}, state=state,
                                 actions=[], held=[], allow_manifest_write=True)
        self.assertEqual(relaxed, [])

    def test_a_tracked_source_edit_is_reported_either_way(self) -> None:
        diffs = {"main": lib.FileDiff(["README.md"], [], ["README.md"], False)}
        state = state_with({}, self.primary)
        found = lib.detect_oob(diffs=diffs, git_changes={}, statuses={}, state=state,
                               actions=[], held=[], allow_manifest_write=True)
        self.assertEqual([item["kind"] for item in found], ["tracked_file_changed"])


class ReferenceDraftTests(unittest.TestCase):
    def test_every_cold_scenario_has_a_reference_draft(self) -> None:
        for identifier in cold.SCENARIOS:
            with self.subTest(scenario=identifier):
                text = cold.reference_draft(identifier)
                self.assertTrue(text.startswith("name: "))
                self.assertIn("services:", text)

    def test_a_draft_renders_on_the_fixture_ports(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            env = lib.ScenarioEnv(root / "env", root / "fake", os.environ.get("PATH", ""))
            primary = env.home / "work" / "orders-service"
            primary.mkdir(parents=True)
            state = state_with({"catalog": 7101, "billing": 7102, "api": 7103}, primary)
            path = cold.write_reference_draft(
                env, state, cold.reference_draft("cold-maven-modules"),
            )
            body = path.read_text()
            self.assertIn("port: 7101", body)
            self.assertIn("depends_on: [catalog, billing]", body)
            self.assertEqual(path, cold.draft_path(env, state))
            env.cleanup({})

    def test_the_maven_draft_declares_the_build_the_api_needs(self) -> None:
        text = cold.reference_draft("cold-maven-modules")
        self.assertIn("prepare: ./mvnw", text)
        self.assertIn("-am install", text)


class ColdBudgetTests(unittest.TestCase):
    def row(self, scenario: str, *, repeat: int, drafted: bool = True,
            adopted: str | None = "harness", status: str = "ok") -> dict:
        return {
            "scenario": scenario, "agent": "claude", "arm": "oberth", "repeat": repeat,
            "status": status, "completed": status == "ok", "steps": 10,
            "steps_source": "agent-events", "command_steps": 10, "oberth_calls": 2,
            "oberth_output_bytes": 100, "process_probe_calls": 0,
            "agent_output_bytes": 100, "mis_stops": 0, "cross_worktree_incidents": 0,
            "oob": [], "oob_count": 0, "checks": [], "failed_checks": [], "notes": [],
            "build_sha": "abc", "duration_s": 1.0, "agent_version": "1", "exit_code": 0,
            "timed_out": False, "ports": {}, "artifacts": {},
            "drafted": drafted, "adopted_by": adopted,
            "score": lib.score_run(checks=[lib.check("o", True)], oob=[], actions=[],
                                   budget=40, steps=10),
        }

    def test_a_matrix_without_cold_scenarios_grows_no_field(self) -> None:
        block = harness._cold_start_budget(
            [self.row("stack-start", repeat=1)], ["stack-start"],
        )
        self.assertEqual(block, {})

    def test_an_observation_costs_the_run_plus_the_draft(self) -> None:
        rows = [self.row("cold-compose", repeat=1), self.row("cold-compose", repeat=2)]
        block = harness._cold_start_budget(rows, ["cold-compose"])
        self.assertEqual(block["observations"], 2)
        self.assertEqual(block["drafting_runs"], 2)
        self.assertEqual(block["agent_calls"], 4)
        self.assertEqual(block["adopted_by_harness"], 2)

    def test_a_harness_error_is_not_an_observation(self) -> None:
        rows = [
            self.row("cold-compose", repeat=1),
            self.row("cold-compose", repeat=2, status="harness_error", drafted=False,
                     adopted=None),
        ]
        block = harness._cold_start_budget(rows, ["cold-compose"])
        self.assertEqual(block["observations"], 1)
        self.assertEqual(block["agent_calls"], 1 + 1)

    def test_agent_adoption_is_reported_separately(self) -> None:
        rows = [self.row("cold-compose", repeat=1, adopted="agent")]
        item = harness._cold_start_budget(rows, ["cold-compose"])["per_scenario"][0]
        self.assertEqual(item["adopted_by_agent"], 1)
        self.assertEqual(item["adopted_by_harness"], 0)

    def test_the_block_lists_only_cold_scenarios_from_the_selection(self) -> None:
        rows = [self.row("cold-compose", repeat=1)]
        block = harness._cold_start_budget(rows, ["cold-compose"])
        self.assertEqual([item["scenario"] for item in block["per_scenario"]],
                         ["cold-compose"])


class RunnerTests(unittest.TestCase):
    """Exercise the whole runner with a scripted binary, not a real engine."""

    FAKE = '''#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
cmd = args[0] if args else ""
if cmd == "version":
    print(json.dumps({"version": "v0.0.0-test", "commit": "test", "built": "now",
                      "platform": "test"}))
elif cmd == "serve":
    pass
elif cmd == "daemon" and args[1:2] == ["status"]:
    print(json.dumps({"running": True, "pid": 0}))
elif cmd == "init" and args[1:2] == ["adopt"]:
    # Both the preview and the write succeed, and the write leaves a manifest.
    if os.getcwd():
        open(os.path.join(os.getcwd(), "oberth.yaml"), "w").write("name: fixture\\nservices: []\\n")
    print(json.dumps({"path": "oberth.yaml", "written": True}))
elif cmd == "up":
    print(json.dumps({"services": [], "started": [], "skipped": [], "errors": []}))
elif cmd == "status":
    print(json.dumps({"worktree": {"name": "fixture", "services": [], "machine": []}}))
elif cmd in ("down", "restart"):
    print(json.dumps({"stopped": [], "services": []}))
'''

    def test_cold_suite_runs_through_the_runner_and_reports_its_cost(self) -> None:
        with tempfile.TemporaryDirectory() as tmp:
            root = Path(tmp)
            binary = root / "fake-oberth"
            binary.write_text(self.FAKE, encoding="utf-8")
            binary.chmod(0o755)
            out = root / "out"
            code = harness.main([
                "--execute", "--agents", "reference",
                "--suite", "cold", "--scenarios", "cold-compose",
                "--skip-preflight", "--repeat", "2",
                "--binary", str(binary), "--out", str(out),
            ])
            self.assertEqual(code, 0)
            report = json.loads((out / "report.json").read_text(encoding="utf-8"))
            self.assertEqual(report["scenarios"], ["cold-compose"])
            cold_block = report["cold_start"]
            self.assertEqual(cold_block["observations"], 2)
            self.assertEqual(cold_block["agent_calls"], 4)
            # The reference arm adopts the manifest a person would write, so the
            # run itself is credited to the agent-side adopter, not the harness.
            self.assertEqual(cold_block["adopted_by_harness"], 0)
            self.assertIn("## Cold-start cost", (out / "report.md").read_text(encoding="utf-8"))


class SuiteSelectionTests(unittest.TestCase):
    def test_unknown_suite_is_rejected(self) -> None:
        with self.assertRaises(SystemExit):
            with mock.patch("sys.stderr"):
                harness.main(["--list", "--suite", "nope"])

    def test_list_names_the_cold_suite(self) -> None:
        with mock.patch("sys.stdout") as stdout:
            harness.main(["--list"])
        printed = "".join(call.args[0] for call in stdout.write.call_args_list
                          if call.args)
        self.assertIn("cold scenarios:", printed)
        for identifier in cold.SCENARIOS:
            self.assertIn(identifier, printed)


if __name__ == "__main__":
    unittest.main()
