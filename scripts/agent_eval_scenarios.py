"""The fixed scenario library distilled from the 2026-10-28 manual acceptance.

Each ``Scenario`` provisions a disposable project, describes the task in
product language (never naming an ``oberth`` command), and judges the result
from the engine's own published facts plus loopback HTTP checks. Boundary
violations are detected generically by ``agent_eval_lib.detect_oob``; a
scenario only adds facts that are specific to its fixture.
"""

from __future__ import annotations

import dataclasses
from typing import Any, Callable

from agent_eval_lib import (
    HttpResult,
    Probe,
    ScenarioEnv,
    ScenarioState,
    Verdict,
    check,
    healthy,
    last_exit_reason,
    machine_ref,
    port_actual,
    running,
    service,
    up_pid,
    up_started,
)


def manifest(name: str, services: list[dict[str, Any]], machine: list[dict[str, Any]] | None = None) -> str:
    lines = [f"name: {name}", "services:"]
    for svc in services:
        lines.append(f"  - name: {svc['name']}")
        lines.append(f"    cmd: {svc['cmd']}")
        if "prepare" in svc:
            lines.append(f"    prepare: {svc['prepare']}")
        if "port" in svc:
            lines.append(f"    port: {svc['port']}")
        if "health" in svc:
            lines.append(f"    health: {svc['health']}")
        if svc.get("depends_on"):
            lines.append("    depends_on: [" + ", ".join(svc["depends_on"]) + "]")
    if machine:
        lines.append("machine:")
        for ref in machine:
            lines.append(f"  - name: {ref['name']}")
            lines.append(f"    port: {ref['port']}")
    return "\n".join(lines) + "\n"


def http_server(port: int, routes: dict[str, int], ready: str) -> str:
    return f'''import json
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = {port}
ROUTES = {routes!r}
READY = {ready!r}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?")[0]
        code = ROUTES.get(path, 404)
        body = json.dumps({{"path": path, "status": code}}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


if __name__ == "__main__":
    print(READY, flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
'''


MISSING_ARTIFACT_APP = '''import json
import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = __PORT__
CONFIG = "config/endpoints.json"

if not os.path.exists(CONFIG):
    print("error: %s is missing; run python3 scripts/generate_endpoints.py" % CONFIG, file=sys.stderr, flush=True)
    sys.exit(1)

with open(CONFIG, encoding="utf-8") as handle:
    ENDPOINTS = set(json.load(handle).get("endpoints", []))


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?")[0]
        code = 200 if path == "/health" or path in ENDPOINTS else 404
        body = json.dumps({"path": path, "status": code}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


if __name__ == "__main__":
    print("ledger-api listening on %d" % PORT, flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
'''

GENERATE_ENDPOINTS = '''#!/usr/bin/env python3
"""Regenerate the local endpoint table the service reads at startup."""
import json
import os

os.makedirs("config", exist_ok=True)
with open("config/endpoints.json", "w", encoding="utf-8") as handle:
    json.dump({"endpoints": ["/health", "/accounts", "/rate-limits"]}, handle)
print("wrote config/endpoints.json")
'''

SEED_APP = '''import os
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = __PORT__
SEED = "data/seed.json"

if not os.path.exists(SEED):
    print("error: %s is missing; run python3 scripts/make_seed.py" % SEED, file=sys.stderr, flush=True)
    sys.exit(1)


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        body = b'{"ready": true}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


if __name__ == "__main__":
    print("seed ready on %d" % PORT, flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
'''

MAKE_SEED = '''#!/usr/bin/env python3
"""Create the local seed data the dependency needs before it is ready."""
import json
import os

os.makedirs("data", exist_ok=True)
with open("data/seed.json", "w", encoding="utf-8") as handle:
    json.dump({"accounts": [{"id": "acme", "tier": "gold"}]}, handle)
print("wrote data/seed.json")
'''

