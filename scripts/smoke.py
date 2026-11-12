#!/usr/bin/env python3
"""The per-PR smoke check: run the minimal sample in two disposable worktrees.

Never touches the caller's daemon, HOME or BERTH_HOME. The complex sample
(`examples/stack-workshop`) is not run here; it is the input of the manual
benchmark, which is the only consumer that needs Docker.
"""
from __future__ import annotations

import argparse
import importlib.util
import json
import shlex
import shutil
import sys
import tempfile
from pathlib import Path
from urllib.request import build_opener, ProxyHandler


def trial_module():
    source = Path(__file__).with_name("verify-e2e.py")
    spec = importlib.util.spec_from_file_location("berth_trial", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def smoke(module, binary: Path, sample: Path) -> dict:
    with tempfile.TemporaryDirectory(prefix="bsmoke-", dir="/tmp") as tmp:
        trial = module.Trial(binary, Path(tmp).resolve())
        try:
            trial.provision()
            for root in trial.roots:
                for name in ("api.py", "worker.py", "oberth.yaml"):
                    shutil.copyfile(sample / name, root / name)
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
            return {"sample": "hello-worktree", "worktrees": 2,
                    "distinct_ports": True, "healthy": True, "portless_worker": True,
                    "single_side_stop": "passed"}
        finally:
            trial.close()


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    binary = parser.parse_args().binary.resolve(strict=True)
    sample = Path(__file__).resolve().parent.parent / "examples" / "hello-worktree"
    print(json.dumps(smoke(trial_module(), binary, sample)))
    print("isolated smoke processes and temporary files cleaned up")


if __name__ == "__main__":
    main()
