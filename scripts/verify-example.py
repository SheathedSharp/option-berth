#!/usr/bin/env python3
"""Run the checked-in examples in two disposable worktrees, never the user's daemon."""
from __future__ import annotations

import argparse
import importlib.util
import json
import shlex
import shutil
import subprocess
import sys
import tempfile
import time
from pathlib import Path
from urllib.request import build_opener, ProxyHandler


def trial_module():
    source = Path(__file__).with_name("verify-e2e.py")
    spec = importlib.util.spec_from_file_location("berth_trial", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def verify_hello(module, binary: Path, example: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="bhello-", dir="/tmp") as tmp:
        trial = module.Trial(binary, Path(tmp).resolve())
        try:
            trial.provision()
            for root in trial.roots:
                for name in ("api.py", "worker.py", "oberth.yaml"):
                    shutil.copyfile(example / name, root / name)
                manifest = root / "oberth.yaml"
                data = manifest.read_text()
                for name in ("api", "worker"):
                    command = shlex.quote(sys.executable) + f" -u {name}.py"
                    data = data.replace(f"cmd: python3 -u {name}.py", "cmd: " + json.dumps(command))
                manifest.write_text(data)
                trial.command(root, "up", "--wait", "--json")
            http = build_opener(ProxyHandler({}))
            ports = []
            for root in trial.roots:
                state = trial.ready(root)
                port = next(s["port_actual"] for s in state["worktree"]["services"] if s["name"] == "api")
                ports.append(port)
                with http.open(f"http://127.0.0.1:{port}/health", timeout=3) as response:
                    assert json.load(response) == {"service": "api", "ok": True, "port": port}
            assert ports[0] != ports[1], "worktrees received the same port"
            trial.command(trial.roots[0], "down", "--force", "--json")
            state = trial.status(trial.roots[0])
            assert not any(s["running"] for s in state["worktree"]["services"]), state
            trial.ready(trial.roots[1])
            with http.open(f"http://127.0.0.1:{ports[1]}/health", timeout=3) as response:
                assert response.status == 200
            return {"example": "hello-worktree", "worktrees": 2,
                    "distinct_ports": True, "healthy": True, "portless_worker": True,
                    "single_side_stop": "passed"}
        finally:
            trial.close()


def verify_worktree_data(module, binary: Path, example: Path) -> dict:
    """Two worktrees migrate and serve separate databases, and one down leaves
    the other's process and data alone."""
    http = build_opener(ProxyHandler({}))

    def health(root: Path, timeout: float = 15) -> tuple[int, dict]:
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            services = trial.status(root)["worktree"]["services"]
            api = next(s for s in services if s["name"] == "api")
            if api.get("running") and api.get("port_actual"):
                try:
                    with http.open(f"http://127.0.0.1:{api['port_actual']}/health", timeout=3) as response:
                        return api["port_actual"], json.load(response)
                except OSError as error:
                    last = error
            else:
                last = api
            time.sleep(.15)
        raise RuntimeError(f"api did not become healthy in {root}: {last}")

    with tempfile.TemporaryDirectory(prefix="bwdata-", dir="/tmp") as tmp:
        trial = module.Trial(binary, Path(tmp).resolve())
        try:
            trial.provision()
            for root in trial.roots:
                for name in ("api.py", "migrate.py", "oberth.yaml"):
                    shutil.copyfile(example / name, root / name)
                manifest = root / "oberth.yaml"
                data = manifest.read_text()
                for line, script in (("prepare: python3 migrate.py", "migrate.py"),
                                     ("cmd: python3 -u api.py", "api.py")):
                    command = shlex.quote(sys.executable) + f" {script}"
                    data = data.replace(line, line.split(":")[0] + ": " + json.dumps(command))
                manifest.write_text(data)
                trial.command(root, "up", "--wait", "--json")

            first = [health(root) for root in trial.roots]
            ports, docs = [port for port, _ in first], [doc for _, doc in first]
            assert ports[0] != ports[1], f"worktrees share an auto port: {ports}"
            assert docs[0]["db"] != docs[1]["db"], f"worktrees share a database: {docs}"
            for doc in docs:
                assert doc["ok"] is True, doc
                assert doc["migrations"] == 1 and doc["notes"] == 1, doc

            # A restart reruns `prepare:`. The migration is idempotent, and the
            # other worktree's database is not touched.
            trial.command(trial.roots[0], "restart", "--only", "api", "--json")
            port0, doc0 = health(trial.roots[0])
            assert port0 == ports[0], f"restart moved the auto port: {ports[0]} -> {port0}"
            assert doc0["db"] == docs[0]["db"] and doc0["migrations"] == 1 and doc0["notes"] == 1, doc0
            assert health(trial.roots[1])[1] == docs[1]

            # `down` is scoped to the manifest's own worktree: the other side
            # keeps running, and neither data file is deleted.
            trial.command(trial.roots[0], "down", "--force", "--json")
            state = trial.status(trial.roots[0])
            assert not any(s["running"] for s in state["worktree"]["services"]), state
            assert health(trial.roots[1])[1] == docs[1]
            assert Path(docs[0]["db"]).is_file() and Path(docs[1]["db"]).is_file()

            return {"example": "worktree-data", "worktrees": 2,
                    "distinct_ports": True, "distinct_databases": True,
                    "migrations_per_worktree": 1, "idempotent_restart": True,
                    "single_side_stop": "passed"}
        finally:
            trial.close()


def verify_stack(module, binary: Path, example: Path) -> dict:
    """One full ten-service stack: ordered dependencies, a slow auth, a prepared
    frontend, a migration and two portless workers. Runs in one disposable
    worktree so the required Examples job stays bounded."""
    http = build_opener(ProxyHandler({}))

    def get_json(port: int, path: str, timeout: float = 20) -> dict:
        deadline = time.monotonic() + timeout
        last = None
        while time.monotonic() < deadline:
            try:
                with http.open(f"http://127.0.0.1:{port}{path}", timeout=3) as response:
                    return json.load(response)
            except OSError as error:
                last = error
                time.sleep(.15)
        raise RuntimeError(f"{path} on {port} did not answer: {last}")

    with tempfile.TemporaryDirectory(prefix="bstack-", dir="/tmp") as tmp:
        trial = module.Trial(binary, Path(tmp).resolve())
        try:
            trial.provision()
            root = trial.roots[0]
            for source in example.rglob("*"):
                if not source.is_file() or source.name == "README.md":
                    continue
                target = root / source.relative_to(example)
                target.parent.mkdir(parents=True, exist_ok=True)
                shutil.copyfile(source, target)
            manifest = root / "oberth.yaml"
            data = manifest.read_text()
            py = sys.executable
            replacements = {
                "cmd: python3 -u service.py": "cmd: " + json.dumps(f"{py} -u service.py"),
                "cmd: python3 -u worker.py": "cmd: " + json.dumps(f"{py} -u worker.py"),
                "prepare: python3 migrate.py": "prepare: " + json.dumps(f"{py} migrate.py"),
                "prepare: python3 build.py": "prepare: " + json.dumps(f"{py} build.py"),
            }
            for old, new in replacements.items():
                data = data.replace(old, new)
            manifest.write_text(data)
            trial.command(root, "up", "--wait", "--wait-timeout", "25s", "--json")

            state = trial.status(root)
            services = {item["name"]: item for item in state["worktree"]["services"]}
            expected = {
                "auth", "catalog", "api", "gateway", "web", "search", "billing",
                "ingest", "worker", "notifier",
            }
            assert set(services) == expected, sorted(services)
            assert all(item.get("running") for item in services.values()), services
            assert all(item.get("health_status", {}).get("status") == "ok"
                       for name, item in services.items()
                       if name not in {"worker", "notifier"}), services

            gateway = services["gateway"]["port_actual"]
            api = services["api"]["port_actual"]
            web = services["web"]["port_actual"]
            assert get_json(gateway, "/health")["ok"] is True
            assert get_json(api, "/version")["version"] == "v1"
            assert get_json(web, "/version")["version"].startswith("v1-")

            effects = {
                "worker": root / ".berth" / state["worktree"]["identity"] / "worker.heartbeat",
                "notifier": root / ".berth" / state["worktree"]["identity"] / "notifier.heartbeat",
            }
            for name, path in effects.items():
                deadline = time.monotonic() + 15
                while time.monotonic() < deadline:
                    if path.exists() and path.read_text().strip():
                        last = json.loads([line for line in path.read_text().splitlines() if line.strip()][-1])
                        if last.get("ok"):
                            break
                    time.sleep(.2)
                else:
                    raise RuntimeError(f"{name} never wrote an ok heartbeat at {path}")
                assert last["service"] == name, (last, name)

            trial.command(root, "down", "--force", "--json")
            stopped = trial.status(root)
            assert not any(item["running"] for item in stopped["worktree"]["services"]), stopped
            return {"example": "stack-workshop", "services": len(services),
                    "ordered_dependencies": True, "slow_dependency": True,
                    "prepared_frontend": True, "migration": True,
                    "portless_workers": 2, "down_stops_all": "passed"}
        finally:
            trial.close()


def compose_available() -> bool:
    try:
        done = subprocess.run(
            ["docker", "compose", "version"],
            capture_output=True, text=True, timeout=15,
        )
    except (OSError, subprocess.SubprocessError):
        return False
    return done.returncode == 0


def compose(root: Path, project: str, *args: str, timeout: float = 120) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        ["docker", "compose", "-f", str(root / "docker-compose.yml"), "-p", project, *args],
        cwd=root, capture_output=True, text=True, timeout=timeout,
    )


