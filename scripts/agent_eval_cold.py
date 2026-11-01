"""Cold-start scenarios: a repository that has no ``oberth.yaml`` yet.

The other two suites hand the agent a manifest that already declares the
services. These hand it a checkout that is a plausible real project and nothing
else — no ``oberth.yaml``, no draft waiting in ``BERTH_HOME``. The question is
the one a new user asks: from "I have this repository" to "its services are
healthy". The path is measured end to end:

    draft -> adopt -> up --wait

How the project starts is stated by the repository's own files and never in
prose: a ``Makefile`` with the project's targets, an npm ``workspaces`` list
plus the members' ``scripts``, a multi-module ``pom.xml`` and a ``README`` that
names the build goal. The process the agent has to launch goes through tools
the fixture ships — a package-manager front end, a Maven wrapper, a compose
project's own bootstrap script — so the task is readable by any coding agent
and works identically for a control arm with no ``oberth`` at all.

Three properties shape this module:

**The harness adopts, not a person.** Everywhere else a person has already
declared the services. Here nothing exists until the run's own harness adopts
what the agent drafted. Adoption is mechanical and gated: ``init adopt
--dry-run`` has to pass before the draft is written, and the run record says
``adopted_by: "harness"``. The judge never checks who wrote the manifest — only
what the services do and whether the rest of the checkout survived.

**A cold start pays for two model calls.** ``init draft`` starts a second agent
process inside the run, so one observation costs the measured run plus one
drafting run. ``budget_note()`` states that, and the report carries it, so a
token budget that assumes one call per run is never right for this suite.

**The manifest is the one file a correct run may change.** Editing a tracked
source file or rewriting Git history is a violation, not a step.
"""

from __future__ import annotations

import json
from pathlib import Path
from typing import Any, Callable

from agent_eval_lib import (
    HttpResult,
    Probe,
    ScenarioEnv,
    ScenarioState,
    Verdict,
    check,
    free_port,
    healthy,
    port_actual,
    running,
    service,
    up_pid,
)
from agent_eval_scenarios import Scenario

SUITE = "cold"

# The agent process, plus the drafting run `init draft` starts inside the run.
AGENT_CALLS_PER_OBSERVATION = 2

# A cold start has to discover the project, declare it, and only then start it.
# The budget is larger than the core scenarios for that reason, and it is never
# a correctness threshold: going over it costs points, nothing else.
DRAFT_BUDGET = 40

# A cold start runs a build step before the first service listens, so the wait
# budget has to cover it.
WAIT_TIMEOUT = "60s"
UP_TIMEOUT = 150.0


# --------------------------------------------------------------------------- #
# Tools the fixtures ship
#
# These are ordinary repository files, not harness tricks: a Maven wrapper and a
# compose project's bootstrap script are both things a real repository checks
# in. They exist so the agent's own commands and the service's start command go
# through one code path, and so a control arm without ``oberth`` can start the
# project by hand from the README. Python only — the harness may depend on the
# standard library alone.


NPM_SHIM = '''#!/usr/bin/env python3
"""Run one script of this workspace, resolving `-w <name>` against packages/<name>.

This is the repository's own front end for `npm run <target>`, so the start
command in a manifest and the command the README documents are the same one.
"""
import json
import os
import subprocess
import sys

argv = sys.argv[1:]
if not argv or argv[0] != "run":
    sys.stderr.write("run-npm: only `run <target> [-w <workspace>]` is available\\n")
    raise SystemExit(2)
rest = argv[1:]
workspace = None
if "-w" in rest:
    index = rest.index("-w")
    try:
        workspace = rest[index + 1]
    except IndexError:
        sys.stderr.write("run-npm: -w needs a workspace name\\n")
        raise SystemExit(2)
    rest = rest[:index] + rest[index + 2:]
if not rest:
    sys.stderr.write("run-npm: run needs a target\\n")
    raise SystemExit(2)
target = rest[0]
root = os.getcwd()
if workspace:
    root = os.path.join(root, "packages", workspace)
manifest_path = os.path.join(root, "package.json")
try:
    with open(manifest_path, encoding="utf-8") as handle:
        scripts = json.load(handle).get("scripts") or {}
except OSError:
    sys.stderr.write("run-npm: no package.json at %s\\n" % root)
    raise SystemExit(1)
command = scripts.get(target)
if not command:
    sys.stderr.write("run-npm: no script %r in %s\\n" % (target, manifest_path))
    raise SystemExit(1)
raise SystemExit(subprocess.call(command, shell=True, cwd=root))
'''

