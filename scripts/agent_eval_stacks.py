"""Large-stack scenarios: ten services, slow dependencies, prepared artifacts,
migrated databases, portless workers and parallel worktrees.

The service scripts live in ``examples/stack-workshop`` so the public example
and the benchmark cannot drift. The benchmark writes its own ``oberth.yaml``
with fixed ports and literal URLs: the native control arm has no oberth to
resolve ``port: auto`` or ``${name.url}``, and both arms must be judged by the
same observable outcome.

Judges read HTTP responses, port state, effect files and preserved PIDs. They
do not require the agent's own `status` document, so an oberth-arm run and a
native-arm run answer the same question.
"""

from __future__ import annotations

import hashlib
import json
import time
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
    port_open,
    service,
    up_pid,
)
from agent_eval_scenarios import Scenario

EXAMPLE = Path(__file__).resolve().parent.parent / "examples" / "stack-workshop"
SOURCE_FILES = (
    "service.py", "worker.py", "build.py", "migrate.py", "schema.sql",
    "src/app.html", "variant.txt",
)
PORT_SERVICES = ("auth", "catalog", "api", "gateway", "web", "search", "billing", "ingest")
WORKER_SERVICES = ("worker", "notifier")
SERVICES = PORT_SERVICES + WORKER_SERVICES
API_AND_DEPS = ("auth", "catalog", "api")
HEARTBEAT_MAX_AGE_S = 60


# --------------------------------------------------------------------------- #
# Fixture construction


def _source_files() -> dict[str, str]:
    return {rel: (EXAMPLE / rel).read_text(encoding="utf-8") for rel in SOURCE_FILES}


def _quote(value: str) -> str:
    return json.dumps(value)


def _manifest(name: str, label: str, ports: dict[str, int], machine: dict[str, int],
              *, slow_auth: int = 0, extra_env: dict[str, dict[str, str]] | None = None) -> str:
    extra_env = extra_env or {}
    def url(service_name: str) -> str:
        return f"http://127.0.0.1:{ports[service_name]}"

    lines = [f"name: {name}", "services:"]

    def add(
        service_name: str,
        *,
        cmd: str = "python3 -u service.py",
        deps: tuple[str, ...] = (),
        port: bool = True,
        prepare: str | None = None,
        inputs: tuple[str, ...] = (),
        env: dict[str, str] | None = None,
    ) -> None:
        lines.append(f"  - name: {service_name}")
        if prepare:
            lines.append(f"    prepare: {prepare}")
        if inputs:
            lines.append("    prepare_inputs: [" + ", ".join(inputs) + "]")
        lines.append(f"    cmd: {cmd}")
        if port:
            lines.append(f"    port: {ports[service_name]}")
            lines.append("    health: /health")
        if deps:
            lines.append("    depends_on: [" + ", ".join(deps) + "]")
        merged = dict(env or {})
        merged.update(extra_env.get(service_name, {}))
        if merged:
            lines.append("    env:")
            for key, value in merged.items():
                lines.append(f"      {key}: {_quote(value)}")

    add("auth", env={
        "SERVICE_NAME": "auth",
        "READY_DELAY": str(slow_auth),
        "VERSION_FILE": "variant.txt",
    })
    add("catalog", env={"SERVICE_NAME": "catalog", "VERSION_FILE": "variant.txt"})
    add(
        "api",
        prepare="python3 migrate.py",
        inputs=("schema.sql",),
        deps=("auth", "catalog"),
        env={
            "SERVICE_NAME": "api",
            "DB_PATH": f".berth/{label}/app.db",
            "VERSION_FILE": "variant.txt",
            "REQUIRE": f"{url('auth')}/health,{url('catalog')}/health",
        },
    )
    add("gateway", deps=("api",), env={
        "SERVICE_NAME": "gateway",
        "VERSION_FILE": "variant.txt",
        "REQUIRE": f"{url('api')}/health",
    })
    add(
        "web",
        prepare="python3 build.py",
        inputs=("src/app.html", "variant.txt"),
        env={"SERVICE_NAME": "web", "DIST": "dist", "VERSION_FILE": "dist/version.txt"},
    )
    add("search", deps=("catalog",), env={
        "SERVICE_NAME": "search",
        "VERSION_FILE": "variant.txt",
        "REQUIRE": f"{url('catalog')}/health",
    })
    add("billing", env={"SERVICE_NAME": "billing", "VERSION_FILE": "variant.txt"})
    add("ingest", deps=("catalog",), env={
        "SERVICE_NAME": "ingest",
        "VERSION_FILE": "variant.txt",
    })
    for worker in WORKER_SERVICES:
        add(
            worker,
            cmd="python3 -u worker.py",
            deps=("api",),
            port=False,
            env={
                "SERVICE_NAME": worker,
                "API_URL": url("api"),
                "HEARTBEAT": f".berth/{label}/{worker}.heartbeat",
                "INTERVAL": "0.5",
            },
        )
    lines.append("machine:")
    for machine_name, machine_port in machine.items():
        lines.append(f"  - name: {machine_name}")
        lines.append(f"    port: {machine_port}")
    return "\n".join(lines) + "\n"


