#!/usr/bin/env python3
"""Darwin-only isolated candidate smoke. Never connects to the user's host.

Denies all /Users access, IP outbound, and other-process inspection/signalling.
Uses a private DB, synthetic completed run and no service-start RPC. A denied
system observation is not full scan or real state-change replay acceptance.
"""
import argparse
import fcntl
import json
import os
from pathlib import Path
import socket
import subprocess
import tempfile
import time
from check_storage_ownership import BASE, ROOT, Probe, build, git


class RPC:
    def __init__(self, endpoint):
        self.socket = socket.socket(socket.AF_UNIX)
        self.socket.settimeout(4)
        self.socket.connect(str(endpoint))
        self.file = self.socket.makefile("rwb")
        self.ident = 0
        self.notifications = []

    def call(self, method, params=None, error=False):
        self.ident += 1
        self.file.write((json.dumps({"jsonrpc": "2.0", "id": self.ident,
                                    "method": method, "params": params or {}}) + "\n").encode())
        self.file.flush()
        while True:
            reply = json.loads(self.file.readline())
            if reply.get("id") != self.ident:
                self.notifications.append(reply)
                continue
            if error:
                assert "error" in reply, reply
                return reply["error"]
            assert "error" not in reply, reply
            return reply["result"]

    def close(self):
        self.file.close()
        self.socket.close()