MVNW = '''#!/usr/bin/env python3
"""Build and run one module of this reactor.

`-pl <module>` selects a module and `-am` brings in the modules it needs.
`install` runs each required module's build step in declaration order;
`spring-boot:run` executes the selected module's run script in the foreground,
which is what a long-lived service does.
"""
import json
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
argv = sys.argv[1:]
if not (ROOT / "pom.xml").is_file():
    sys.stderr.write("mvnw: no pom.xml next to this wrapper\\n")
    raise SystemExit(1)


def option(name):
    if name not in argv:
        return None
    index = argv.index(name)
    try:
        return argv[index + 1]
    except IndexError:
        sys.stderr.write("mvnw: %s needs a value\\n" % name)
        raise SystemExit(2)


module = option("-pl")
goal = next((part for part in argv if part in ("install", "spring-boot:run")), "")
if not module or not goal:
    sys.stderr.write("mvnw: this wrapper serves -pl <module> with install or spring-boot:run\\n")
    raise SystemExit(2)
module_dir = ROOT / module
if not (module_dir / "pom.xml").is_file():
    sys.stderr.write("mvnw: no module %s\\n" % module)
    raise SystemExit(1)
if goal == "install":
    required = ["catalog", "billing"]
    ordered = required if "-am" in argv else [module]
    for name in ordered:
        build = ROOT / name / "build.py"
        if build.is_file():
            code = subprocess.call([sys.executable, str(build)], cwd=str(ROOT / name))
            if code != 0:
                raise SystemExit(code)
    raise SystemExit(0)
run = module_dir / "run.py"
if not run.is_file():
    sys.stderr.write("mvnw: module %s has no run script\\n" % module)
    raise SystemExit(1)
raise SystemExit(subprocess.call([sys.executable, str(run)], cwd=str(module_dir)))
'''

COMPOSE_TOOL = '''#!/usr/bin/env python3
"""This compose project's bootstrap: render the env file, then start services.

`setup` renders config/app.env from the checked-in example — the file is
generated, so it is not committed. `up -d <service>...` starts one service's
command in the background with that env file, and `down` stops what this
project's state file says is running.
"""
import json
import os
import signal
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
STATE = ROOT / ".compose-state.json"


def render_env():
    example = ROOT / "config" / "app.env.example"
    if not example.is_file():
        print("missing config/app.env.example", file=sys.stderr)
        return 1
    target = ROOT / "config" / "app.env"
    target.write_text(example.read_text())
    print("wrote %s" % target.relative_to(ROOT))
    return 0


def load_state():
    try:
        return json.loads(STATE.read_text())
    except (OSError, json.JSONDecodeError):
        return {}


def save_state(state):
    STATE.write_text(json.dumps(state))


def alive(pid):
    try:
        os.kill(pid, 0)
        return True
    except OSError:
        return False


def up(names):
    env_path = ROOT / "config" / "app.env"
    if not env_path.is_file():
        print("run `scripts/dev.py setup` first: config/app.env is generated", file=sys.stderr)
        return 1
    state = load_state()
    for name in names:
        if name in state and alive(state[name]):
            print("%s is already up (pid %d)" % (name, state[name]))
            continue
        command = ROOT / name / "start.sh"
        if not command.is_file():
            print("no service %s" % name, file=sys.stderr)
            return 1
        proc = subprocess.Popen(
            [str(command)], cwd=str(ROOT / name),
            env={**os.environ, "ENV_FILE": str(env_path)},
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            start_new_session=True,
        )
        state[name] = proc.pid
        print("started %s (pid %d)" % (name, proc.pid))
    save_state(state)
    return 0


def down():
    state = load_state()
    for name, pid in state.items():
        try:
            os.killpg(os.getpgid(pid), signal.SIGTERM)
            print("stopped %s (pid %d)" % (name, pid))
        except OSError:
            pass
    save_state({})
    return 0


def main():
    argv = sys.argv[1:]
    if not argv:
        print("usage: dev.py setup | up -d <service>... | down", file=sys.stderr)
        return 2
    if argv[0] == "setup":
        return render_env()
    if argv[0] == "up":
        return up([part for part in argv[1:] if not part.startswith("-")])
    if argv[0] == "down":
        return down()
    print("unknown command: %s" % argv[0], file=sys.stderr)
    return 2


if __name__ == "__main__":
    raise SystemExit(main())
'''

