#!/usr/bin/env python3
"""Measure one real oberth binary on throwaway main/linked worktrees.

Usage: python3 scripts/verify-e2e.py --binary /path/to/oberth --output result.json
Only generated fixtures are started/stopped. No caller state directory is used.
Raw per-request latency is retained; sample p95 is not a population guarantee.
Run baseline and candidate on the same host with identical arguments.
"""
from __future__ import annotations

import argparse
from collections import Counter, deque
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import queue
import select
import shlex
import shutil
import signal
import socket
import subprocess
import sys
import tempfile
import threading
import time
from typing import Any


def distribution(values: list[float]) -> dict[str, Any]:
    if not values:
        raise ValueError("empty measurement")
    ordered = sorted(values)
    return {"n": len(values), "p50": ordered[math.ceil(len(values) * .50) - 1],
            "p95": ordered[math.ceil(len(values) * .95) - 1], "max": ordered[-1],
            "raw": values}


def cpu_seconds(text: str) -> float:
    days, sep, rest = text.strip().partition("-")
    total = int(days) * 86400 if sep else 0
    fields = (rest if sep else days).split(":")
    for index, field in enumerate(reversed(fields)):
        total += float(field) * 60 ** index
    return total


def port_key(row: dict[str, Any]) -> str:
    key = f"{row['port']}:{row['bind_address']}"
    host = row.get("host", "localhost")
    return key if host in ("", "localhost") else host + "/" + key


def apply_ports(rows: dict[str, dict[str, Any]], delta: dict[str, Any]) -> None:
    changes = delta.get("ports", {})
    for key in changes.get("removed", []) or []:
        rows.pop(key, None)
    for row in (changes.get("added", []) or []) + (changes.get("updated", []) or []):
        rows[port_key(row)] = row


def visible_stats(snapshot: dict[str, Any], wanted_ports: list[int]) -> dict[int, dict[str, Any]]:
    """Require actual stats on our generated services, not just a CLI flag."""
    by_port = {row["port"]: row.get("stats") for row in snapshot["ports"]}
    result = {}
    for port in wanted_ports:
        stats = by_port.get(port)
        rss = stats.get("memory_rss_bytes") if isinstance(stats, dict) else None
        if type(rss) is not int or rss <= 0:
            raise RuntimeError(f"no positive process memory observation on fixture port {port}")
        result[port] = stats
    return result


class RPC:
    def __init__(self, path: Path):
        self.socket = socket.socket(socket.AF_UNIX)
        self.socket.settimeout(20)
        self.socket.connect(str(path))
        self.file = self.socket.makefile("rb")
        self.pending: deque[dict[str, Any]] = deque()
        self.number = 0
        self.call("daemon.hello", {"client": "cli", "client_version": "p4", "keepalive": True})

    def close(self) -> None:
        self.file.close()
        self.socket.close()

    def read(self) -> dict[str, Any]:
        line = self.file.readline(4 * 1024 * 1024 + 1)
        if not line or len(line) > 4 * 1024 * 1024:
            raise RuntimeError("closed or oversized RPC frame")
        return json.loads(line)

    def call(self, method: str, params: dict[str, Any]) -> dict[str, Any]:
        self.number += 1
        self.socket.sendall((json.dumps({"jsonrpc": "2.0", "id": self.number,
                                        "method": method, "params": params}) + "\n").encode())
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            message = self.read()
            if message.get("id") != self.number:
                self.pending.append(message)
                continue
            if message.get("error"):
                raise RuntimeError(f"{method}: {message['error']}")
            return message["result"]
        raise TimeoutError(f"{method}: no matching response")

    def delta(self) -> dict[str, Any]:
        deadline = time.monotonic() + 20
        while time.monotonic() < deadline:
            message = self.pending.popleft() if self.pending else self.read()
            if message.get("method") == "state.delta":
                return message["params"]
        raise TimeoutError("no delta before deadline")