def compose_containers(project: str) -> list[str]:
    done = subprocess.run(
        ["docker", "ps", "-aq", "--filter", f"label=com.docker.compose.project={project}"],
        capture_output=True, text=True, timeout=30,
    )
    if done.returncode:
        # An unavailable daemon must not read as "no containers": that would
        # make the after-down assertion pass without observing anything.
        raise RuntimeError(f"docker ps for {project}: {done.stderr.strip()}")
    return [line for line in done.stdout.splitlines() if line.strip()]


def run_oberth_json(trial, root: Path, *args: str, timeout: float = 120) -> dict:
    done = subprocess.run(
        [str(trial.binary), *args], cwd=root, env=trial.env,
        capture_output=True, text=True, timeout=timeout,
    )
    if done.returncode:
        raise RuntimeError(
            f"{args}: exit={done.returncode}\n{done.stderr}\n{done.stdout}"
        )
    return json.loads(done.stdout)


def verify_compose(module, binary: Path, example: Path) -> dict:
    """One Compose project per worktree, verified where Docker exists.

    macOS runners have no Docker daemon, so this branch reports a skip instead
    of pretending the stack ran. The required Examples check still runs both
    operating systems; the Compose evidence is the Linux row.
    """
    if not sys.platform.startswith("linux"):
        return {"example": "compose-stack", "skipped": "Docker Compose is verified on Linux CI"}
    if not compose_available():
        # On the Linux row this is a missing prerequisite, not a platform
        # difference: fail so the required check cannot pass without evidence.
        raise RuntimeError("docker compose is not available on the Linux examples runner")

    # Pull first: `up --wait` must not spend its budget on an image download.
    pulled = subprocess.run(
        ["docker", "pull", "postgres:16-alpine"],
        capture_output=True, text=True, timeout=300,
    )
    if pulled.returncode:
        raise RuntimeError("postgres:16-alpine could not be pulled: " + pulled.stderr.strip())

    with tempfile.TemporaryDirectory(prefix="bcompose-", dir="/tmp") as tmp:
        trial = module.Trial(binary, Path(tmp).resolve())
        projects: list[str] = []
        try:
            trial.provision()
            for root in trial.roots:
                for name in ("docker-compose.yml", "oberth.yaml"):
                    shutil.copyfile(example / name, root / name)

            identities = [trial.status(root)["worktree"]["identity"] for root in trial.roots]
            projects = ["app_" + identity for identity in identities]
            for root, project in zip(trial.roots, projects):
                run_oberth_json(trial, root, "up", "--wait", "--json")

            ports = []
            for root, project, identity in zip(trial.roots, projects, identities):
                state = trial.status(root)
                db = next(item for item in state["worktree"]["services"] if item["name"] == "db")
                assert db["running"], db
                deadline = time.monotonic() + 60
                while time.monotonic() < deadline:
                    health = db.get("health_status") or {}
                    if health.get("status") == "ok":
                        break
                    time.sleep(.2)
                    state = trial.status(root)
                    db = next(item for item in state["worktree"]["services"] if item["name"] == "db")
                assert (db.get("health_status") or {}).get("status") == "ok", db
                assert db.get("port_actual"), db
                ports.append(db["port_actual"])

                mapped = compose(root, project, "port", "db", "5432")
                if mapped.returncode:
                    raise RuntimeError(f"compose port: {mapped.stderr}")
                host_port = int(mapped.stdout.strip().rsplit(":", 1)[1])
                assert host_port == db["port_actual"], (host_port, db["port_actual"])

                database = f"app_{identity}"
                query = compose(
                    root, project, "exec", "-T", "db", "psql", "-U", "app",
                    "-d", database, "-tAc", "select current_database()",
                )
                if query.returncode:
                    raise RuntimeError(f"psql in {project}: {query.stderr}")
                assert query.stdout.strip() == database, (query.stdout, database)
                assert compose_containers(project), f"no containers for {project}"

            assert ports[0] != ports[1], f"worktrees share a Compose host port: {ports}"

            # A normal down is SIGTERM: Compose must stop its own containers and
            # leave the other worktree's project alone.
            run_oberth_json(trial, trial.roots[0], "down", "--json")
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline and compose_containers(projects[0]):
                time.sleep(.2)
            assert not compose_containers(projects[0]), f"down left {projects[0]} running"
            assert compose_containers(projects[1]), "down stopped the other worktree's containers"
            other = trial.status(trial.roots[1])
            other_db = next(item for item in other["worktree"]["services"] if item["name"] == "db")
            assert other_db["running"], other_db
            assert (other_db.get("health_status") or {}).get("status") == "ok", other_db

            run_oberth_json(trial, trial.roots[1], "down", "--json")
            deadline = time.monotonic() + 60
            while time.monotonic() < deadline and compose_containers(projects[1]):
                time.sleep(.2)
            assert not compose_containers(projects[1]), f"down left {projects[1]} running"

            return {"example": "compose-stack", "worktrees": 2,
                    "distinct_ports": True, "distinct_databases": True,
                    "port_auto_into_compose": True, "pg_isready_health": True,
                    "single_side_stop": "passed", "down_stops_containers": True}
        finally:
            for root, project in zip(trial.roots, projects):
                try:
                    compose(root, project, "down", "--volumes", "--remove-orphans")
                except (OSError, subprocess.SubprocessError):
                    pass
            trial.close()


def verify(binary: Path) -> None:
    module = trial_module()
    examples = Path(__file__).resolve().parent.parent / "examples"
    checked = [verify_hello(module, binary.resolve(strict=True), examples / "hello-worktree"),
               verify_worktree_data(module, binary, examples / "worktree-data"),
               verify_stack(module, binary, examples / "stack-workshop"),
               verify_compose(module, binary, examples / "compose-stack")]
    for result in checked:
        print(json.dumps(result))
    print("isolated example processes and temporary files cleaned up")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    verify(parser.parse_args().binary)