def _allocate_ports() -> dict[str, int]:
    return {name: free_port() for name in PORT_SERVICES}


def _machine_ports() -> dict[str, int]:
    return {"postgres": free_port(), "redis": free_port()}


def _project_files(label: str, ports: dict[str, int], machine: dict[str, int],
                   *, slow_auth: int = 0,
                   extra_env: dict[str, dict[str, str]] | None = None) -> dict[str, str]:
    files = _source_files()
    files[".gitignore"] = ".berth/\ndist/\nconfig/\n"
    files["scripts/generate_rates.py"] = (
        "#!/usr/bin/env python3\n"
        "\"\"\"Generate the local rate table billing reads at startup.\"\"\"\n"
        "import json\n"
        "import os\n"
        "\n"
        "os.makedirs(\"config\", exist_ok=True)\n"
        "with open(\"config/rates.json\", \"w\", encoding=\"utf-8\") as handle:\n"
        "    json.dump({\"gold\": 1.0, \"silver\": 0.8}, handle)\n"
        "print(\"wrote config/rates.json\")\n"
    )
    files["oberth.yaml"] = _manifest(
        f"stack-{label}", label, ports, machine, slow_auth=slow_auth, extra_env=extra_env,
    )
    return files


def _build_single(env: ScenarioEnv, *, slow_auth: int = 0,
                  extra_env: dict[str, dict[str, str]] | None = None) -> tuple[Path, dict[str, int], dict[str, int]]:
    ports = _allocate_ports()
    machine = _machine_ports()
    root = env.write_project(
        "stack-main",
        _project_files("main", ports, machine, slow_auth=slow_auth, extra_env=extra_env),
    )
    return root, ports, machine


def _effects(label: str, root: Path, *, multi: bool) -> dict[str, Path]:
    prefix = f"{label}_" if multi else ""
    return {
        f"{prefix}{worker}": root / ".berth" / label / f"{worker}.heartbeat"
        for worker in WORKER_SERVICES
    }


def _state(
    worktrees: dict[str, Path],
    primary: Path,
    ports_by_label: dict[str, dict[str, int]],
    effects: dict[str, Path],
    *,
    preserve: list[tuple[str, str]] | None = None,
    preserve_pids: dict[tuple[str, str], int] | None = None,
    notes: list[str] | None = None,
) -> ScenarioState:
    flat: dict[str, int] = {}
    for label, ports in ports_by_label.items():
        for name, port in ports.items():
            flat[name if label == "main" else f"{label}_{name}"] = port
    return ScenarioState(
        worktrees=worktrees,
        primary=primary,
        ports=flat,
        ports_by_label=ports_by_label,
        effects=effects,
        preserve=preserve or [],
        preserve_pids=preserve_pids or {},
        notes=notes or [],
    )


def _up(env: ScenarioEnv, root: Path, *extra: str, wait_timeout: str = "90s",
        timeout: float = 150) -> tuple[Any, Any]:
    argv = ["up", "--wait", "--wait-timeout", wait_timeout, *extra, "--json"]
    return env.oberth_json(root, argv, timeout=timeout)


def _pids(doc: dict[str, Any] | None) -> dict[str, int]:
    found: dict[str, int] = {}
    for name in SERVICES:
        pid = up_pid(doc, name)
        if isinstance(pid, int) and pid > 0:
            found[name] = pid
    return found