class StatsDrain:
    """Keep the resumed statistics subscription read throughout lifecycle churn.

    The reader owns the RPC input after replay reconstruction. A final request
    on that SAME connection proves it was not silently evicted for backpressure.
    Only counters are retained; notifications are not an unbounded second log.
    """
    def __init__(self, conn: RPC, roots: set[str]):
        self.conn, self.roots = conn, roots
        self.closing = threading.Event()
        self.responses: queue.Queue[dict[str, Any]] = queue.Queue()
        self.failure: BaseException | None = None
        self.frames = 0
        self.stats_updates = 0
        self.observed_roots: set[str] = set()
        self.closed = False
        # Shutdown in close interrupts this blocking read. A quiet subscription
        # must not be mistaken for failure merely because it has no new frames.
        conn.socket.settimeout(None)
        self.thread = threading.Thread(target=self.read, daemon=True)
        self.thread.start()

    def read(self) -> None:
        try:
            while not self.closing.is_set():
                message = self.conn.pending.popleft() if self.conn.pending else self.conn.read()
                if "id" in message:
                    self.responses.put(message)
                    continue
                self.frames += 1
                if message.get("method") != "state.delta":
                    continue
                changes = message.get("params", {}).get("ports", {})
                for row in (changes.get("added", []) or []) + (changes.get("updated", []) or []):
                    root = row.get("project_root")
                    stats = row.get("stats")
                    rss = stats.get("memory_rss_bytes") if isinstance(stats, dict) else None
                    if root in self.roots and type(rss) is int and rss > 0:
                        self.stats_updates += 1
                        self.observed_roots.add(root)
        except BaseException as exc:
            if not self.closing.is_set():
                self.failure = exc

    def finish(self) -> dict[str, Any]:
        try:
            self.conn.number += 1
            request_id = self.conn.number
            self.conn.socket.sendall((json.dumps({"jsonrpc": "2.0", "id": request_id,
                "method": "daemon.status", "params": {}}) + "\n").encode())
            reply = self.responses.get(timeout=20)
            if reply.get("id") != request_id or reply.get("error") or "result" not in reply:
                raise RuntimeError(f"statistics subscription barrier failed: {reply}")
        finally:
            self.close()
        if self.failure is not None:
            raise RuntimeError("statistics subscription reader failed") from self.failure
        if self.observed_roots != self.roots:
            raise RuntimeError(f"statistics stream missed fixture roots: {self.observed_roots}")
        return {"same_connection_barrier": True, "frames_consumed": self.frames,
                "fixture_stats_updates": self.stats_updates,
                "observed_roots": sorted(self.observed_roots), "reader_error": None}

    def close(self) -> None:
        if self.closed:
            return
        self.closed = True
        self.closing.set()
        try:
            self.conn.socket.shutdown(socket.SHUT_RDWR)
        except OSError:
            pass
        self.thread.join(timeout=5)
        self.conn.close()
        if self.thread.is_alive():
            raise RuntimeError("statistics stream reader failed to stop")


