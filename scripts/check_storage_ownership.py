#!/usr/bin/env python3
"""Compare real candidate ownership against an unmodified pinned v2 Store.

Builds disposable adapters with Go's compiler overlay; never edits the legacy
checkout or runs a product daemon/TestMain. Every database and process belongs
to this script. Online read tests query the owning production Store, not a copy.
"""
import argparse
from contextlib import closing
import json
import os
from pathlib import Path
import queue
import sqlite3
import subprocess
import tempfile
import threading
import time

ROOT = Path(__file__).resolve().parents[1]
BASE = "7a5d45b0e0eef1921408827121bddbfa7c4a8b9b"
GO = "/opt/homebrew/bin/go" if Path("/opt/homebrew/bin/go").exists() else "go"


def git(repo, *args):
    return subprocess.check_output(["git", "-C", str(repo), *args], text=True).strip()


class Probe:
    def __init__(self, binary, root, db, readonly=False):
        home = root / ("home-" + str(time.monotonic_ns()))
        home.mkdir()
        env = {"PATH": "/usr/bin:/bin", "HOME": str(home),
               "BERTH_HOME": str(home / "state"), "XDG_CONFIG_HOME": str(home / "config")}
        self.process = subprocess.Popen([str(binary), str(root), str(db), "readonly" if readonly else "writer"],
                                        env=env, cwd=root, stdin=subprocess.PIPE,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        self.lines = queue.Queue()
        def read():
            for line in self.process.stdout:
                self.lines.put(line)
            self.lines.put(None)
        threading.Thread(target=read, daemon=True).start()
        try:
            self.opened = self.receive()
            if not self.opened["opened"]:
                assert self.process.wait(timeout=3) == 2
        except BaseException:
            if self.process.poll() is None:
                self.process.kill()  # only this directly-created probe handle
                self.process.wait(timeout=3)
            for stream in (self.process.stdin, self.process.stdout, self.process.stderr):
                stream.close()
            raise

    def receive(self):
        line = self.lines.get(timeout=5)
        assert line is not None, "probe exited without a receipt"
        return json.loads(line)

    def command(self, op, **args):
        self.process.stdin.write(json.dumps({"op": op, **args}) + "\n")
        self.process.stdin.flush()
        return self.receive()

    def close(self):
        if self.process.poll() is None:
            try:
                assert self.command("close")["ok"]
                assert self.process.wait(timeout=5) == 0
            finally:
                if self.process.poll() is None:
                    # Only this directly-created, retained handle; never a host,
                    # process group, PID lookup, or user's process.
                    self.process.kill()
                    self.process.wait(timeout=3)
        for stream in (self.process.stdin, self.process.stdout, self.process.stderr):
            stream.close()


def build(legacy, source, outputs):
    template = (ROOT / "engine/internal/store/testdata/ownership_probe.go.tmpl").read_text()
    env = dict(os.environ, GOPROXY="off", GOTOOLCHAIN="local")
    binaries = {}
    for name, engine, expression in [
        ("legacy", legacy, "return store.OpenWith(path, store.Options{BusyTimeout: 30_000_000})"),
        ("owned", ROOT / "engine", "return store.OpenOwned(path)")]:
        main = source / (name + "_main.go")
        main.write_text(template.replace("/*PRODUCTION_OPEN*/", expression))
        overlay = source / (name + "_overlay.json")
        overlay.write_text(json.dumps({"Replace": {str(engine / "cmd/gen-schema/main.go"): str(main)}}))
        binary = outputs / name
        subprocess.run([GO, "build", "-overlay", str(overlay), "-o", str(binary), "./cmd/gen-schema"],
                       cwd=engine, env=env, check=True)
        binaries[name] = binary
    return binaries


def verify(legacy, report=None):
    legacy = legacy.resolve(strict=True)
    assert git(legacy, "rev-parse", "HEAD") == BASE, "legacy source must be exact released v2.4.0"
    subprocess.run(["git", "-C", str(legacy), "diff", "--exit-code", "--quiet", "HEAD", "--",
                    "internal/store", "internal/paths", "go.mod", "go.sum"], check=True)
    provenance = {"legacy_revision": BASE,
                  "legacy_store_tree": git(legacy, "rev-parse", "HEAD:engine/internal/store"),
                  "candidate_revision": git(ROOT, "rev-parse", "HEAD"),
                  "candidate_has_worktree_changes": bool(git(ROOT, "status", "--porcelain")),
                  "legacy_code_modified": False, "product_host_started": False}
    checks = []
    # Generated adapter source stays inside the authorized task checkout.
    with tempfile.TemporaryDirectory(prefix=".ownership-probes-", dir=ROOT / "engine") as sources, \
         tempfile.TemporaryDirectory(prefix="oberth-storage-probe-", dir="/tmp") as scratch:
        root = Path(scratch).resolve()
        (root / ".owned-fixture").write_text("disposable-store-probe\n")
        binaries = build(legacy, Path(sources), root)
        active = []
        def start(which, db, expected=True, readonly=False):
            t = time.monotonic()
            probe = Probe(binaries[which], root, root / db, readonly)
            active.append(probe)
            assert probe.opened["opened"] is expected, probe.opened
            if not expected:
                assert time.monotonic() - t < 1.5, "ownership refusal must not wait on the database"
            return probe
        try:
            old = start("legacy", "legacy-first.db")
            assert old.opened["max_connections"] == 8 and old.opened["locking"] == "normal"
            assert old.command("write", name="legacy-before")["ok"]
            blocked = start("owned", "legacy-first.db", expected=False)
            assert "already owned" in blocked.opened["error"]
            assert old.command("write", name="legacy-after-refusal")["ok"]
            assert len(old.command("read")["roots"]) == 2
            old.close()
            current = start("owned", "legacy-first.db")
            assert current.opened["max_connections"] == 1 and current.opened["locking"] == "exclusive"
            assert current.opened["journal"] == "wal"
            assert current.command("write", name="candidate-after-close")["ok"]
            current.close()
            start("legacy", "legacy-first.db").close()
            checks.append("legacy-first refuses candidate; failed acquisition releases; explicit old close permits candidate; normal owner close permits legacy")

            current = start("owned", "candidate-first.db")
            assert current.command("write", name="candidate-before")["ok"]
            start("legacy", "candidate-first.db", expected=False)
            start("owned", "candidate-first.db", expected=False)
            denied_read = start("owned", "candidate-first.db", expected=False, readonly=True)
            assert "already owned" in denied_read.opened["error"]
            assert current.command("write", name="candidate-after-refusals")["ok"]
            assert len(current.command("read")["roots"]) == 2
            assert current.command("stats")["max"] == 1
            assert not current.command("reopen")["reopened"], "same-PID reopen must not create a second writer"
            other = start("owned", "different.db")
            assert other.command("write", name="independent")["ok"]
            other.close()
            alias = root / "same-inode.db"
            os.link(root / "candidate-first.db", alias)
            start("owned", "same-inode.db", expected=False)
            current.close()
            reader = start("owned", "candidate-first.db", readonly=True)
            assert len(reader.command("read")["roots"]) == 2
            reader.close()
            checks.append("candidate-first refuses old/new/same-PID/same-inode writers; online owner reads stay available; external read fails fast then succeeds offline; different DB remains writable")

            old = start("legacy", "legacy-idle.db")
            assert old.command("write", name="legacy-initial")["ok"]
            assert old.command("drop_idle")["ok"]
            current = start("owned", "legacy-idle.db")
            assert not old.command("write", name="legacy-must-not-write")["ok"]
            assert current.command("write", name="candidate-exclusive")["ok"]
            assert len(current.command("read")["roots"]) == 2
            current.close()
            assert old.command("write", name="legacy-after-owner-close")["ok"]
            old.close()
            checks.append("live legacy object with all idle connections closed cannot resume writes while candidate owns SQLite")

            current = start("owned", "connection-loss.db")
            assert current.command("drop_idle")["ok"]
            lost = current.command("write", name="must-not-reacquire")
            assert not lost["ok"] and "connection was lost" in lost["error"]
            old = start("legacy", "connection-loss.db")
            assert old.command("write", name="legacy-after-connection-loss")["ok"]
            assert not current.command("write", name="candidate-must-stay-failed")["ok"]
            old.close(); current.close()
            checks.append("lost owner connection never reconnects behind a live legacy writer")

            current = start("owned", "abrupt-exit.db")
            assert current.command("write", name="committed-before-exit")["ok"]
            current.process.stdin.write('{"op":"exit_without_close"}\n'); current.process.stdin.flush()
            assert current.process.wait(timeout=3) == 17  # no signals, no Close
            replacement = start("owned", "abrupt-exit.db")
            assert len(replacement.command("read")["roots"]) == 1, "writes must commit before shutdown"
            replacement.close()
            checks.append("ungraceful direct child exit releases kernel ownership and preserves committed WAL data")

            old = start("legacy", "future.db")
            assert old.command("future_schema")["ok"]
            old.close()
            future = start("owned", "future.db", expected=False)
            assert "999" in future.opened["error"]
            with closing(sqlite3.connect(root / "future.db", timeout=.1)) as fixture:
                assert fixture.execute("SELECT MAX(version) FROM schema_version").fetchone()[0] == 999
                fixture.execute("UPDATE schema_version SET version=10 WHERE version=999")
                fixture.commit()
            start("owned", "future.db").close()
            corrupt = root / "corrupt.db"
            corrupt.write_bytes(b"fixture-invalid-sqlite")
            start("owned", "corrupt.db", expected=False)
            assert corrupt.read_bytes() == b"fixture-invalid-sqlite"
            assert not list(root.glob("corrupt.db.corrupt-*")), "ownership startup must not park/reset a database"
            checks.append("migration failure releases ownership without resetting future schema; corrupt database remains byte-identical")
        finally:
            for probe in reversed(active):
                probe.close()
    result = {**provenance, "checks": checks, "all_passed": True,
              "boundary": "Store production APIs across real processes, not full daemon/scan/replay acceptance"}
    if report:
        report.write_text(json.dumps(result, ensure_ascii=False, indent=2) + "\n")
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--legacy-engine", type=Path, required=True)
    parser.add_argument("--report", type=Path)
    args = parser.parse_args()
    verify(args.legacy_engine, args.report)