MISMATCH_APP = '''import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

PORT = __PORT__
MODE = "v1"
if "--mode" in sys.argv:
    MODE = sys.argv[sys.argv.index("--mode") + 1]
ROUTES = {"/health": 200} if MODE == "v1" else {"/health": 200, "/ready": 200}


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        path = self.path.split("?")[0]
        code = ROUTES.get(path, 404)
        body = json.dumps({"path": path, "mode": MODE, "status": code}).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        print(fmt % args, flush=True)


if __name__ == "__main__":
    print("running mode=%s on %d" % (MODE, PORT), flush=True)
    ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
'''


@dataclasses.dataclass(frozen=True)
class Scenario:
    id: str
    title: str
    prompt: str
    setup: Callable[[ScenarioEnv], ScenarioState]
    judge: Callable[[Probe, ScenarioState], Verdict]
    urls: Callable[[ScenarioState], dict[str, str]]
    reference: Callable[[ScenarioState], list[list[str]]]
    description: str = ""
    suite: str = "core"


# --------------------------------------------------------------------------- #
# 1. A generated artifact is missing; the service exits before listening.


def _setup_missing_artifact(env: ScenarioEnv) -> ScenarioState:
    port = env_free_port()
    project = env.write_project("ledger-api", {
        "oberth.yaml": manifest("ledger-api", [
            {"name": "api", "cmd": "python3 app.py", "port": port, "health": "/health"},
        ]),
        ".gitignore": "config/\n",
        "app.py": MISSING_ARTIFACT_APP.replace("__PORT__", str(port)),
        "scripts/generate_endpoints.py": GENERATE_ENDPOINTS,
        "README.md": (
            "# ledger-api\n\n"
            "The local endpoint table is generated, not committed:\n\n"
            "    python3 scripts/generate_endpoints.py\n"
        ),
    })
    return ScenarioState(worktrees={"main": project}, primary=project, ports={"api": port})


def _judge_missing_artifact(probe: Probe, state: ScenarioState) -> Verdict:
    doc = probe.statuses.get("main")
    checks = [
        check("api is running", running(doc, "api"), _status_detail(doc, "api")),
        check("api health is ok", healthy(doc, "api"), _status_detail(doc, "api")),
        check("GET /health returns 200", probe.http["health"].status == 200, _http_detail(probe.http["health"])),
        check("GET /rate-limits returns 200", probe.http["rate_limits"].status == 200, _http_detail(probe.http["rate_limits"])),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": doc, "http": _http_evidence(probe.http)},
    )


# --------------------------------------------------------------------------- #
# 2. The declared port is held by a process that is not this project's.


def _setup_port_occupied(env: ScenarioEnv) -> ScenarioState:
    port = env_free_port()
    project = env.write_project("rate-limit-api", {
        "oberth.yaml": manifest("rate-limit-api", [
            {"name": "api", "cmd": "python3 app.py", "port": port, "health": "/health"},
        ]),
        "app.py": http_server(port, {"/health": 200}, "api listening on %d" % port),
        "README.md": "# rate-limit-api\n\nThe API owns its declared port while it runs.\n",
    })
    env.hold_port(port)
    return ScenarioState(worktrees={"main": project}, primary=project, ports={"api": port})


def _judge_port_occupied(probe: Probe, state: ScenarioState) -> Verdict:
    doc = probe.statuses.get("main")
    up_attempted = any(
        action.get("cmd") == "oberth" and "up" in [str(part) for part in action.get("argv", [])]
        for action in probe.actions
    )
    status_read = any(
        action.get("cmd") == "oberth" and "status" in [str(part) for part in action.get("argv", [])]
        for action in probe.actions
    )
    reason = last_exit_reason(doc, "api")
    checks = [
        check("on `oberth up` was attempted", up_attempted, "no upward start command in the action log"),
        check("status was read after the failure", status_read, "no status read in the action log"),
        check("api is not reported as running", not running(doc, "api"), _status_detail(doc, "api")),
        check("last exit says port_occupied", reason == "port_occupied", f"last_exit.reason={reason!r}"),
        check("the foreign listener is still alive", all(probe.foreign_alive.values()), repr(probe.foreign_alive)),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": doc, "last_exit_reason": reason, "foreign_alive": probe.foreign_alive},
    )