START_SH = '''#!/bin/sh
# One service of this compose project. Configuration comes from the rendered
# env file; the process stays in the foreground.
set -e
ENV_FILE="${ENV_FILE:-$(pwd)/../config/app.env}"
if [ ! -f "$ENV_FILE" ]; then
  echo "$0: configuration is missing; run scripts/dev.py setup" >&2
  exit 1
fi
set -a
. "$ENV_FILE"
set +a
exec python3 app.py
'''



# --------------------------------------------------------------------------- #
# Fixtures


def _json(value: Any) -> str:
    return json.dumps(value, indent=2) + "\n"


def _project(env: ScenarioEnv, name: str, files: dict[str, str],
             executable: tuple[str, ...] = ()) -> Path:
    """Write a fixture and mark the scripts a person would run as executable.

    ``write_project`` commits what it wrote, so the executable bit has to be
    set before that happens; the fixture is therefore written twice — once by
    ``write_project`` without git, once after the bit is set.
    """
    root = env.write_project(name, files, git=False)
    for rel in executable:
        (root / rel).chmod(0o755)
    env.git_init(root)
    return root


def _http_service(name: str, *, body_key: str = "service", require: tuple[str, ...] = (),
                  require_file: str | None = None) -> str:
    """A JSON service: /health is 200 only when its declared inputs are ready.

    It binds ``PORT``: the address book hands every service in a manifest a port
    through ``PORT``/``BERTH_PORT``, so a manifest writes ``port: 8000`` without
    the command having to hard-code it. A native-arm run gets the same variable
    by exporting it, which is what the README documents.
    """
    return (
        "import json\n"
        "import os\n"
        "import sys\n"
        "from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer\n"
        "from urllib.request import ProxyHandler, build_opener\n\n"
        f"NAME = {json.dumps(name)}\n"
        "PORT = int(os.environ['PORT'])\n"
        f"REQUIRE = {list(require)!r}\n"
        f"REQUIRE_FILE = {require_file!r}\n"
        "OPENER = build_opener(ProxyHandler({}))\n\n"
        "if REQUIRE_FILE and not os.path.exists(REQUIRE_FILE):\n"
        "    print('%s: %s is missing; run the documented setup step' % (NAME, REQUIRE_FILE),\n"
        "          file=sys.stderr, flush=True)\n"
        "    sys.exit(1)\n\n\n"
        "def waiting():\n"
        "    out = []\n"
        "    for url in REQUIRE:\n"
        "        try:\n"
        "            with OPENER.open(url, timeout=1.0) as response:\n"
        "                if response.status != 200:\n"
        "                    out.append(url)\n"
        "        except Exception:\n"
        "            out.append(url)\n"
        "    return out\n\n\n"
        "class Handler(BaseHTTPRequestHandler):\n"
        "    def do_GET(self):\n"
        "        path = self.path.split('?')[0]\n"
        "        blocking = waiting()\n"
        "        if path == '/health' and blocking:\n"
        "            code, body = 503, {'ok': False, 'waiting_on': blocking}\n"
        "        elif path in ('/', '/health'):\n"
        f"            code, body = 200, {{'ok': True, '{body_key}': NAME, 'pid': os.getpid()}}\n"
        "        else:\n"
        "            code, body = 404, {'ok': False, 'path': path}\n"
        "        data = json.dumps(body).encode()\n"
        "        self.send_response(code)\n"
        "        self.send_header('Content-Type', 'application/json')\n"
        "        self.send_header('Content-Length', str(len(data)))\n"
        "        self.end_headers()\n"
        "        self.wfile.write(data)\n\n"
        "    def log_message(self, fmt, *args):\n"
        "        print(fmt % args, flush=True)\n\n\n"
        "print('%s on %d' % (NAME, PORT), flush=True)\n"
        "ThreadingHTTPServer(('127.0.0.1', PORT), Handler).serve_forever()\n"
    )


# --------------------------------------------------------------------------- #
# Fixtures
#
# Every README below documents the whole path a person would take, including
# "export the port, then start it" — that is what makes the same repository
# startable by an agent with no `oberth` at all. A manifest only has to say
# `port:` and hand over the command; the engine's own address book exports
# `PORT` for the service, which is why the commands read `$PORT` instead of a
# baked-in number.


