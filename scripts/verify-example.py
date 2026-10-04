#!/usr/bin/env python3
"""Run the checked-in example in two disposable worktrees, never the user's daemon."""
import argparse
import importlib.util
import json
import shlex
import shutil
import sys
import tempfile
from pathlib import Path
from urllib.request import build_opener, ProxyHandler


def verify(binary: Path) -> None:
    source = Path(__file__).with_name("verify-e2e.py")
    spec = importlib.util.spec_from_file_location("berth_trial", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    example = source.parent.parent / "examples" / "hello-worktree"
    with tempfile.TemporaryDirectory(prefix="bhello-", dir="/tmp") as tmp:
        trial = module.Trial(binary.resolve(strict=True), Path(tmp).resolve())
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
            print(json.dumps({"example": "hello-worktree", "worktrees": 2,
                "distinct_ports": True, "healthy": True, "portless_worker": True,
                "single_side_stop": "passed"}))
        finally:
            trial.close()
    print("isolated example processes and temporary files cleaned up")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    verify(parser.parse_args().binary)