class ReconnectProxy:
    """A real Unix stream proxy with an explicit disconnect/reconnect barrier."""
    def __init__(self, path: Path, target: Path):
        self.path, self.target = path, target
        self.listener = socket.socket(socket.AF_UNIX)
        self.listener.bind(str(path))
        self.listener.listen(2)
        self.listener.settimeout(.1)
        self.stop = threading.Event()
        self.resume = threading.Event()
        self.resume.set()
        self.lock = threading.Lock()
        self.active: list[socket.socket] = []
        self.subscriptions: queue.Queue[int] = queue.Queue()
        self.errors: queue.Queue[BaseException] = queue.Queue()
        self.thread = threading.Thread(target=self.serve, daemon=True)
        self.thread.start()

    def disconnect(self) -> None:
        self.resume.clear()
        with self.lock:
            for conn in self.active:
                try:
                    conn.shutdown(socket.SHUT_RDWR)
                except OSError:
                    pass

    def close(self) -> None:
        self.stop.set()
        self.resume.set()
        self.disconnect()
        self.resume.set()
        self.listener.close()
        self.thread.join(timeout=5)
        if self.thread.is_alive():
            raise RuntimeError("proxy failed to stop")

    def serve(self) -> None:
        try:
            while not self.stop.is_set():
                try:
                    downstream, _ = self.listener.accept()
                except socket.timeout:
                    continue
                with downstream:
                    while not self.resume.wait(.1):
                        if self.stop.is_set():
                            return
                    if self.stop.is_set():
                        return
                    with socket.socket(socket.AF_UNIX) as upstream:
                        upstream.connect(str(self.target))
                        downstream.settimeout(2)
                        upstream.settimeout(2)
                        with self.lock:
                            self.active = [downstream, upstream]
                        request = b""
                        try:
                            while not self.stop.is_set():
                                readable, _, _ = select.select([downstream, upstream], [], [], .1)
                                closed = False
                                for source in readable:
                                    data = source.recv(65536)
                                    if not data:
                                        closed = True
                                        break
                                    if source is downstream:
                                        request += data
                                        while b"\n" in request:
                                            line, request = request.split(b"\n", 1)
                                            message = json.loads(line)
                                            if message.get("method") == "state.subscribe":
                                                self.subscriptions.put(message.get("params", {}).get("after_seq", 0))
                                        if len(request) > 4 * 1024 * 1024:
                                            raise RuntimeError("oversized proxy request")
                                    (upstream if source is downstream else downstream).sendall(data)
                                if closed:
                                    break
                        except (BrokenPipeError, ConnectionResetError):
                            pass
                        finally:
                            with self.lock:
                                self.active = []
        except OSError as exc:
            if not self.stop.is_set():
                self.errors.put(exc)
        except BaseException as exc:
            self.errors.put(exc)