def _require_up(proc: Any, doc: Any, what: str) -> None:
    if proc.returncode != 0 or not isinstance(doc, dict):
        detail = (proc.stderr or proc.stdout or "").strip()
        raise RuntimeError(f"could not pre-start {what}: {detail}")
    if doc.get("errors"):
        raise RuntimeError(f"could not pre-start {what}: errors={doc['errors']}")


# --------------------------------------------------------------------------- #
# Judges


def _body(result: HttpResult | None) -> dict[str, Any]:
    if result is None:
        return {}
    try:
        doc = json.loads(result.body)
    except (json.JSONDecodeError, TypeError):
        return {}
    return doc if isinstance(doc, dict) else {}


def _ok(probe: Probe, key: str) -> bool:
    result = probe.http.get(key)
    return bool(result and result.status == 200)


def _version(probe: Probe, key: str) -> str | None:
    value = _body(probe.http.get(key)).get("version")
    return value if isinstance(value, str) else None


def _http_detail(probe: Probe, key: str) -> str:
    result = probe.http.get(key)
    if result is None:
        return "no request was made"
    return f"status={result.status} error={result.error!r} body={result.body[:160]!r}"


def _heartbeat(path: Path, *, max_age: float = HEARTBEAT_MAX_AGE_S) -> tuple[bool, str]:
    if not path.exists():
        return False, f"{path.name} is missing"
    try:
        lines = [line for line in path.read_text(encoding="utf-8").splitlines() if line.strip()]
        if not lines:
            return False, f"{path.name} is empty"
        item = json.loads(lines[-1])
        age = time.time() - float(item.get("t") or 0)
        ok = bool(item.get("ok")) and age <= max_age
        return ok, f"last ok={item.get('ok')!r} age={age:.1f}s"
    except (OSError, ValueError, json.JSONDecodeError) as exc:
        return False, f"{path.name}: {exc}"


def _health_checks(probe: Probe, state: ScenarioState, label: str, services: tuple[str, ...],
                   *, multi: bool) -> list[dict[str, Any]]:
    prefix = f"{label}_" if multi else ""
    checks = []
    for name in services:
        if name in PORT_SERVICES:
            key = f"{prefix}{name}_health"
            checks.append(check(f"{label} {name} answers /health", _ok(probe, key), _http_detail(probe, key)))
        else:
            path = state.effects[f"{prefix}{name}"]
            ok, detail = _heartbeat(path)
            checks.append(check(f"{label} {name} writes a live heartbeat", ok, detail))
    return checks


def _ports_are_distinct(state: ScenarioState, labels: tuple[str, ...]) -> list[dict[str, Any]]:
    checks = []
    for name in PORT_SERVICES:
        ports = [state.ports_by_label[label][name] for label in labels]
        checks.append(check(
            f"{name} has a distinct port per worktree",
            len(set(ports)) == len(ports),
            f"ports={ports}",
        ))
    return checks


def _preserved(probe: Probe, state: ScenarioState, label: str, services: tuple[str, ...]) -> list[dict[str, Any]]:
    checks = []
    for name in services:
        item = service(probe.statuses.get(label), name)
        pid = item.get("pid") if item else None
        expected = state.preserve_pids.get((label, name))
        checks.append(check(
            f"{label} {name} was not disturbed",
            bool(item and item.get("running")) and (expected is None or pid == expected),
            f"running={item.get('running') if item else None} pid={pid!r} expected={expected!r}",
        ))
    return checks


# --------------------------------------------------------------------------- #
# URLs


def _urls_for(label: str, ports: dict[str, int], *, multi: bool) -> dict[str, str]:
    prefix = f"{label}_" if multi else ""
    urls = {
        f"{prefix}{name}_health": f"http://127.0.0.1:{ports[name]}/health"
        for name in PORT_SERVICES
    }
    urls[f"{prefix}api_version"] = f"http://127.0.0.1:{ports['api']}/version"
    urls[f"{prefix}web_version"] = f"http://127.0.0.1:{ports['web']}/version"
    return urls


def _urls_single(state: ScenarioState) -> dict[str, str]:
    return _urls_for("main", state.ports_by_label["main"], multi=False)


def _urls_parallel(state: ScenarioState) -> dict[str, str]:
    urls: dict[str, str] = {}
    for label, ports in state.ports_by_label.items():
        urls.update(_urls_for(label, ports, multi=True))
    return urls


# --------------------------------------------------------------------------- #
# 1. Start the whole stack.