def verify(binary, legacy, report):
    assert os.uname().sysname == "Darwin", "requires macOS sandbox-exec"
    assert git(legacy, "rev-parse", "HEAD") == BASE
    subprocess.run(["git", "-C", str(legacy), "diff", "--exit-code", "--quiet", "HEAD", "--",
                    "internal/store", "internal/paths", "go.mod", "go.sum"], check=True)
    binary = binary.resolve(strict=True)
    checks = []
    with tempfile.TemporaryDirectory(prefix=".ownership-probes-", dir=ROOT / "engine") as source, \
         tempfile.TemporaryDirectory(prefix="oberth-storage-probe-", dir="/tmp") as scratch:
        root = Path(scratch).resolve()
        assert root.parent == Path("/private/tmp")
        (root / ".owned-fixture").write_text("disposable-store-probe\n")
        home = root / "home"
        home.mkdir()
        state = root / "state"
        state.mkdir()
        project = root / "project"
        project.mkdir()
        (project / "oberth.yaml").write_text("name: fixture\nservices: []\n")
        endpoint = root / "runtime-service.sock"
        db = state / "option-berth.db"
        env = {"PATH": "/usr/bin:/bin:/usr/sbin:/sbin", "HOME": str(home),
               "BERTH_HOME": str(state), "XDG_CONFIG_HOME": str(home / "config"),
               "BERTH_DB": str(db), "TMPDIR": str(root), "SHELL": "/usr/bin/false",
               "BERTH_RUNTIME_SERVICE_SOCKET": str(endpoint),
               "BERTH_RUNTIME_SERVICE_NO_AUTOSTART": "1", "BERTH_NO_UPDATE_CHECK": "1"}
        profile = '(version 1)(allow default)(deny file-read* file-write* (subpath "/Users"))(deny network-outbound (remote ip "*:*"))(deny process-info* (target others))(deny signal (target others))'
        prefix = ["/usr/bin/sandbox-exec", "-p", profile]
        subprocess.run(prefix + ["/usr/bin/true"], cwd=root, env=env, check=True)
        denied = subprocess.run(prefix + ["/usr/bin/stat", "-f", "%N", str(ROOT / "VERSION")],
                                cwd=root, env=env, capture_output=True)
        assert denied.returncode != 0, "sandbox must deny real user-tree metadata"
        checks.append("sandbox compiled; /Users metadata denied; outbound IP and other-process inspection/signalling remain denied")
        binaries = build(legacy, Path(source), root)
        seed = Probe(binaries["owned"], root, db)
        try:
            assert seed.opened["opened"] and seed.command("seed_fixture")["ok"]
        finally:
            seed.close()
        processes = []
        logs = []
        def launch(extra=None):
            stream = tempfile.TemporaryFile()
            logs.append(stream)
            proc = subprocess.Popen(prefix + [str(binary), "runtime", "service", "run"],
                                    cwd=root, env=dict(env, **(extra or {})),
                                    stdin=subprocess.DEVNULL, stdout=stream, stderr=stream)
            processes.append(proc)
            return proc, stream
        def refusal(extra=None):
            proc, stream = launch(extra)
            assert proc.wait(timeout=6) != 0, "second writer must be refused"
            stream.seek(0)
            text = stream.read().decode()
            assert "already" in text or "owned" in text, text
        def cli(*args, code=0, extra=None):
            result = subprocess.run(prefix + [str(binary), *args], cwd=project,
                                    env=dict(env, **(extra or {})), capture_output=True, text=True, timeout=8)
            assert result.returncode == code, (args, result.returncode, result.stdout, result.stderr)
            return result
        current = None
        old = None
        try:
            # Unmodified released Store plus a genuinely external old endpoint lock.
            old_dir = root / "unlisted-previous-endpoint"
            old_dir.mkdir()
            with (old_dir / "daemon.lock").open("w") as lock:
                fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
                old = Probe(binaries["legacy"], root, db)
                assert old.opened["opened"]
                refusal()
                assert old.command("write", name="old-still-owns")["ok"]
                old.close()
                old = None
            checks.append("actual candidate Serve refused unmodified v2 Store at unknown external endpoint; old Store remained writable")
            current, stream = launch()
            deadline = time.monotonic() + 8
            while not endpoint.exists() and current.poll() is None and time.monotonic() < deadline:
                time.sleep(0.03)
            if current.poll() is not None or not endpoint.exists():
                stream.seek(0)
                raise AssertionError(stream.read().decode())
            c = RPC(endpoint)
            try:
                hello = c.call("runtime.service.hello", {"client": "isolated-smoke", "client_version": "3.0.0"})
                assert hello["protocol_version"] == "3.0.0" and hello["pid"] == current.pid, hello
                assert c.call("runtime.service.status")["pid"] == current.pid
                old_error = c.call("daemon.status", error=True)
                assert "not_found" in json.dumps(old_error), old_error
                for section in ("schema", "worktrees", "run_exits"):
                    receipt = c.call("runtime.storage.read", {"section": section})
                    assert receipt["section"] == section and receipt[section] is not None, receipt
                assert receipt["run_exits"][0]["code"] == 42, receipt
            finally:
                c.close()
            checks.append("actual protocol-3 hello/status; retired RPC explicitly rejected; owner schema/worktree/exit42 receipts")
            refusal()
            refusal({"BERTH_RUNTIME_SERVICE_SOCKET": str(root / "second.sock")})
            second = root / "second-state"
            second.mkdir()
            refusal({"BERTH_HOME": str(second), "BERTH_RUNTIME_SERVICE_SOCKET": str(root / "third.sock")})
            checks.append("same endpoint, different endpoint, and different BERTH_HOME sharing DB all refused without stopping owner")
            online = cli("worktrees", "--json").stdout
            assert "wt_123456789abc" in online, online
            doctor = json.loads(cli("doctor", "--json", code=1).stdout)
            assert any(row["id"] == "db_ok" and row["status"] == "ok" for row in doctor["checks"]), doctor
            busy = cli("worktrees", "--no-runtime-service", "--json", code=1)
            assert not busy.stdout and "runtime_service_busy" in busy.stderr, busy
            mismatch = cli("worktrees", "--json", code=1,
                           extra={"BERTH_DB": str(root / "wrong.db")})
            assert not mismatch.stdout and "storage" in mismatch.stderr.lower(), mismatch
            other_db = root / "different.db"
            other_db.write_bytes(b"not-the-owned-database")
            mismatch = cli("worktrees", "--json", code=1, extra={"BERTH_DB": str(other_db)})
            assert not mismatch.stdout and "storage" in mismatch.stderr.lower(), mismatch
            assert other_db.read_bytes() == b"not-the-owned-database"
            assert "wt_123456789abc" in cli("worktrees", "--json",
                extra={"BERTH_DB": "../state/option-berth.db"}).stdout
            alias = root / "db-alias.db"
            alias.symlink_to(db)
            assert "wt_123456789abc" in cli("worktrees", "--json",
                                           extra={"BERTH_DB": str(alias)}).stdout
            checks.append("wrong DB at live socket refused; same-inode DB symlink accepted")
            checks.append("production CLI online ledger and doctor db_ok read through owner; direct mode returns nonzero busy JSON, never false empty history")
            for _ in range(2):
                c = RPC(endpoint)
                try:
                    c.call("runtime.service.hello", {"client": "isolated-stream", "client_version": "3.0.0"})
                    c.call("state.subscribe", {"events": True})
                finally:
                    c.close()
            checks.append("actual state subscription disconnect/reconnect accepted; not real state-change replay evidence")
            cli("runtime", "service", "stop")
            assert current.wait(timeout=6) == 0
            current = None
            assert "wt_123456789abc" in cli("worktrees", "--no-runtime-service", "--json").stdout
            reader = Probe(binaries["owned"], root, db, readonly=True)
            try:
                assert reader.opened["opened"]
            finally:
                reader.close()
            checks.append("normal production stop RPC and foreground exit0 released ownership; offline CLI history remained readable")
        finally:
            if old is not None:
                old.close()
            for proc in processes:
                if proc.poll() is None:
                    try:
                        c = RPC(endpoint)
                        c.call("runtime.service.shutdown")
                        c.close()
                        proc.wait(timeout=6)
                    finally:
                        if proc.poll() is None:
                            # Only this directly-created retained child; never PID/PGID discovery.
                            proc.kill()
                            proc.wait(timeout=3)
            for stream in logs:
                stream.close()
    result = {"candidate_revision": git(ROOT, "rev-parse", "HEAD"), "legacy_revision": BASE,
              "checks": checks, "all_passed": True,
              "boundary": "isolated empty/inert candidate only; system scanning denied; no true service changes or cursor replay"}
    if report:
        report.write_text(json.dumps(result, indent=2) + "\n")
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--legacy-engine", type=Path, required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    verify(args.binary, args.legacy_engine, args.report)