def _setup_compose(env: ScenarioEnv) -> ScenarioState:
    """A compose project whose env file is rendered from an example first."""
    api_port, worker_port = free_port(), free_port()
    root = _project(env, "ledger-compose", {
        "README.md": (
            "# ledger-compose\n\n"
            "Two services, `api` and `worker`. The shipped `compose.yaml` reads\n"
            "`config/app.env`, which is generated from the checked-in example and is\n"
            "therefore not in the repository. Render it once:\n\n"
            "```sh\n"
            "python3 scripts/dev.py setup\n"
            "```\n\n"
            "Then start a service in the foreground. It binds `$PORT`, which compose\n"
            "takes from the env file and a supervisor is expected to provide:\n\n"
            "```sh\n"
            f"export PORT={api_port}; python3 api/app.py      # api\n"
            f"export PORT={worker_port}; python3 worker/app.py  # worker\n"
            "```\n\n"
            "`python3 scripts/dev.py up -d api worker` starts both in the background, and\n"
            "`python3 scripts/dev.py down` stops them. `platform-db` is the shared MySQL\n"
            "this checkout reads but does not own.\n"
        ),
        ".gitignore": "config/app.env\n.compose-state.json\n",
        "config/app.env.example": (
            f"API_PORT={api_port}\nWORKER_PORT={worker_port}\n"
        ),
        "compose.yaml": (
            "services:\n"
            "  api:\n"
            "    build: ./api\n"
            "    env_file: config/app.env\n"
            "    ports: [\"${API_PORT}:8000\"]\n"
            "  worker:\n"
            "    build: ./worker\n"
            "    env_file: config/app.env\n"
            "    ports: [\"${WORKER_PORT}:8001\"]\n"
        ),
        "Makefile": (
            ".PHONY: setup up down\n\n"
            "setup:\n\tpython3 scripts/dev.py setup\n\n"
            "up: setup\n\tpython3 scripts/dev.py up -d api worker\n\n"
            "down:\n\tpython3 scripts/dev.py down\n"
        ),
        "scripts/dev.py": COMPOSE_TOOL,
        "api/app.py": _http_service("api"),
        "worker/app.py": _http_service("worker"),
    }, executable=("scripts/dev.py",))
    return ScenarioState(worktrees={"main": root}, primary=root,
                         ports={"api": api_port, "worker": worker_port})


def _setup_node_monorepo(env: ScenarioEnv) -> ScenarioState:
    """An npm-workspaces monorepo: install, then three members with two edges."""
    names = ("gateway", "identity", "scheduler")
    ports = {name: free_port() for name in names}
    upstream = {"identity": (), "gateway": ("identity",), "scheduler": ("gateway",)}
    files: dict[str, str] = {
        "README.md": (
            "# workshop-monorepo\n\n"
            "Three npm workspaces under `packages/`. Install once:\n\n"
            "```sh\n"
            "python3 scripts/install.py install\n"
            "```\n\n"
            "Then run a member in the foreground. Each one binds `$PORT`:\n\n"
            "```sh\n"
            f"export PORT={ports['gateway']}; tools/run-npm run dev -w gateway\n"
            f"export PORT={ports['identity']}; tools/run-npm run dev -w identity\n"
            f"export PORT={ports['scheduler']}; tools/run-npm run dev -w scheduler\n"
            "```\n\n"
            "`gateway` needs `identity`; `scheduler` needs `gateway`. A member that has\n"
            "not been installed yet exits with a message saying so.\n"
        ),
        ".gitignore": "node_modules/\n",
        "package.json": _json({
            "name": "workshop-monorepo", "private": True, "workspaces": ["packages/*"],
        }),
        "scripts/install.py": (
            "#!/usr/bin/env python3\n"
            '"""Install this monorepo\'s workspaces."""\n'
            "import sys\n"
            "from pathlib import Path\n\n"
            "ROOT = Path(__file__).resolve().parent.parent\n\n\n"
            "def main():\n"
            "    argv = [part for part in sys.argv[1:] if not part.startswith('-')]\n"
            "    if not argv or argv[0] != 'install':\n"
            "        print('usage: scripts/install.py install [workspace ...]', file=sys.stderr)\n"
            "        return 2\n"
            "    names = argv[1:]\n"
            "    if not names:\n"
            "        names = sorted(part.name for part in (ROOT / 'packages').iterdir()\n"
            "                       if (part / 'package.json').is_file())\n"
            "    for name in names:\n"
            "        member = ROOT / 'packages' / name\n"
            "        if not (member / 'package.json').is_file():\n"
            "            print('no such workspace: %s' % name, file=sys.stderr)\n"
            "            return 1\n"
            "        (member / 'node_modules').mkdir(exist_ok=True)\n"
            "        (member / 'node_modules' / '.installed').write_text('ok\\n')\n"
            "    print('installed: %s' % ', '.join(sorted(names)))\n"
            "    return 0\n\n\n"
            "if __name__ == '__main__':\n"
            "    raise SystemExit(main())\n"
        ),
        "tools/run-npm": NPM_SHIM,
    }
    for name in names:
        require = tuple(f"http://127.0.0.1:{ports[dep]}/health" for dep in upstream[name])
        files[f"packages/{name}/package.json"] = _json({
            "name": "@workshop/" + name,
            "private": True,
            "scripts": {"dev": "python3 server.py"},
        })
        files[f"packages/{name}/server.py"] = _http_service(
            name, require=require, require_file="node_modules/.installed",
        )
    root = _project(env, "workshop-monorepo", files,
                    executable=("scripts/install.py", "tools/run-npm"))
    return ScenarioState(worktrees={"main": root}, primary=root, ports=dict(ports))


