#!/usr/bin/env python3
"""Read-only production CLI naming regression; never starts a Runtime Service.

Only an explicitly supplied candidate binary is used. Environment is built from
an allowlist, not copied from the caller; HOME/state/socket are disposable.
"""
import argparse
import json
import pathlib
import subprocess
import tempfile


def check(binary):
    results = []
    with tempfile.TemporaryDirectory(prefix="oberth-naming-cli-", dir="/tmp") as tmp:
        root = pathlib.Path(tmp)
        home, state = root / "home", root / "state"
        home.mkdir(); state.mkdir()
        env = {
            "PATH": "/usr/bin:/bin:/usr/sbin:/sbin",
            "HOME": str(home), "USERPROFILE": str(home),
            "SHELL": str(home / "no-login-shell"),
            "XDG_CONFIG_HOME": str(home / ".config"),
            "XDG_RUNTIME_DIR": "", "BERTH_HOME": str(state),
            "BERTH_RUNTIME_SERVICE_SOCKET": str(root / "service.sock"),
            "BERTH_RUNTIME_SERVICE_NO_AUTOSTART": "1", "BERTH_NO_UPDATE_CHECK": "1",
            "NO_COLOR": "1", "TERM": "dumb",
        }

        def run(args, code=0, extra=None):
            command_env = dict(env)
            command_env.update(extra or {})
            proc = subprocess.run([str(binary), *args], cwd=home, env=command_env,
                                  capture_output=True, text=True, timeout=10)
            if proc.returncode != code:
                raise AssertionError(f"{args}: exit {proc.returncode}, expected {code}; {proc.stderr}")
            results.append({"args": args, "exit": proc.returncode})
            return proc

        help_text = run(["--help"]).stdout
        assert "runtime" in help_text
        assert not any(line.split()[:1] in (["daemon"], ["serve"]) for line in help_text.splitlines())
        service_help = run(["runtime", "service", "--help"]).stdout
        for leaf in ("run", "status", "stop", "restart", "path", "log", "schema"):
            assert any(line.split()[:1] == [leaf] for line in service_help.splitlines()), leaf
        run(["runtime", "service", "run", "--help"])
        path = json.loads(run(["runtime", "service", "path", "--json"]).stdout)
        assert str(root / "service.sock") in json.dumps(path)
        stopped = run(["runtime", "service", "status", "--json"], code=1)
        assert json.loads(stopped.stdout)["running"] is False
        assert not stopped.stderr.strip(), "JSON status must not mix in diagnostics"
        for args in (["daemon", "status", "--json"], ["serve", "--help"], ["--no-daemon", "status", "--json"]):
            proc = run(args, code=2)
            assert not proc.stdout.strip()
            assert "runtime" in proc.stderr
            if "--json" in args:
                assert "error" in json.loads(proc.stderr)
        for key in ("BERTH_SOCKET", "BERTH_DAEMON_DETACHED", "BERTH_ALLOW_TEST_DAEMON", "BERTH_NO_AUTOSTART"):
            proc = run(["runtime", "service", "status", "--json"], code=2, extra={key: "retired-private-value"})
            assert not proc.stdout.strip()
            error = json.loads(proc.stderr)
            assert key in json.dumps(error)
            assert "retired-private-value" not in proc.stderr
        (state / "config.yaml").write_text("daemon: {}\n")
        proc = run(["runtime", "service", "status", "--json"], code=2)
        assert "runtime_service" in proc.stderr and not proc.stdout.strip()
        (state / "config.yaml").write_text("runtime_service: {}\n")
        checks = "runtime_service_reachable,runtime_service_version_matches,runtime_service_build_matches,runtime_service_protocol"
        doctor = run(["doctor", "--only", checks, "--json"], code=1)
        rows = json.loads(doctor.stdout)
        text = json.dumps(rows)
        assert "runtime_service_reachable" in text
        assert "daemon_reachable" not in text and "no daemon" not in text
    return results


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=pathlib.Path, required=True)
    args = parser.parse_args()
    result = check(args.binary.resolve(strict=True))
    print(json.dumps({"checks": result, "service_started": False}, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    main()