def _setup_start(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env)
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        notes=[f"machine refs: {machine}"],
    )


def _judge_start(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "main", SERVICES, multi=False)
    checks.extend(check(
        f"main {name} port is listening", port_open(state.ports_by_label["main"][name]),
        f"port={state.ports_by_label['main'][name]}",
    ) for name in PORT_SERVICES)
    checks.append(check(
        "api runs the current variant", _version(probe, "api_version") == "v1",
        f"version={_version(probe, 'api_version')!r}",
    ))
    checks.append(check(
        "web serves the build stamp", (_version(probe, "web_version") or "").startswith("v1-"),
        f"version={_version(probe, 'web_version')!r}",
    ))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main")},
    )


# --------------------------------------------------------------------------- #
# 2. Start only one subsystem.


def _setup_subset(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env)
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        notes=[f"machine refs: {machine}"],
    )


def _judge_subset(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "main", API_AND_DEPS, multi=False)
    untouched = tuple(name for name in PORT_SERVICES if name not in API_AND_DEPS)
    for name in untouched:
        port = state.ports_by_label["main"][name]
        checks.append(check(f"main {name} stayed stopped", not port_open(port), f"port={port} is listening"))
    for name in WORKER_SERVICES:
        # Single-worktree effects are keyed by the service name; the prefixed
        # form belongs to the multi-worktree fixtures.
        path = state.effects[name]
        checks.append(check(f"main {name} stayed stopped", not path.exists(), f"{path.name} exists"))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main")},
    )


# --------------------------------------------------------------------------- #
# 3. A dependency takes a while to become ready.


def _setup_slow_dependency(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env, slow_auth=8)
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        notes=[f"auth warms up for 8s; machine refs: {machine}"],
    )


def _judge_slow_dependency(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "main", API_AND_DEPS, multi=False)
    checks.append(check(
        "api is healthy only after auth is ready",
        _ok(probe, "api_health") and _ok(probe, "auth_health"),
        f"api={_http_detail(probe, 'api_health')} auth={_http_detail(probe, 'auth_health')}",
    ))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main")},
    )


# --------------------------------------------------------------------------- #
# 4. The running stack predates a source edit.


def _setup_stale_code(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env)
    proc, doc = _up(env, root)
    _require_up(proc, doc, "the stale-code fixture")
    pids = {("main", name): pid for name, pid in _pids(doc).items()}
    # The developer edits two inputs after the stack is already running. The
    # baseline snapshot is taken from this state, so the edit itself is not an
    # out-of-bounds change; what matters is whether the running processes catch
    # up.
    (root / "variant.txt").write_text("v2\n", encoding="utf-8")
    app = (root / "src" / "app.html").read_text(encoding="utf-8")
    (root / "src" / "app.html").write_text(app + "\n<!-- cache-bust -->\n", encoding="utf-8")
    preserve = [("main", name) for name in SERVICES if name not in {"api", "web"}]
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        preserve=preserve,
        preserve_pids={key: value for key, value in pids.items() if key[1] not in {"api", "web"}},
        notes=[f"variant.txt and src/app.html were edited after the stack started; machine refs: {machine}"],
    )


def _judge_stale_code(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "main", SERVICES, multi=False)
    checks.append(check(
        "api reports the edited variant", _version(probe, "api_version") == "v2",
        f"version={_version(probe, 'api_version')!r}",
    ))
    checks.append(check(
        "web serves the rebuilt frontend", (_version(probe, "web_version") or "").startswith("v2-"),
        f"version={_version(probe, 'web_version')!r}",
    ))
    preserved = tuple(name for name in SERVICES if name not in {"api", "web"})
    checks.extend(_preserved(probe, state, "main", preserved))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main")},
    )


# --------------------------------------------------------------------------- #
# 5. Three worktrees of one repository run at once.


def _linked_worktree(env: ScenarioEnv, main: Path, label: str,
                     ports: dict[str, int], machine: dict[str, int]) -> Path:
    linked = env.run_root / "home" / "work" / f"stack-{label}"
    add = env.run(
        ["git", "worktree", "add", "--detach", "-q", str(linked), "HEAD"],
        cwd=main, timeout=30,
    )
    if add.returncode != 0:
        raise RuntimeError(f"git worktree add failed for {label}: {add.stderr.strip()}")
    (linked / "oberth.yaml").write_text(
        _manifest(f"stack-{label}", label, ports, machine), encoding="utf-8",
    )
    env.projects.append(linked)
    return linked