def _setup_maven_modules(env: ScenarioEnv) -> ScenarioState:
    """A Maven reactor: two libraries the API needs, built through the wrapper."""
    names = ("catalog", "billing", "api")
    ports = {name: free_port() for name in names}
    upstream = {"catalog": (), "billing": (), "api": ("catalog", "billing")}
    files: dict[str, str] = {
        "README.md": (
            "# orders-service\n\n"
            "A reactor of three modules. `api` is the service; `catalog` and `billing` are\n"
            "the libraries it calls. Build them, then run one in the foreground. Each one\n"
            "binds `$PORT`:\n\n"
            "```sh\n"
            "./mvnw -q -DskipTests -pl api -am install\n"
            f"./mvnw -pl catalog spring-boot:run            # export PORT={ports['catalog']} first\n"
            f"./mvnw -pl billing spring-boot:run            # export PORT={ports['billing']} first\n"
            f"./mvnw -pl api spring-boot:run                # export PORT={ports['api']} first\n"
            "```\n\n"
            "A module that has not been built yet exits with a message saying so. `api`\n"
            "answers 503 on `/health` until both libraries answer. `postgres` is the\n"
            "machine's database: this project reads it and never starts it.\n"
        ),
        ".gitignore": "target/\n*.class\n",
        "pom.xml": (
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            "<project>\n"
            '  <modelVersion>4.0.0</modelVersion>\n'
            "  <groupId>com.example</groupId>\n"
            "  <artifactId>orders</artifactId>\n"
            "  <packaging>pom</packaging>\n"
            "  <modules>\n"
            "    <module>catalog</module>\n"
            "    <module>billing</module>\n"
            "    <module>api</module>\n"
            "  </modules>\n"
            "</project>\n"
        ),
        "mvnw": MVNW,
    }
    for name in names:
        files[f"{name}/pom.xml"] = (
            '<?xml version="1.0" encoding="UTF-8"?>\n'
            "<project>\n"
            '  <modelVersion>4.0.0</modelVersion>\n'
            "  <groupId>com.example</groupId>\n"
            f"  <artifactId>{name}</artifactId>\n"
            "</project>\n"
        )
    for name in ("catalog", "billing"):
        files[f"{name}/build.py"] = (
            '"""Compile this module."""\n'
            "import pathlib\n\n"
            "here = pathlib.Path(__file__).resolve().parent\n"
            "target = here / 'target'\n"
            "target.mkdir(exist_ok=True)\n"
            "(target / 'classes.txt').write_text('built\\n')\n"
            "print('built %s' % here.name)\n"
        )
    for name in names:
        require = tuple(f"http://127.0.0.1:{ports[dep]}/health" for dep in upstream[name])
        files[f"{name}/run.py"] = _http_service(
            name, body_key="module", require=require,
            require_file=None if name == "api" else "target/classes.txt",
        )
    root = _project(env, "orders-service", files, executable=("mvnw",))
    return ScenarioState(worktrees={"main": root}, primary=root, ports=dict(ports))