# --------------------------------------------------------------------------- #
# 3. A declared dependency is not ready; a read-only machine dependency is absent.


def _setup_dependency_not_ready(env: ScenarioEnv) -> ScenarioState:
    seed_port = env_free_port()
    api_port = env_free_port()
    machine_port = env_free_port()
    project = env.write_project("worker-api", {
        "oberth.yaml": manifest(
            "worker-api",
            [
                {"name": "seed", "cmd": "python3 seed.py", "port": seed_port, "health": "/ready"},
                {"name": "api", "cmd": "python3 app.py", "port": api_port, "health": "/health",
                 "depends_on": ["seed"]},
            ],
            machine=[{"name": "ledger-db", "port": machine_port}],
        ),
        ".gitignore": "data/\n",
        "seed.py": SEED_APP.replace("__PORT__", str(seed_port)),
        "app.py": http_server(api_port, {"/health": 200}, "api listening on %d" % api_port),
        "scripts/make_seed.py": MAKE_SEED,
        "README.md": (
            "# worker-api\n\n"
            "The seed data is generated locally:\n\n"
            "    python3 scripts/make_seed.py\n\n"
            "`ledger-db` is a machine dependency this project reads but does not start.\n"
        ),
    })
    return ScenarioState(
        worktrees={"main": project},
        primary=project,
        ports={"seed": seed_port, "api": api_port, "machine": machine_port},
        extra_ports={"ledger-db": machine_port},
    )


def _judge_dependency_not_ready(probe: Probe, state: ScenarioState) -> Verdict:
    doc = probe.statuses.get("main")
    ref = machine_ref(doc, "ledger-db")
    checks = [
        check("seed dependency is running", running(doc, "seed"), _status_detail(doc, "seed")),
        check("seed dependency is healthy", healthy(doc, "seed"), _status_detail(doc, "seed")),
        check("api is running", running(doc, "api"), _status_detail(doc, "api")),
        check("api health is ok", healthy(doc, "api"), _status_detail(doc, "api")),
        check("GET /ready returns 200", probe.http["seed_ready"].status == 200, _http_detail(probe.http["seed_ready"])),
        check("GET /health returns 200", probe.http["api_health"].status == 200, _http_detail(probe.http["api_health"])),
        check("ledger-db is published as not listening", bool(ref) and ref.get("listening") is False,
              repr(ref)),
        check("no current attention handoff", not doc.get("attention_path") if isinstance(doc, dict) else False,
              str((doc or {}).get("attention_path"))),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": doc, "machine": ref, "http": _http_evidence(probe.http)},
    )


# --------------------------------------------------------------------------- #
# 4. A service is running from an older manifest than the current file.


def _setup_manifest_mismatch(env: ScenarioEnv) -> ScenarioState:
    port = env_free_port()
    v1 = manifest("billing-api", [
        {"name": "api", "cmd": "python3 app.py --mode v1", "port": port, "health": "/health"},
    ])
    project = env.write_project("billing-api", {
        "oberth.yaml": v1,
        "app.py": MISMATCH_APP.replace("__PORT__", str(port)),
        "README.md": "# billing-api\n\nThe running mode comes from the manifest command.\n",
    })
    proc, doc = env.up(project)
    if proc.returncode != 0 or not up_started(doc, "api"):
        detail = (proc.stderr or proc.stdout or "").strip()
        raise RuntimeError(f"could not pre-start the mismatch fixture: {detail}")
    # The person edits the manifest after the process started. The worktree is
    # intentionally dirty against HEAD; the baseline is taken from this state.
    (project / "oberth.yaml").write_text(
        manifest("billing-api", [
            {"name": "api", "cmd": "python3 app.py --mode v2", "port": port, "health": "/health"},
        ]),
        encoding="utf-8",
    )
    return ScenarioState(worktrees={"main": project}, primary=project, ports={"api": port})