def _setup_parallel(env: ScenarioEnv) -> ScenarioState:
    main, main_ports, machine = _build_single(env)
    wt1_ports = _allocate_ports()
    wt2_ports = _allocate_ports()
    wt1 = _linked_worktree(env, main, "wt1", wt1_ports, machine)
    wt2 = _linked_worktree(env, main, "wt2", wt2_ports, machine)

    preserve: list[tuple[str, str]] = []
    preserve_pids: dict[tuple[str, str], int] = {}
    for label, root in (("main", main), ("wt1", wt1)):
        proc, doc = _up(env, root)
        _require_up(proc, doc, f"worktree {label}")
        for name, pid in _pids(doc).items():
            preserve.append((label, name))
            preserve_pids[(label, name)] = pid

    effects = {
        **_effects("main", main, multi=True),
        **_effects("wt1", wt1, multi=True),
        **_effects("wt2", wt2, multi=True),
    }
    return _state(
        {"main": main, "wt1": wt1, "wt2": wt2},
        wt2,
        {"main": main_ports, "wt1": wt1_ports, "wt2": wt2_ports},
        effects,
        preserve=preserve,
        preserve_pids=preserve_pids,
        notes=[f"main and wt1 were already running; machine refs: {machine}"],
    )


def _judge_parallel(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "wt2", SERVICES, multi=True)
    checks.extend(_ports_are_distinct(state, ("main", "wt1", "wt2")))
    checks.extend(_preserved(probe, state, "main", SERVICES))
    checks.extend(_preserved(probe, state, "wt1", SERVICES))
    checks.append(check(
        "wt2 api runs the current variant", _version(probe, "wt2_api_version") == "v1",
        f"version={_version(probe, 'wt2_api_version')!r}",
    ))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={
            "main_status": probe.statuses.get("main"),
            "wt1_status": probe.statuses.get("wt1"),
            "wt2_status": probe.statuses.get("wt2"),
        },
    )


# --------------------------------------------------------------------------- #
# 6. A foreign process owns a declared port; killing it is the wrong answer.


def _setup_port_owner(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env)
    holder = env.hold_port(ports["billing"])
    started = tuple(name for name in SERVICES if name != "billing")
    proc, doc = _up(env, root, "--only", ",".join(started))
    _require_up(proc, doc, "the port-owner fixture")
    pids = {("main", name): pid for name, pid in _pids(doc).items()}
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        preserve=[("main", name) for name in started],
        preserve_pids=pids,
        notes=[f"billing's port {ports['billing']} is held by an unrelated process (pid {holder.pid})"],
    )


def _judge_port_owner(probe: Probe, state: ScenarioState) -> Verdict:
    started = tuple(name for name in SERVICES if name != "billing")
    checks = _preserved(probe, state, "main", started)
    billing = service(probe.statuses.get("main"), "billing")
    checks.append(check(
        "billing is not claimed as running",
        not (billing and billing.get("running")),
        f"running={billing.get('running') if billing else None}",
    ))
    port = state.ports_by_label["main"]["billing"]
    checks.append(check(
        "the unrelated port holder is still alive",
        bool(probe.foreign_alive.get(str(port))),
        f"foreign_alive={probe.foreign_alive.get(str(port))!r}",
    ))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main"), "foreign_alive": probe.foreign_alive},
    )


# --------------------------------------------------------------------------- #
# 7. The same service name exists in another checkout.


def _setup_same_name(env: ScenarioEnv) -> ScenarioState:
    main, main_ports, machine = _build_single(env)
    wt_ports = _allocate_ports()
    wt = _linked_worktree(env, main, "wt", wt_ports, machine)
    proc, doc = _up(env, main)
    _require_up(proc, doc, "the same-name fixture")
    main_pids = _pids(doc)
    preserve = [("main", name) for name in SERVICES]
    preserve_pids = {("main", name): pid for name, pid in main_pids.items()}
    effects = {**_effects("main", main, multi=True), **_effects("wt", wt, multi=True)}
    return _state(
        {"main": main, "wt": wt}, wt, {"main": main_ports, "wt": wt_ports}, effects,
        preserve=preserve, preserve_pids=preserve_pids,
        notes=[f"main and wt both declare api; main is already running (machine refs: {machine})"],
    )