# --------------------------------------------------------------------------- #
# Judgement
#
# All three shapes are judged on the same facts, so a difference between two
# rows is a difference in the repository and never in the judge:
#
#   * a draft was produced and the harness adopted it;
#   * every expected service is declared, running and healthy;
#   * each one runs on the port its repository declares;
#   * each one answers /health with 200;
#   * only oberth.yaml changed, and Git history is intact.


KEYS = {
    "cold-compose": ("api", "worker"),
    "cold-node-monorepo": ("gateway", "identity", "scheduler"),
    "cold-maven-modules": ("api", "catalog", "billing"),
}


def _manifest_checks(state: ScenarioState) -> list[dict[str, Any]]:
    """A manifest exists and declares services — the outcome, not the route.

    The judge deliberately does not ask *who* drafted or adopted it: the
    decision for this suite is to score the final state and the untouched
    tracked files, so an agent that reached a correct manifest its own way is
    measured on what it produced. Whether the harness had to adopt is recorded
    in ``adopted_by`` for the report, not scored here.
    """
    manifest = state.primary / "oberth.yaml"
    if not manifest.is_file():
        return [check("an oberth.yaml exists", False, f"{manifest} was not written")]
    text = manifest.read_text(encoding="utf-8")
    return [
        check("an oberth.yaml exists", True),
        check("the manifest declares services rather than an empty list",
              "services: []" not in text, "oberth.yaml still has `services: []`"),
    ]


def _service_checks(probe: Probe, state: ScenarioState, names: tuple[str, ...]) -> list[dict[str, Any]]:
    doc = probe.statuses.get("main")
    declared = {item.get("name") for item in (doc.get("worktree") or {}).get("services") or []} \
        if isinstance(doc, dict) else set()
    checks = []
    for name in names:
        item = service(doc, name)
        checks.append(check(f"{name} is declared", name in declared, f"declared={sorted(declared)}"))
        checks.append(check(f"{name} is running", running(doc, name), _detail(item)))
        checks.append(check(f"{name} is healthy", healthy(doc, name), _detail(item)))
        expected = state.ports[name]
        checks.append(check(
            f"{name} runs on the port its repository declares",
            port_actual(doc, name) == expected,
            f"port_actual={port_actual(doc, name)!r} expected={expected}",
        ))
        result = probe.http.get(f"{name}_health") or HttpResult(None, "not requested")
        checks.append(check(
            f"{name} answers /health with 200",
            result.status == 200,
            f"status={result.status} error={result.error!r} body={result.body[:160]!r}",
        ))
    return checks


def _boundary_checks(probe: Probe, state: ScenarioState) -> list[dict[str, Any]]:
    """The manifest is the one file a cold start may add or change.

    Both facts are checked per worktree, and the labels come from the union of
    what the probe observed: a run that only rewrote history and left every file
    alone still has to fail, so the loop cannot be driven by the diff alone.
    """
    checks = []
    for label in sorted({*probe.diffs, *probe.git_changes}):
        diff = probe.diffs.get(label)
        others = [path for path in (diff.tracked_changed if diff else [])
                  if Path(path).name != "oberth.yaml"]
        checks.append(check(
            f"{label}: no tracked file other than oberth.yaml changed",
            not others, "; ".join(others), kind="safety",
        ))
        changes = probe.git_changes.get(label) or []
        checks.append(check(
            f"{label}: Git history was not rewritten",
            not changes, "; ".join(changes), kind="safety",
        ))
    return checks


def _detail(item: dict[str, Any] | None) -> str:
    if item is None:
        return "service not present in status"
    health = item.get("health_status") or {}
    return (f"running={item.get('running')} port_actual={item.get('port_actual')} "
            f"health={health.get('status')!r} last_exit={item.get('last_exit')}")


def _judge(scenario_id: str) -> Callable[[Probe, ScenarioState], Verdict]:
    def judge(probe: Probe, state: ScenarioState) -> Verdict:
        checks = _manifest_checks(state)
        checks.extend(_service_checks(probe, state, KEYS[scenario_id]))
        checks.extend(_boundary_checks(probe, state))
        return Verdict(
            completed=all(item["ok"] for item in checks),
            checks=checks,
            evidence={"status": probe.statuses.get("main")},
        )

    return judge


# --------------------------------------------------------------------------- #
# URLs


def _urls(state: ScenarioState, names: tuple[str, ...]) -> dict[str, str]:
    return {f"{name}_health": f"http://127.0.0.1:{state.ports[name]}/health" for name in names}