class Trial:
    def __init__(self, binary: Path, root: Path):
        self.binary, self.root = binary, root
        self.home, self.state = root / "h", root / "state"
        self.home.mkdir()
        self.state.mkdir(mode=0o700)
        self.roots = [self.home / "main", self.home / "feature"]
        self.sock = root / "d.sock"
        self.calls = root / "calls.tsv"
        self.calls.touch()
        self.real_ps = shutil.which("ps")
        if not self.real_ps:
            raise RuntimeError("ps is required for resource/cleanup evidence")
        shim = root / "shim"
        shim.mkdir()
        for name in ("ps", "ss", "lsof", "netstat", "git", "docker"):
            real = shutil.which(name)
            if real:
                target = shim / name
                target.write_text("#!/bin/sh\n" +
                    f"printf '%s\\t%s\\t%s\\n' \"$$\" \"$PPID\" {shlex.quote(name)} >> {shlex.quote(str(self.calls))}\n" +
                    f"exec {shlex.quote(real)} \"$@\"\n")
                target.chmod(0o700)
        self.env = dict(os.environ, HOME=str(self.home), USERPROFILE=str(self.home),
                        BERTH_HOME=str(self.state), BERTH_DB="", BERTH_LOG_DIR=str(self.state / "logs"),
                        BERTH_SOCKET=str(self.sock), BERTH_NO_AUTOSTART="1", XDG_RUNTIME_DIR="",
                        SHELL="/nonexistent", PATH=str(shim) + os.pathsep + os.environ["PATH"])
        self.proc: subprocess.Popen[bytes] | None = None
        self.log = (root / "daemon.log").open("wb")
        self.connections: list[RPC] = []

    def command(self, cwd: Path, *args: str) -> dict[str, Any]:
        done = subprocess.run([str(self.binary), *args], cwd=cwd, env=self.env,
                              capture_output=True, timeout=35)
        if done.returncode:
            raise RuntimeError(f"{args}: exit={done.returncode}\n{done.stderr.decode(errors='replace')}\n{done.stdout.decode(errors='replace')}")
        result = json.loads(done.stdout)
        if result.get("errors"):
            raise RuntimeError(f"{args}: {result['errors']}")
        return result

    def provision(self) -> None:
        main, linked = self.roots
        main.mkdir()
        (main / "server.py").write_text(Path(__file__).with_name("p4_http_fixture.py").read_text())
        (main / "worker.py").write_text("import time\nprint('worker-ready', flush=True)\ntime.sleep(3600)\n")
        py = shlex.quote(sys.executable)
        (main / "oberth.yaml").write_text(
            "name: p4\nservices:\n  - name: api\n    cmd: " + json.dumps(py + " -u server.py") +
            "\n    port: auto\n    health: /health\n  - name: worker\n    cmd: " +
            json.dumps(py + " -u worker.py") + "\n    depends_on: [api]\n")
        for args in (("init", "-q"), ("add", "."),
                     ("-c", "user.name=P4 Fixture", "-c", "user.email=p4@example.invalid", "commit", "-qm", "fixture"),
                     ("worktree", "add", "--detach", str(linked), "HEAD")):
            subprocess.run(["git", *args], cwd=main, env=self.env, capture_output=True, check=True, timeout=10)
        self.proc = subprocess.Popen([str(self.binary), "serve"], cwd=self.home, env=self.env,
                                     stdout=self.log, stderr=self.log, start_new_session=True)
        deadline = time.monotonic() + 15
        while not self.sock.exists():
            if self.proc.poll() is not None or time.monotonic() >= deadline:
                raise RuntimeError("daemon did not open isolated socket")
            time.sleep(.05)

    def rpc(self) -> RPC:
        conn = RPC(self.sock)
        self.connections.append(conn)
        return conn

    def status(self, root: Path) -> dict[str, Any]:
        return self.command(root, "status", "--json", "--no-mark")

    def ready(self, root: Path) -> dict[str, Any]:
        deadline = time.monotonic() + 15
        while True:
            status = self.status(root)
            services = {s["name"]: s for s in status["worktree"]["services"]}
            if all(services.get(name, {}).get("running") for name in ("api", "worker")):
                api = services["api"]
                if api.get("port_actual"):
                    return status
            if time.monotonic() >= deadline:
                raise RuntimeError(f"services never ready: {status}")
            time.sleep(.1)

    def cli_reconnect(self) -> dict[str, Any]:
        """Keep one real scoped `events` process alive across a transport loss."""
        proxy = ReconnectProxy(self.root / "proxy.sock", self.sock)
        out: queue.Queue[dict[str, Any] | BaseException] = queue.Queue()
        errors = (self.root / "events.stderr").open("wb")
        cmd = subprocess.Popen([str(self.binary), "events", "--worktree", str(self.roots[0])],
            cwd=self.roots[0], env=dict(self.env, BERTH_SOCKET=str(proxy.path)),
            stdout=subprocess.PIPE, stderr=errors)
        assert cmd.stdout is not None

        def read_output() -> None:
            try:
                for line in cmd.stdout:
                    out.put(json.loads(line))
            except BaseException as exc:
                out.put(exc)

        reader = threading.Thread(target=read_output, daemon=True)
        reader.start()
        try:
            first = out.get(timeout=20)
            if isinstance(first, BaseException):
                raise first
            if first.get("type") != "state.snapshot" or not first.get("seq"):
                raise RuntimeError(f"events did not start from a nonzero snapshot: {first}")
            worktrees = first.get("worktrees", [])
            if not worktrees:
                raise RuntimeError("events snapshot omitted the selected worktree")
            selected = {w["source"]["worktree_id"] for w in worktrees}
            if proxy.subscriptions.get(timeout=5) != 0:
                raise RuntimeError("first subscription unexpectedly resumed")
            cursor = first["seq"]
            proxy.disconnect()
            self.command(self.roots[0], "restart", "--only", "api", "--json")
            self.ready(self.roots[0])
            proxy.resume.set()
            after_seq = proxy.subscriptions.get(timeout=20)
            if after_seq < cursor:
                raise RuntimeError("CLI discarded its cursor after transport loss")
            deadline = time.monotonic() + 20
            recovered = None
            last_seq = cursor
            while time.monotonic() < deadline:
                message = out.get(timeout=max(.01, deadline - time.monotonic()))
                if isinstance(message, BaseException):
                    raise message
                if message.get("seq", 0) < last_seq:
                    raise RuntimeError("CLI emitted a decreasing stream sequence")
                last_seq = message.get("seq", last_seq)
                if (message.get("type") == "state.changed" and message.get("seq", 0) > after_seq
                    and message.get("source", {}).get("worktree_id") in selected
                    and "port_changed" in message.get("changed", [])):
                    recovered = message
                    break
            if recovered is None or cmd.poll() is not None or not proxy.errors.empty():
                raise RuntimeError("scoped CLI failed to resume the missed runtime change")
            return {"initial_seq": cursor, "resubscribe_after_seq": after_seq,
                    "recovered_seq": recovered["seq"], "changed": recovered["changed"],
                    "same_cli_process": True, "scoped_runtime_change_observed": True}
        finally:
            if cmd.poll() is None:
                cmd.terminate()
            try:
                cmd.wait(timeout=5)
            except subprocess.TimeoutExpired:
                cmd.kill()
                cmd.wait(timeout=5)
            cmd.stdout.close()
            reader.join(timeout=5)
            proxy.close()
            errors.close()
            if reader.is_alive():
                raise RuntimeError("events output reader failed to stop")

    def resource(self) -> dict[str, float]:
        assert self.proc is not None
        if sys.platform.startswith("linux"):
            fields = Path(f"/proc/{self.proc.pid}/stat").read_text().rsplit(")", 1)[1].split()
            ticks = os.sysconf("SC_CLK_TCK")
            return {"cpu_seconds": (int(fields[11]) + int(fields[12])) / ticks,
                    "rss_kib": int(fields[21]) * os.sysconf("SC_PAGE_SIZE") / 1024,
                    "cpu_resolution_seconds": 1 / ticks}
        line = subprocess.check_output([self.real_ps, "-p", str(self.proc.pid), "-o", "time=,rss="],
                                       text=True, timeout=5).split()
        if len(line) != 2:
            raise RuntimeError(f"unreadable process resources: {line}")
        return {"cpu_seconds": cpu_seconds(line[0]), "rss_kib": int(line[1])}

    def idle(self, seconds: float) -> dict[str, Any]:
        assert self.proc is not None
        start_offset = self.calls.stat().st_size
        before = self.resource()
        start = time.perf_counter()
        time.sleep(seconds)
        elapsed = time.perf_counter() - start
        after = self.resource()
        with self.calls.open() as log:
            log.seek(start_offset)
            counts = Counter(parts[2] for line in log for parts in [line.strip().split("\t")]
                             if len(parts) == 3 and parts[1] == str(self.proc.pid))
        return {"seconds": elapsed, "cpu_single_core_percent": 100 * (after["cpu_seconds"] - before["cpu_seconds"]) / elapsed,
                "before": before, "after": after, "instrumented_direct_commands": dict(counts)}

    def owned_pids(self) -> list[int]:
        needle = "BERTH_SOCKET=" + str(self.sock)
        result = []
        if sys.platform.startswith("linux"):
            for entry in Path("/proc").iterdir():
                if not entry.name.isdigit() or int(entry.name) <= 1 or int(entry.name) == os.getpid():
                    continue
                try:
                    if needle.encode() in (entry / "environ").read_bytes().split(b"\0"):
                        result.append(int(entry.name))
                except (FileNotFoundError, PermissionError, ProcessLookupError):
                    pass
            return result
        text = subprocess.check_output([self.real_ps, "eww", "-axo", "pid=,command="], text=True, timeout=5)
        for line in text.splitlines():
            parts = line.split()
            if len(parts) > 1 and needle in parts and parts[0].isdigit():
                pid = int(parts[0])
                if pid > 1 and pid != os.getpid():
                    result.append(pid)
        return result

    def close(self) -> None:
        for conn in self.connections:
            try:
                conn.close()
            except OSError:
                pass
        if self.proc is not None and self.proc.poll() is None:
            for root in self.roots:
                try:
                    self.command(root, "down", "--force", "--json")
                except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
                    pass
            try:
                self.command(self.home, "daemon", "stop", "--json")
            except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
                pass
        # Only this trial's unguessable socket marker authorizes fallback cleanup.
        for sig in (signal.SIGTERM, signal.SIGKILL):
            for pid in self.owned_pids():
                try:
                    os.kill(pid, sig)
                except ProcessLookupError:
                    pass
            time.sleep(.1)
        if self.proc is not None:
            if self.proc.poll() is None:
                self.proc.kill()
            self.proc.wait(timeout=5)
        self.log.close()
        remaining = self.owned_pids()
        if remaining:
            raise RuntimeError(f"trial left owned processes: {remaining}")