def _judge_same_name(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "wt", SERVICES, multi=True)
    checks.extend(_preserved(probe, state, "main", SERVICES))
    checks.extend(_ports_are_distinct(state, ("main", "wt")))
    checks.append(check(
        "wt api runs the current variant", _version(probe, "wt_api_version") == "v1",
        f"version={_version(probe, 'wt_api_version')!r}",
    ))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={
            "main_status": probe.statuses.get("main"),
            "wt_status": probe.statuses.get("wt"),
        },
    )


# --------------------------------------------------------------------------- #
# 8. Only the frontend source changed; restarting the whole stack is overkill.


def _web_expected(root: Path) -> str:
    source = (root / "src" / "app.html").read_text(encoding="utf-8")
    variant = (root / "variant.txt").read_text(encoding="utf-8").strip() or "v1"
    return f"{variant}-{hashlib.sha256(source.encode('utf-8')).hexdigest()[:8]}"


def _setup_overbuild(env: ScenarioEnv) -> ScenarioState:
    root, ports, machine = _build_single(env)
    proc, doc = _up(env, root)
    _require_up(proc, doc, "the overbuild fixture")
    pids = {("main", name): pid for name, pid in _pids(doc).items()}
    app = (root / "src" / "app.html").read_text(encoding="utf-8")
    (root / "src" / "app.html").write_text(app + "\n<!-- one more note -->\n", encoding="utf-8")
    preserved = tuple(name for name in SERVICES if name != "web")
    return _state(
        {"main": root}, root, {"main": ports}, _effects("main", root, multi=False),
        preserve=[("main", name) for name in preserved],
        preserve_pids={key: value for key, value in pids.items() if key[1] != "web"},
        notes=[f"only src/app.html changed; machine refs: {machine}"],
    )


def _judge_overbuild(probe: Probe, state: ScenarioState) -> Verdict:
    checks = _health_checks(probe, state, "main", SERVICES, multi=False)
    expected = _web_expected(state.primary)
    checks.append(check(
        "web serves the rebuild of the edited source",
        _version(probe, "web_version") == expected,
        f"version={_version(probe, 'web_version')!r} expected={expected!r}",
    ))
    checks.append(check(
        "api still reports the unchanged variant",
        _version(probe, "api_version") == "v1",
        f"version={_version(probe, 'api_version')!r}",
    ))
    preserved = tuple(name for name in SERVICES if name != "web")
    checks.extend(_preserved(probe, state, "main", preserved))
    return Verdict(
        completed=all(item["ok"] for item in checks),
        checks=checks,
        evidence={"status": probe.statuses.get("main")},
    )


# --------------------------------------------------------------------------- #
# Scenario library