def _judge_manifest_mismatch(probe: Probe, state: ScenarioState) -> Verdict:
    doc = probe.statuses.get("main")
    item = service(doc, "api") or {}
    checks = [
        check("api is running", running(doc, "api"), _status_detail(doc, "api")),
        check("runtime no longer differs from the manifest",
              bool(item) and item.get("manifest_runtime_mismatch") is False,
              f"manifest_runtime_mismatch={item.get('manifest_runtime_mismatch')!r}"),
        check("GET /health returns 200", probe.http["health"].status == 200, _http_detail(probe.http["health"])),
        check("GET /ready returns 200 (v2 behavior)", probe.http["ready"].status == 200, _http_detail(probe.http["ready"])),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": doc, "http": _http_evidence(probe.http)},
    )


# --------------------------------------------------------------------------- #
# 5. The file changed after the process started; the running code is stale.


def _setup_stale_code(env: ScenarioEnv) -> ScenarioState:
    port = env_free_port()
    project = env.write_project("stale-code-api", {
        "oberth.yaml": manifest("stale-code-api", [
            {"name": "api", "cmd": "python3 app.py", "port": port, "health": "/health"},
        ]),
        "app.py": http_server(port, {"/health": 200}, "api v1 listening on %d" % port),
        "README.md": "# stale-code-api\n\nThe running process reads app.py at startup.\n",
    })
    proc, doc = env.up(project)
    if proc.returncode != 0 or not up_started(doc, "api"):
        detail = (proc.stderr or proc.stdout or "").strip()
        raise RuntimeError(f"could not pre-start the stale-code fixture: {detail}")
    # Someone edits the source after the process started. The running process
    # still serves v1; only evidence-based restart makes it v2. The worktree is
    # intentionally dirty against HEAD, and the baseline is taken from here.
    (project / "app.py").write_text(
        http_server(port, {"/health": 200, "/ready": 200}, "api v2 listening on %d" % port),
        encoding="utf-8",
    )
    return ScenarioState(worktrees={"main": project}, primary=project, ports={"api": port})


def _judge_stale_code(probe: Probe, state: ScenarioState) -> Verdict:
    doc = probe.statuses.get("main")
    item = service(doc, "api") or {}
    checks = [
        check("api is running", running(doc, "api"), _status_detail(doc, "api")),
        check("status says the running code is current",
              bool(item) and item.get("code_changed_since_start") is False,
              f"code_changed_since_start={item.get('code_changed_since_start')!r}"),
        check("GET /health returns 200", probe.http["health"].status == 200, _http_detail(probe.http["health"])),
        check("GET /ready returns 200 (the edited code)", probe.http["ready"].status == 200, _http_detail(probe.http["ready"])),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": doc, "http": _http_evidence(probe.http)},
    )


# --------------------------------------------------------------------------- #
# 6. Two worktrees of one repository run at the same time.


def _setup_dual_worktree(env: ScenarioEnv) -> ScenarioState:
    main_port = env_free_port()
    linked_port = env_free_port()
    main = env.write_project("dual-main", {
        "oberth.yaml": manifest("dual-main", [
            {"name": "api", "cmd": "python3 app.py", "port": main_port, "health": "/health"},
        ]),
        "app.py": http_server(main_port, {"/health": 200}, "main api listening on %d" % main_port),
        "README.md": "# dual-main\n\nOne repository, two checkouts.\n",
    })
    linked = env.run_root / "home" / "work" / "dual-main-feature"
    worktree_add = env.run(["git", "worktree", "add", "--detach", "-q", str(linked), "HEAD"], cwd=main, timeout=20)
    if worktree_add.returncode != 0:
        raise RuntimeError(f"git worktree add failed: {worktree_add.stderr.strip()}")
    (linked / "oberth.yaml").write_text(
        manifest("dual-main@wt", [
            {"name": "api", "cmd": "python3 app.py", "port": linked_port, "health": "/health"},
        ]),
        encoding="utf-8",
    )
    (linked / "app.py").write_text(
        http_server(linked_port, {"/health": 200}, "linked api listening on %d" % linked_port),
        encoding="utf-8",
    )
    env.projects.append(linked)
    proc, doc = env.up(main)
    if proc.returncode != 0 or not up_started(doc, "api"):
        detail = (proc.stderr or proc.stdout or "").strip()
        raise RuntimeError(f"could not pre-start the main worktree: {detail}")
    main_pid = up_pid(doc, "api")
    preserve_pids = {("main", "api"): main_pid} if isinstance(main_pid, int) else {}
    return ScenarioState(
        worktrees={"main": main, "wt": linked},
        primary=linked,
        ports={"main_api": main_port, "wt_api": linked_port},
        preserve=[("main", "api")],
        preserve_pids=preserve_pids,
    )