# --------------------------------------------------------------------------- #
# The reference flow
#
# It proves the fixture supports the three stages a person would take, and it
# is an oberth-arm self-check: the same judge scores it as a real agent.


def _reference() -> Callable[[ScenarioState], list[list[str]]]:
    """Adopt the reference draft, then start it and read the result.

    There is no `init draft` in this list: the reference arm runs no model, so
    its draft is the one written by ``write_reference_draft`` — the manifest a
    person who knew this repository would write. Adoption still goes through
    `init adopt`, so the path after the draft is the real one; what the
    reference arm does not prove is the drafting step, and the docs say so.
    """
    return lambda state: [
        ["oberth", "init", "adopt", "--replace", "--json"],
        ["oberth", "up", "--wait", "--wait-timeout", WAIT_TIMEOUT, "--json"],
        ["oberth", "status", "--no-mark", "--json"],
    ]


SCENARIOS: dict[str, Scenario] = {
    "cold-compose": Scenario(
        id="cold-compose",
        title="冷启动：compose 项目",
        prompt=(
            "这个仓库还没有 oberth.yaml。我想用 oberth 管理它的服务：先声明服务，"
            "再让它们真的跑起来并确认健康。仓库里的 README 和 Makefile 写了这个项目怎么起。"
            "除了 oberth.yaml，不要改动已跟踪的文件。"
            "做完后用一句话说明你确认到的运行事实。"
        ),
        setup=_setup_compose,
        judge=_judge("cold-compose"),
        urls=lambda state: _urls(state, KEYS["cold-compose"]),
        reference=_reference(),
        description="A compose project whose env file is rendered before either service starts.",
        suite=SUITE,
        budget=DRAFT_BUDGET,
    ),
    "cold-node-monorepo": Scenario(
        id="cold-node-monorepo",
        title="冷启动：Node monorepo",
        prompt=(
            "这个仓库还没有 oberth.yaml。packages/ 下三个 npm workspace 的服务需要跑起来，"
            "依赖关系照仓库里的说明。用 oberth 声明并启动它们，确认都健康。"
            "除了 oberth.yaml，不要改动已跟踪的文件。"
            "做完后用一句话说明你确认到的运行事实。"
        ),
        setup=_setup_node_monorepo,
        judge=_judge("cold-node-monorepo"),
        urls=lambda state: _urls(state, KEYS["cold-node-monorepo"]),
        reference=_reference(),
        description="An npm-workspaces monorepo with an install step and two dependency edges.",
        suite=SUITE,
        budget=DRAFT_BUDGET,
    ),
    "cold-maven-modules": Scenario(
        id="cold-maven-modules",
        title="冷启动：Maven 多模块",
        prompt=(
            "这个仓库还没有 oberth.yaml。这是一个 Maven 多模块项目，api 是对外服务，"
            "另外两个模块是它依赖的库。按仓库里的说明构建并跑起来，确认 api 健康。"
            "除了 oberth.yaml，不要改动已跟踪的文件。"
            "做完后用一句话说明你确认到的运行事实。"
        ),
        setup=_setup_maven_modules,
        judge=_judge("cold-maven-modules"),
        urls=lambda state: _urls(state, KEYS["cold-maven-modules"]),
        reference=_reference(),
        description="A three-module Maven reactor where api needs the other two built first.",
        suite=SUITE,
        budget=DRAFT_BUDGET,
    ),
}


# --------------------------------------------------------------------------- #
# Cold-start accounting
#
# One observation is not one agent run: `init draft` starts a second agent
# process inside it. The report states that so a reader comparing provider spend
# does not read the extra call as a harness bug.


def budget_note() -> dict[str, Any]:
    return {
        "agent_calls_per_observation": AGENT_CALLS_PER_OBSERVATION,
        "why": (
            "`init draft` runs the configured agent once inside the run, so one "
            "cold-start observation costs the measured run plus one drafting run"
        ),
    }


def scenario_budgets() -> dict[str, Any]:
    """The per-scenario cost budget this suite declares, for the report."""
    return {scenario_id: scenario.budget for scenario_id, scenario in SCENARIOS.items()}


# --------------------------------------------------------------------------- #
# Reference drafts
#
# What a person who knew this repository would write. The reference arm uses it
# to prove the fixture supports the adopt -> up -> status path, and the Maven
# draft doubles as the multi-module case: `prepare: ./mvnw ...` is how a
# declaration says "these modules have to be built before this one runs".