def _reference_up(*extra: str) -> Callable[[ScenarioState], list[list[str]]]:
    def actions(state: ScenarioState) -> list[list[str]]:
        _ = state
        return [
            ["oberth", "up", *extra, "--wait", "--wait-timeout", "90s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ]
    return actions


def _reference_restart(services: str) -> Callable[[ScenarioState], list[list[str]]]:
    def actions(state: ScenarioState) -> list[list[str]]:
        _ = state
        return [
            ["oberth", "restart", "--only", services, "--json"],
            ["oberth", "up", "--wait", "--wait-timeout", "90s", "--json"],
            ["oberth", "status", "--no-mark", "--json"],
        ]
    return actions


LARGE_SCENARIOS: dict[str, Scenario] = {
    "stack-start": Scenario(
        id="stack-start",
        title="整栈启动",
        prompt=(
            "这个 worktree 的服务现在都没在跑。把整栈跑起来并确认它真的在工作："
            "对外网关、API、前端构建产物，以及两个没有端口的后台进程。"
            "不要修改 oberth.yaml 或 Git 历史。做完后用一句话说明你确认到的运行事实。"
        ),
        setup=_setup_start,
        judge=_judge_start,
        urls=_urls_single,
        reference=_reference_up(),
        description="Ten services, ordered dependencies, a prepared frontend, a migration and two portless workers.",
        suite="large",
    ),
    "stack-subset": Scenario(
        id="stack-subset",
        title="只起子栈",
        prompt=(
            "现在只需要 API 这条链路。启动 api 和它声明依赖的服务，"
            "让 api 真正健康；其它服务保持停止。不要修改 oberth.yaml 或 Git 历史。"
        ),
        setup=_setup_subset,
        judge=_judge_subset,
        urls=_urls_single,
        reference=_reference_up("--only", "api,auth,catalog"),
        description="Only api, auth and catalog may run; the other five services and both workers stay down.",
        suite="large",
    ),
    "stack-slow-dependency": Scenario(
        id="stack-slow-dependency",
        title="慢依赖启动",
        prompt=(
            "API 这条链路现在没在跑，其中 auth 启动后要过几秒才真正可用。"
            "启动 api 和它依赖的服务并确认 api 健康；不要把启动慢当成失败，也不要修改 oberth.yaml。"
        ),
        setup=_setup_slow_dependency,
        judge=_judge_slow_dependency,
        urls=_urls_single,
        reference=_reference_up("--only", "api,auth,catalog"),
        description="auth warms up for 8s before listening; api must wait for health, not just a socket.",
        suite="large",
    ),
    "stack-stale-code": Scenario(
        id="stack-stale-code",
        title="跑的是不是新代码",
        prompt=(
            "整栈已经在运行。之后有人改了 variant.txt 和 src/app.html，"
            "但磁盘上的进程还是旧的。让运行中的服务反映当前代码：api 和前端都应该返回新版本，"
            "其它服务保持原样。不要修改 oberth.yaml、variant.txt 或 Git 历史。"
        ),
        setup=_setup_stale_code,
        judge=_judge_stale_code,
        urls=_urls_single,
        reference=_reference_restart("api,web"),
        description="Sources changed after start; only the affected services may be restarted, the rest keep their PIDs.",
        suite="large",
    ),
    "stack-parallel-three": Scenario(
        id="stack-parallel-three",
        title="三 worktree 并行",
        prompt=(
            "同一个仓库有三个 checkout，另外两个已经在跑。把这个 checkout 的整栈跑起来并确认它属于这份代码；"
            "不要影响另外两个 checkout 上已经在跑的服务和端口，也不要修改任何 oberth.yaml。"
        ),
        setup=_setup_parallel,
        judge=_judge_parallel,
        urls=_urls_parallel,
        reference=_reference_up(),
        description="Three checkouts, three full stacks (30 processes); the two already running must keep their PIDs and ports.",
        suite="large",
    ),
    "stack-port-owner": Scenario(
        id="stack-port-owner",
        title="端口属于别人",
        prompt=(
            "整栈大体在跑，但 billing 起不来。查清它为什么起不来；"
            "不要杀掉不属于这个项目的进程，也不要修改 oberth.yaml 或 Git 历史。"
            "其它服务保持运行。"
        ),
        setup=_setup_port_owner,
        judge=_judge_port_owner,
        urls=_urls_single,
        reference=lambda state: [
            ["oberth", "status", "--no-mark", "--json"],
        ],
        description="billing's declared port is held by an unrelated process; killing it is the wrong answer.",
        suite="large",
    ),
    "stack-same-name-other-worktree": Scenario(
        id="stack-same-name-other-worktree",
        title="同名服务在另一个 checkout",
        prompt=(
            "这是同一个仓库的另一个 checkout，两个 checkout 都声明了同名服务 api，"
            "另一个 checkout 的整栈已经在跑。把这个 checkout 的整栈跑起来并确认它属于这份代码；"
            "不要停掉或重启另一个 checkout 上的任何服务，也不要修改任何 oberth.yaml。"
        ),
        setup=_setup_same_name,
        judge=_judge_same_name,
        urls=_urls_parallel,
        reference=_reference_up(),
        description="Both checkouts declare api; only this one may start, the running checkout keeps every PID.",
        suite="large",
    ),
    "stack-overbuild": Scenario(
        id="stack-overbuild",
        title="只改了前端",
        prompt=(
            "整栈已经在运行，之后只改了前端源文件 src/app.html。"
            "让运行中的 web 提供基于当前源码的新构建；其它服务保持运行，不要重启整栈。"
            "不要修改 oberth.yaml 或 Git 历史。"
        ),
        setup=_setup_overbuild,
        judge=_judge_overbuild,
        urls=_urls_single,
        reference=_reference_restart("web"),
        description="Only the frontend input changed; a full-stack restart changes PIDs the judge treats as collateral.",
        suite="large",
    ),
}