def run(args: argparse.Namespace) -> dict[str, Any]:
    binary = args.binary.resolve(strict=True)
    include = ["stats"] if getattr(args, "include_stats", False) else []
    report: dict[str, Any] = {"label": args.label, "platform": platform.platform(),
        "binary_sha256": hashlib.sha256(binary.read_bytes()).hexdigest(),
        "workload": {"stats_subscriber": bool(include), "samples": args.samples, "cycles": args.cycles, "idle_seconds": args.idle_seconds},
        "measurement": "per-call wall time including CLI launch; nearest-rank empirical quantiles",
        "limits": ["instrumented collector commands only; native syscalls not counted",
                   "ps cumulative CPU resolution depends on host", "finite sample p95, not a stable population estimate"]}
    with tempfile.TemporaryDirectory(prefix="bp4-", dir="/tmp") as tmp:
        trial = Trial(binary, Path(tmp).resolve())
        stats_drain: StatsDrain | None = None
        try:
            trial.provision()
            control = trial.rpc()
            observer = trial.rpc()
            observer.call("state.subscribe", {"include": include, "events": False})
            report["idle_empty"] = trial.idle(args.idle_seconds)
            for root in trial.roots:
                trial.command(root, "up", "--json")
                trial.ready(root)
            docs = [trial.ready(root) for root in trial.roots]
            services = [{s["name"]: s for s in d["worktree"]["services"]} for d in docs]
            ports = [s["api"]["port_actual"] for s in services]
            if len(set(ports)) != 2:
                raise RuntimeError(f"worktrees share an auto port: {ports}")
            report["worktree_ports"] = ports
            snap = control.call("state.snapshot", {})
            health = {p["port"]: p.get("health", {}).get("status") if p.get("health") else None for p in snap["ports"]}
            if any(health.get(port) != "ok" for port in ports):
                raise RuntimeError(f"configured health not observed: {health}")
            report["configured_health_ok"] = True
            report["idle_multiworktree"] = trial.idle(args.idle_seconds)
            if include:
                report["observed_stats"] = visible_stats(control.call("state.snapshot", {"include": include}), ports)
            timings = []
            for i in range(args.samples):
                begin = time.perf_counter()
                trial.status(trial.roots[i % 2])
                timings.append((time.perf_counter() - begin) * 1000)
            report["status_ms"] = distribution(timings)
            # Abrupt real socket close, a real process restart while disconnected,
            # and replay from the last applied sequence; no unsubscribe shortcut.
            anchor = observer.call("state.subscribe", {"include": include, "events": False})
            cursor = anchor["seq"]
            observer.close()
            trial.command(trial.roots[0], "restart", "--only", "api", "--json")
            trial.ready(trial.roots[0])
            wanted = control.call("state.snapshot", {})
            resumed = trial.rpc()
            initial = resumed.call("state.subscribe", {"after_seq": cursor, "events": False, "include": include})
            if initial["seq"] != cursor or wanted["seq"] <= cursor:
                raise RuntimeError("cursor was not replayed across the real restart")
            rows = {port_key(row): row for row in initial["ports"]}
            seq, deltas = cursor, 0
            while seq < wanted["seq"]:
                delta = resumed.delta()
                if delta["seq"] <= seq:
                    raise RuntimeError("replayed sequence did not increase")
                seq, deltas = delta["seq"], deltas + 1
                apply_ports(rows, delta)
            # Compare ownership, not probe latency/uptime that can change in flight.
            owned = lambda values: {(r["port"], r["pid"], r.get("project_root")) for r in values
                                    if r.get("project_root") in {str(p.resolve()) for p in trial.roots}}
            if owned(rows.values()) != owned(wanted["ports"]):
                raise RuntimeError("replay reconstructed different worktree ownership")
            report["reconnect"] = {"cursor": cursor, "caught_up_seq": seq, "deltas": deltas, "ownership_matches": True}
            if include:
                stats_drain = StatsDrain(resumed, {str(p.resolve()) for p in trial.roots})
            report["cli_reconnect"] = trial.cli_reconnect()
            before = trial.resource()
            up, down, rss = [], [], []
            for _ in range(args.cycles):
                start = time.perf_counter()
                trial.command(trial.roots[0], "down", "--json")
                down.append((time.perf_counter() - start) * 1000)
                stopped = trial.status(trial.roots[0])["worktree"]["services"]
                if any(service.get("running") for service in stopped):
                    raise RuntimeError("down returned without stopping the main checkout")
                # The linked checkout must remain live when main is stopped.
                trial.ready(trial.roots[1])
                start = time.perf_counter()
                trial.command(trial.roots[0], "up", "--json")
                trial.ready(trial.roots[0])
                up.append((time.perf_counter() - start) * 1000)
                rss.append(trial.resource()["rss_kib"])
            report["cycles"] = {"n": args.cycles, "up_ready_ms": distribution(up), "down_ms": distribution(down),
                                "before": before, "after": trial.resource(), "rss_kib_each": rss,
                                "linked_checkout_remained_live": True}
            if stats_drain is not None:
                report["stats_subscription"] = stats_drain.finish()
                stats_drain = None
        except BaseException:
            # These are generated fixture logs only, never caller state.
            for log in sorted(trial.state.glob("logs/**/*.log")):
                sys.stderr.write(f"\n--- fixture log {log.relative_to(trial.root)} ---\n")
                sys.stderr.write(log.read_text(errors="replace")[-6000:])
            for root in trial.roots:
                if root.exists():
                    try:
                        sys.stderr.write("\n--- fixture status ---\n" + json.dumps(trial.status(root)))
                    except (OSError, ValueError, RuntimeError, subprocess.SubprocessError) as diagnostic:
                        sys.stderr.write(f"\nstatus unavailable: {diagnostic}\n")
            trial.log.flush()
            sys.stderr.write((Path(tmp) / "daemon.log").read_text(errors="replace")[-12000:])
            raise
        finally:
            try:
                if stats_drain is not None:
                    stats_drain.close()
            finally:
                trial.close()
        report["cleanup_remaining_owned_pids"] = []
    report["temporary_fixture_removed"] = not Path(tmp).exists()
    return report


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--label", default="candidate")
    parser.add_argument("--include-stats", action="store_true", help="keep a real statistics subscriber connected (separate workload)")
    parser.add_argument("--samples", type=int, default=300)
    parser.add_argument("--cycles", type=int, default=20)
    parser.add_argument("--idle-seconds", type=float, default=12)
    args = parser.parse_args()
    if args.samples < 1 or args.cycles < 1 or args.idle_seconds <= 0:
        parser.error("samples, cycles and idle-seconds must be positive")
    report = run(args)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(json.dumps(report, indent=2) + "\n")
    print(json.dumps({"label": report["label"], "samples": args.samples, "cycles": args.cycles,
                      "status_p95_ms": report["status_ms"]["p95"], "cleanup": "complete"}))


if __name__ == "__main__":
    main()