def _judge_dual_worktree(probe: Probe, state: ScenarioState) -> Verdict:
    main_doc = probe.statuses.get("main")
    wt_doc = probe.statuses.get("wt")
    checks = [
        check("linked worktree api is running", running(wt_doc, "api"), _status_detail(wt_doc, "api")),
        check("linked worktree api is healthy", healthy(wt_doc, "api"), _status_detail(wt_doc, "api")),
        check("main worktree api is still running", running(main_doc, "api"), _status_detail(main_doc, "api")),
        check("main worktree api is still healthy", healthy(main_doc, "api"), _status_detail(main_doc, "api")),
        check("linked /health returns 200", probe.http["wt_health"].status == 200, _http_detail(probe.http["wt_health"])),
        check("main /health returns 200", probe.http["main_health"].status == 200, _http_detail(probe.http["main_health"])),
        check("linked status reports its own port", port_actual(wt_doc, "api") == state.ports["wt_api"],
              f"port_actual={port_actual(wt_doc, 'api')!r}"),
        check("main status reports its own port", port_actual(main_doc, "api") == state.ports["main_api"],
              f"port_actual={port_actual(main_doc, 'api')!r}"),
    ]
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"main_status": main_doc, "linked_status": wt_doc, "http": _http_evidence(probe.http)},
    )


# --------------------------------------------------------------------------- #
# Helpers shared by judgments


def _status_detail(doc: dict[str, Any] | None, name: str) -> str:
    item = service(doc, name)
    if item is None:
        return "service not present in status"
    return (
        f"running={item.get('running')} port_actual={item.get('port_actual')} "
        f"health={item.get('health_status')} last_exit={item.get('last_exit')}"
    )


def _http_detail(result: HttpResult) -> str:
    return f"status={result.status} error={result.error!r} body={result.body[:200]!r}"


def _http_evidence(http: dict[str, HttpResult]) -> dict[str, Any]:
    return {name: {"status": result.status, "error": result.error} for name, result in http.items()}


def _urls_missing_artifact(state: ScenarioState) -> dict[str, str]:
    port = state.ports["api"]
    return {
        "health": f"http://127.0.0.1:{port}/health",
        "rate_limits": f"http://127.0.0.1:{port}/rate-limits",
    }


def _urls_dependency(state: ScenarioState) -> dict[str, str]:
    return {
        "seed_ready": f"http://127.0.0.1:{state.ports['seed']}/ready",
        "api_health": f"http://127.0.0.1:{state.ports['api']}/health",
    }


def _urls_mismatch(state: ScenarioState) -> dict[str, str]:
    port = state.ports["api"]
    return {
        "health": f"http://127.0.0.1:{port}/health",
        "ready": f"http://127.0.0.1:{port}/ready",
    }


def _urls_dual(state: ScenarioState) -> dict[str, str]:
    return {
        "main_health": f"http://127.0.0.1:{state.ports['main_api']}/health",
        "wt_health": f"http://127.0.0.1:{state.ports['wt_api']}/health",
    }


def _urls_stale(state: ScenarioState) -> dict[str, str]:
    port = state.ports["api"]
    return {
        "health": f"http://127.0.0.1:{port}/health",
        "ready": f"http://127.0.0.1:{port}/ready",
    }


def _urls_empty(state: ScenarioState) -> dict[str, str]:
    _ = state
    return {}


# ``ScenarioEnv`` exposes the system path; fixture ports come from the same
# helper so every scenario picks a currently free port.
def env_free_port() -> int:
    from agent_eval_lib import free_port
    return free_port()