COMPOSE_DRAFT = """name: ledger-compose
services:
  - name: api
    prepare: python3 scripts/dev.py setup
    cmd: python3 api/app.py
    port: {api_port}
    health: /health
  - name: worker
    cmd: python3 worker/app.py
    port: {worker_port}
    health: /health
machine:
  - name: platform-db
    port: 3306
"""

NODE_DRAFT = """name: workshop-monorepo
services:
  - name: identity
    prepare: python3 scripts/install.py install
    cmd: tools/run-npm run dev -w identity
    port: {identity_port}
    health: /health
  - name: gateway
    cmd: tools/run-npm run dev -w gateway
    port: {gateway_port}
    health: /health
    depends_on: [identity]
  - name: scheduler
    cmd: tools/run-npm run dev -w scheduler
    port: {scheduler_port}
    health: /health
    depends_on: [gateway]
"""

MAVEN_DRAFT = """name: orders-service
services:
  - name: catalog
    prepare: ./mvnw -q -DskipTests -pl catalog -am install
    cmd: ./mvnw -pl catalog spring-boot:run
    port: {catalog_port}
    health: /health
  - name: billing
    cmd: ./mvnw -pl billing spring-boot:run
    port: {billing_port}
    health: /health
  - name: api
    cmd: ./mvnw -pl api spring-boot:run
    port: {api_port}
    health: /health
    depends_on: [catalog, billing]
machine:
  - name: postgres
    port: 5432
"""


REFERENCE_DRAFTS = {
    "cold-compose": COMPOSE_DRAFT,
    "cold-node-monorepo": NODE_DRAFT,
    "cold-maven-modules": MAVEN_DRAFT,
}


def reference_draft(scenario_id: str) -> str:
    """The manifest the reference arm adopts for this fixture."""
    return REFERENCE_DRAFTS[scenario_id]


def write_reference_draft(env: ScenarioEnv, state: ScenarioState, template: str) -> Path:
    """Write this worktree's draft where `init adopt` looks for it.

    It is the same file a drafting agent would leave behind, so the reference
    flow adopts it through the ordinary command rather than around it.
    """
    path = draft_path(env, state)
    path.parent.mkdir(parents=True, exist_ok=True)
    values = {f"{name}_port": port for name, port in state.ports.items()}
    path.write_text(template.format(**values), encoding="utf-8")
    return path


# --------------------------------------------------------------------------- #
# Adoption
#
# The worktree's manifest is a person's decision, and in this suite the person
# is the harness acting on one fixed rule. It adopts only what already passes
# `init adopt --dry-run`, and it records that it did: nothing here decides what
# the services are, only whether a draft the agent produced can be written.

HARNESS_ADOPTER = "harness"
AGENT_ADOPTER = "agent"


def draft_path(env: ScenarioEnv, state: ScenarioState) -> Path:
    """Where `init adopt` with no argument looks for this worktree's draft.

    The key is the group name: the directory's base name for an ordinary
    checkout, which is all a cold-start fixture is.
    """
    return env.berth_home / "drafts" / f"{state.primary.name}.yaml"


def adopt_draft(env: ScenarioEnv, state: ScenarioState) -> dict[str, Any]:
    """Adopt the agent's draft when the run left one unadopted.

    Returns what happened, for the run record. A draft that fails the preview is
    not forced in: the run is scored on what its own draft could support.
    """
    manifest = state.primary / "oberth.yaml"
    if manifest.is_file():
        return {"drafted": draft_path(env, state).is_file(), "adopted_by": AGENT_ADOPTER}
    draft = draft_path(env, state)
    if not draft.is_file():
        return {"drafted": False, "adopted_by": None,
                "note": "no draft was left for this worktree"}
    preview = env.oberth(state.primary, ["init", "adopt", "--replace", "--dry-run", "--json"], timeout=60)
    if preview.returncode != 0:
        return {"drafted": True, "adopted_by": None,
                "note": "init adopt --dry-run rejected the draft: "
                        + (preview.stderr or preview.stdout or "").strip()[-400:]}
    adopted = env.oberth(state.primary, ["init", "adopt", "--replace", "--json"], timeout=60)
    if adopted.returncode != 0:
        return {"drafted": True, "adopted_by": None,
                "note": "init adopt failed: " + (adopted.stderr or adopted.stdout or "").strip()[-400:]}
    return {"drafted": True, "adopted_by": HARNESS_ADOPTER}
