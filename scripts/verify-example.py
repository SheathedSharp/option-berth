#!/usr/bin/env python3
"""Run the checked-in examples in two disposable worktrees, never the user's daemon."""
from __future__ import annotations

import argparse
import importlib.util
import json
import shlex
import shutil
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


def verify(binary: Path) -> None:
    module = trial_module()
    examples = Path(__file__).resolve().parent.parent / "examples"
    checked = [verify_hello(module, binary.resolve(strict=True), examples / "hello-worktree"),
               verify_worktree_data(module, binary, examples / "worktree-data")]
    for result in checked:
        print(json.dumps(result))
    print("isolated example processes and temporary files cleaned up")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    verify(parser.parse_args().binary)