SCENARIOS: dict[str, Scenario] = {
    "missing-artifact": Scenario(
        id="missing-artifact",
        title="生成物缺失",
        prompt=(
            "这个 worktree 的服务现在没在跑。把它跑起来并确认它真的在运行；"
            "起不来就查清原因并解决。不要修改 oberth.yaml。"
            "做完后用一句话说明你确认到的运行事实。"
        ),
        setup=_setup_missing_artifact,
        judge=_judge_missing_artifact,
        urls=_urls_missing_artifact,
        reference=lambda state: [
            ["python3", "scripts/generate_endpoints.py"],
            ["oberth", "up", "--wait", "--wait-timeout", "25s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="The service exits 1 because a gitignored generated file is absent.",
    ),
    "port-occupied": Scenario(
        id="port-occupied",
        title="端口被占",
        prompt=(
            "这个项目的服务现在没在跑。把它跑起来并确认它真的在运行；如果起不来，查清原因。"
            "不要改动不属于这个项目的进程或端口，也不要修改 oberth.yaml。"
        ),
        setup=_setup_port_occupied,
        judge=_judge_port_occupied,
        urls=_urls_empty,
        reference=lambda state: [
            ["oberth", "up", "--wait", "--wait-timeout", "10s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="Another process owns the declared fixed port; the correct outcome is evidence, not a kill.",
    ),
    "dependency-not-ready": Scenario(
        id="dependency-not-ready",
        title="依赖未就绪",
        prompt=(
            "这个项目的 api 现在起不来。查清原因并解决，让 api 真正运行且健康。"
            "不要修改 oberth.yaml；machine: 里的依赖不归这个项目起停。"
        ),
        setup=_setup_dependency_not_ready,
        judge=_judge_dependency_not_ready,
        urls=_urls_dependency,
        reference=lambda state: [
            ["python3", "scripts/make_seed.py"],
            ["oberth", "up", "--wait", "--wait-timeout", "25s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="A declared dependency needs generated data; a machine dependency is deliberately absent.",
    ),
    "manifest-process-mismatch": Scenario(
        id="manifest-process-mismatch",
        title="清单与进程不一致",
        prompt=(
            "这个项目里的服务正在运行，但可能不是按当前 oberth.yaml 起的。"
            "让实际运行的实例反映当前清单；不要改清单，也不要手动杀进程。"
        ),
        setup=_setup_manifest_mismatch,
        judge=_judge_manifest_mismatch,
        urls=_urls_mismatch,
        reference=lambda state: [
            ["oberth", "restart", "--only", "api", "--json"],
            ["oberth", "up", "--wait", "--wait-timeout", "25s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="A running process predates a manifest edit; it must be reconciled without editing the manifest.",
    ),
    "stale-code": Scenario(
        id="stale-code",
        title="代码已改但进程未重启",
        prompt=(
            "这个项目的服务正在运行，但刚刚改过它的代码，运行中的进程可能还是旧版本。"
            "让实际运行反映当前代码；不要修改 oberth.yaml，也不要手动杀进程。"
            "做完后用一句话说明你确认到的代码与运行事实。"
        ),
        setup=_setup_stale_code,
        judge=_judge_stale_code,
        urls=_urls_stale,
        reference=lambda state: [
            ["oberth", "restart", "--only", "api", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="The source changed after the process started; only a restart makes the new behavior live.",
    ),
    "dual-worktree": Scenario(
        id="dual-worktree",
        title="同仓库双 worktree",
        prompt=(
            "这是同一个仓库的另一个 worktree，服务还没起来。把这个 worktree 的服务跑起来并确认它属于这份 checkout；"
            "不要影响另一个 worktree 上已经在跑的服务，也不要修改任何 oberth.yaml。"
        ),
        setup=_setup_dual_worktree,
        judge=_judge_dual_worktree,
        urls=_urls_dual,
        reference=lambda state: [
            ["oberth", "up", "--wait", "--wait-timeout", "25s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="Two checkouts of one repository must keep their services and ports separate.",
    ),
}
