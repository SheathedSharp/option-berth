#!/usr/bin/env python3
"""Verify idempotent readiness against a real binary in disposable isolated state.
Usage: python3 scripts/verify-reused-readiness.py --binary /path/to/oberth
No caller services, configuration, credentials or logs are used.
"""
import argparse
import importlib.util
import json
import subprocess
import tempfile
import time
from pathlib import Path


def verify(binary: Path) -> None:
    source = Path(__file__).with_name("verify-e2e.py")
    spec = importlib.util.spec_from_file_location("berth_trial", source)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    with tempfile.TemporaryDirectory(prefix="bready-", dir="/tmp") as tmp:
        trial = module.Trial(binary.resolve(strict=True), Path(tmp).resolve())
        try:
            trial.provision()
            main, linked = trial.roots
            server = main / "server.py"
            code = server.read_text()
            assert "self.send_response(200)" in code
            server.write_text(code.replace("self.send_response(200)",
                'self.send_response(503 if os.path.exists("unhealthy") else 200)'))
            for root in (main, linked):
                trial.command(root, "up", "--wait", "--json")
            initial = trial.ready(main)
            port = next(s["port_actual"] for s in initial["worktree"]["services"] if s["name"] == "api")
            conn = trial.rpc()
            before = next(p for p in conn.call("state.snapshot", {})["ports"] if p["port"] == port)
            # A healthy repeated invocation remains successful and does not restart.
            trial.command(main, "up", "--wait", "--json")
            (main / "unhealthy").touch()
            deadline = time.monotonic() + 20
            while True:
                snapshot = conn.call("state.snapshot", {})
                row = next((p for p in snapshot["ports"] if p["port"] == port), {})
                if row.get("health", {}).get("code") == 503:
                    break
                if time.monotonic() > deadline:
                    raise AssertionError("fixture's HTTP 503 was not observed")
                time.sleep(0.1)
            result = subprocess.run([str(trial.binary), "up", "--wait", "--wait-timeout=400ms", "--json"],
                cwd=main, env=trial.env, capture_output=True, text=True, timeout=20)
            after = trial.status(main)
            api = next(s for s in after["worktree"]["services"] if s["name"] == "api")
            current = next(p for p in conn.call("state.snapshot", {})["ports"] if p["port"] == port)
            assert api["running"] and current["pid"] == before["pid"], "pre-existing API was stopped or restarted"
            trial.ready(linked)
            assert result.returncode == 1, f"unhealthy repeated up falsely succeeded: {result.stdout} {result.stderr}"
            data = json.loads(result.stdout)
            outcome = next(s for s in data["services"] if s["service"] == "api")
            assert outcome["state"] == "failed" and outcome["reason"] == "ready_timeout", data
            assert outcome["skipped"] is True and "api" in data["errors"], data
            print(json.dumps({"healthy_reuse": "passed", "unhealthy_reuse": "failed_as_expected",
                "existing_pid_preserved": True, "linked_worktree_preserved": True}))
        finally:
            trial.close()
    print("isolated fixture and owned processes cleaned up")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    args = parser.parse_args()
    verify(args.binary)
