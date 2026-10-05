#!/usr/bin/env python3
"""Capture actual native app windows with an isolated, real demo project.

Build the engine and SiteCapture explicitly first. Never reads the user's
project registry, provider accounts or ordinary daemon. No ImageRenderer.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import os
from pathlib import Path
import shlex
import shutil
import struct
import subprocess
import sys
import tempfile
import time

REPO = Path(__file__).resolve().parents[1]


def capture(binary: Path, runner: Path, output: Path) -> None:
    binary, runner = binary.resolve(strict=True), runner.resolve(strict=True)
    output.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="oberth-site-", dir="/Users/Shared") as temp:
        base = Path(temp).resolve()
        home, project = base / "home", base / "home" / "projects" / "launchpad"
        state, sock = home / ".option-berth", base / "daemon.sock"
        project.mkdir(parents=True)
        state.mkdir(mode=0o700)
        env = dict(os.environ, HOME=str(home), CFFIXED_USER_HOME=str(home), USERPROFILE=str(home),
                   BERTH_HOME=str(state), BERTH_SOCKET=str(sock), BERTH_BIN=str(binary),
                   BERTH_NO_AUTOSTART="1", BERTH_DB="", BERTH_LOG_DIR=str(state / "logs"),
                   ZDOTDIR=str(home), XDG_CONFIG_HOME=str(home / ".config"), SHELL="/bin/zsh",
                   BERTH_SITE_ROOT=str(project), BERTH_SITE_OUTPUT=str(output.resolve()))
        for name in ("api.py", "worker.py", "oberth.yaml"):
            shutil.copyfile(REPO / "examples" / "hello-worktree" / name, project / name)
        manifest = (project / "oberth.yaml").read_text().replace("name: hello-worktree", "name: launchpad")
        for name in ("api", "worker"):
            manifest = manifest.replace(f"cmd: python3 -u {name}.py", "cmd: " + json.dumps(shlex.quote(sys.executable) + f" -u {name}.py"))
        (project / "oberth.yaml").write_text(manifest)
        (project / ".gitignore").write_text("__pycache__/\n")
        (home / ".zshrc").write_text("PROMPT='%F{cyan}launchpad%f %F{240}feature/checkout%f %F{green}❯%f '\n")
        (project / "README.md").write_text("# Launchpad\n\nA small local API and a background worker.\n")
        for args in [("init", "-q", "-b", "feature/checkout"), ("add", "."),
                     ("-c", "user.name=Demo Developer", "-c", "user.email=demo@example.invalid", "commit", "-qm", "Initial demo")]:
            subprocess.run(["git", *args], cwd=project, env=env, check=True, capture_output=True, timeout=10)
        (project / "README.md").write_text("# Launchpad\n\nA small local API and a background worker.\n\n## Local development\n\nRun `oberth up` and inspect each service before continuing.\nThe API and worker belong to this worktree.\n")
        def cli(*args: str, check: bool = True) -> subprocess.CompletedProcess:
            return subprocess.run([str(binary), *args], cwd=project, env=env,
                                  capture_output=True, text=True, check=check, timeout=40)
        with (base / "daemon.log").open("wb") as log:
            daemon = subprocess.Popen([str(binary), "serve"], cwd=home, env=env,
                                      stdout=log, stderr=log, start_new_session=True)
            cleanup_problem = None
            try:
                deadline = time.monotonic() + 15
                while not sock.exists():
                    if daemon.poll() is not None or time.monotonic() > deadline:
                        raise RuntimeError("Isolated daemon failed to start")
                    time.sleep(.05)
                cli("up", "--wait", "--json")
                cli("status", "--json")
                subprocess.run([str(runner)], cwd=project, env=env, check=True, timeout=90)
            finally:
                if daemon.poll() is None:
                    try:
                        cli("down", "--force", "--json")
                        stopped = json.loads(cli("status", "--json").stdout)
                        if any(s.get("running") for s in stopped["worktree"]["services"]):
                            raise RuntimeError("Demo services did not stop")
                    except (subprocess.SubprocessError, ValueError, KeyError, RuntimeError) as exc:
                        cleanup_problem = exc
                    try:
                        cli("daemon", "stop", "--json")
                        daemon.wait(timeout=8)
                    except subprocess.SubprocessError:
                        daemon.terminate()
                        try:
                            daemon.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            daemon.kill(); daemon.wait(timeout=5)
                if cleanup_problem:
                    raise RuntimeError("Capture cleanup failed; inspect the isolated demo") from cleanup_problem
        assets = {}
        for name in ("services", "git", "terminal", "history", "guide"):
            data = (output / f"{name}.png").read_bytes()
            if data[:8] != b"\x89PNG\r\n\x1a\n":
                raise ValueError("Capture did not produce a PNG")
            width, height = struct.unpack(">II", data[16:24])
            assets[f"{name}.png"] = {"width": width, "height": height, "sha256": hashlib.sha256(data).hexdigest()}
        main = subprocess.check_output(["git", "rev-parse", "origin/main"], cwd=REPO, text=True).strip()
        (output / "captures.json").write_text(json.dumps({"source_main": main,
            "method": "Native NSWindow cacheDisplay, live CLI/daemon and real zsh PTYs",
            "fixture": "Isolated copy of examples/hello-worktree; no user/provider data",
            "assets": assets}, indent=2) + "\n")
    print("Five native windows captured; demo services and owned shells stopped.")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", required=True, type=Path)
    parser.add_argument("--runner", required=True, type=Path)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    capture(args.binary, args.runner, args.output)
