#!/usr/bin/env python3
"""Exercise the real CLI/daemon with disposable native API and no-port workers.

Windows uses a unique named pipe. Unix uses a short private socket. No process
is located or killed by a guessed PID; fallback cleanup is a fixture-owned stop
file and every generated worker has a 90-second self-expiry.
"""
from __future__ import annotations
import argparse
import json
import os
from pathlib import Path
import shutil
import socket
import subprocess
import tempfile
import time
import uuid

FIXTURE_SOURCE = r'''package main
import (
 "encoding/json"
 "fmt"
 "net"
 "net/http"
 "os"
 "path/filepath"
 "time"
)
func main() {
 if len(os.Args) != 2 { os.Exit(2) }
 mode := os.Args[1]
 if mode == "fail" { fmt.Println("fixture exit seven"); os.Exit(7) }
 time.AfterFunc(90*time.Second, func(){os.Exit(124)})
 go func(){ for { if _,err:=os.Stat("stop-"+mode);err==nil {os.Exit(0)};time.Sleep(50*time.Millisecond) } }()
 identity:=map[string]any{"pid":os.Getpid(),"run_id":os.Getenv("BERTH_RUN_ID"),"mode":mode}
 if mode == "api" {
  listener,err:=net.Listen("tcp","127.0.0.1:"+os.Getenv("PORT"));if err!=nil {panic(err)}
  identity["port"]=listener.Addr().(*net.TCPAddr).Port
  raw,_:=json.Marshal(identity);if err:=os.WriteFile(filepath.Join("identity-"+mode+".json"),raw,0600);err!=nil{panic(err)}
  fmt.Println("fixture API ready")
  http.Serve(listener,http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Write([]byte("fixture-ok"))}))
 } else if mode == "worker" {
  raw,_:=json.Marshal(identity);if err:=os.WriteFile("identity-worker.json",raw,0600);err!=nil{panic(err)}
  fmt.Println("fixture worker ready")
  select{}
 } else {os.Exit(2)}
}
'''



class WindowsFixtureHandles:
    """Hold exact kernel process handles, never reselect a PID for cleanup."""
    def __init__(self, executable: Path, identities: dict):
        self.handles = []
        self.kernel = None
        if os.name != "nt":
            return
        import ctypes
        from ctypes import wintypes
        self.ctypes = ctypes
        kernel = ctypes.WinDLL("kernel32", use_last_error=True)
        self.kernel = kernel
        kernel.OpenProcess.argtypes = [wintypes.DWORD, wintypes.BOOL, wintypes.DWORD]
        kernel.OpenProcess.restype = wintypes.HANDLE
        kernel.CloseHandle.argtypes = [wintypes.HANDLE]
        kernel.WaitForSingleObject.argtypes = [wintypes.HANDLE, wintypes.DWORD]
        kernel.WaitForSingleObject.restype = wintypes.DWORD
        kernel.TerminateProcess.argtypes = [wintypes.HANDLE, wintypes.UINT]
        kernel.QueryFullProcessImageNameW.argtypes = [wintypes.HANDLE, wintypes.DWORD, wintypes.LPWSTR, ctypes.POINTER(wintypes.DWORD)]
        try:
            for identity in identities.values():
                handle = kernel.OpenProcess(0x00100000 | 0x1000 | 0x0001, False, identity["pid"])
                if not handle:
                    raise OSError(ctypes.get_last_error(), "cannot hold fixture process")
                path, size = ctypes.create_unicode_buffer(32768), wintypes.DWORD(32768)
                if not kernel.QueryFullProcessImageNameW(handle, 0, path, ctypes.byref(size)) or not os.path.samefile(path.value, executable):
                    kernel.CloseHandle(handle)
                    raise RuntimeError("fixture PID does not identify our generated executable")
                self.handles.append(handle)
        except BaseException:
            self.close()
            raise

    def assert_exited(self):
        if self.kernel is not None:
            for handle in self.handles:
                if self.kernel.WaitForSingleObject(handle, 5000) != 0:
                    raise RuntimeError("scoped down did not terminate the owned Windows fixture process")

    def close(self):
        if self.kernel is not None:
            for handle in self.handles:
                # Called only for image-verified handles acquired while the
                # generated service was live. It cannot hit a reused PID.
                if self.kernel.WaitForSingleObject(handle, 1000) == 258:
                    self.kernel.TerminateProcess(handle, 125)
                    self.kernel.WaitForSingleObject(handle, 5000)
                self.kernel.CloseHandle(handle)
        self.handles.clear()


def wait_until(label, operation, timeout=18):
    deadline = time.monotonic() + timeout
    last = None
    while time.monotonic() < deadline:
        try:
            last = operation()
            if last:
                return last
        except (OSError, ValueError, subprocess.SubprocessError):
            pass
        time.sleep(0.15)
    raise RuntimeError(f"{label} did not reach its expected state (last result: {last!r})")


def verify(binary: Path, workspace: Path) -> dict:
    home = workspace / "home"
    project = home / "project with spaces"
    project.mkdir(parents=True)
    state = home / ".option-berth"
    state.mkdir(mode=0o700)
    pipe = r"\\.\pipe\oberth-native-" + uuid.uuid4().hex if os.name == "nt" else str(workspace / "d.sock")
    env = dict(os.environ)
    for key in list(env):
        if key.startswith("BERTH_") or key.startswith("OBERTH_"):
            env.pop(key)
    env.update(HOME=str(home), USERPROFILE=str(home), BERTH_HOME=str(state), BERTH_SOCKET=pipe,
               BERTH_NO_AUTOSTART="1", XDG_RUNTIME_DIR="", GIT_CONFIG_NOSYSTEM="1")
    # Building the fixture is not an engine command and may use the caller's
    # existing Go build cache. The fixture itself receives only the isolated HOME.
    source = workspace / "fixture.go"
    source.write_text(FIXTURE_SOURCE)
    fixture = project / ("fixture.exe" if os.name == "nt" else "fixture")
    subprocess.run(["go", "build", "-o", str(fixture), str(source)], check=True, timeout=60)
    cmd = "./fixture.exe" if os.name == "nt" else "./fixture"
    (project / "oberth.yaml").write_text(
        "name: native-lifecycle\nservices:\n"
        f"  - name: api\n    cmd: {cmd} api\n    port: auto\n"
        f"  - name: worker\n    cmd: {cmd} worker\n")
    # A separate unrelated listener must survive every scoped lifecycle action.
    sentinel = socket.socket()
    sentinel.bind(("127.0.0.1", 0)); sentinel.listen()
    sentinel_port = sentinel.getsockname()[1]
    daemons = []
    owned = None
    log = (workspace / "daemon.log").open("wb")

    def cli(*arguments, cwd=project, accept_failure=False):
        result = subprocess.run([str(binary), *arguments], cwd=cwd, env=env,
                                capture_output=True, text=True, timeout=25)
        if result.returncode and not accept_failure:
            raise RuntimeError(f"CLI {' '.join(arguments[:2])} failed ({result.returncode}): {result.stderr[:500]}")
        return json.loads(result.stdout) if result.stdout.strip() else {}

    def status():
        return cli("status", "--json", "--no-mark")

    def running():
        doc = status()
        services = {item["name"]: item for item in doc.get("worktree", {}).get("services", [])}
        return services if services.get("api", {}).get("running") and services.get("worker", {}).get("running") else False

    def start_daemon():
        process = subprocess.Popen([str(binary), "serve"], cwd=home, env=env, stdout=log, stderr=log)
        daemons.append(process)
        wait_until("daemon readiness", lambda: cli("daemon", "status", "--json", cwd=home, accept_failure=True).get("running") is True)
        return process

    try:
        first = start_daemon()
        up = cli("up", "--json")
        if up.get("errors"):
            raise RuntimeError(f"up reported errors: {up['errors']}")
        services = wait_until("native API and no-port worker", running)
        identity = wait_until("fixture startup identity files", lambda: {
            mode: json.loads((project / f"identity-{mode}.json").read_text()) for mode in ("api", "worker")})
        if not all(value.get("run_id") for value in identity.values()):
            raise RuntimeError("native services did not receive run identities")
        owned = WindowsFixtureHandles(fixture, identity)
        port = identity["api"]["port"]
        services = wait_until("observed API port readiness", lambda: (value if (value := running()) and value["api"].get("port_actual") == port else False))
        if services["api"].get("port_actual") != port or services["worker"].get("port_actual") is not None:
            raise RuntimeError("API port or no-port worker attribution changed")
        cli("up", "--json")
        again = {mode: json.loads((project / f"identity-{mode}.json").read_text()) for mode in identity}
        if again != identity:
            raise RuntimeError("idempotent up restarted a native service")
        cli("daemon", "stop", "--json", cwd=home)
        first.wait(timeout=10)
        direct = wait_until("direct observation after daemon stop", running)
        if not direct:
            raise RuntimeError("no-daemon status lost native workers")
        start_daemon()
        wait_until("daemon restart rehydrates services", running)
        rehydrated = {mode: json.loads((project / f"identity-{mode}.json").read_text()) for mode in identity}
        if rehydrated != identity:
            raise RuntimeError("collector restart replaced a running service")
        down = cli("down", "--force", "--json")
        if down.get("errors") or down.get("ok") is False:
            raise RuntimeError("scoped native down reported failure")
        def stopped():
            services = status().get("worktree", {}).get("services", [])
            return {value.get("name") for value in services} == {"api", "worker"} and all(value.get("running") is False for value in services)
        wait_until("service stop observation", stopped)
        def port_closed():
            probe = socket.socket(); probe.settimeout(0.2)
            try:
                return probe.connect_ex(("127.0.0.1", port)) != 0
            finally:
                probe.close()
        wait_until("real API socket closure", port_closed)
        owned.assert_exited()
        with socket.create_connection(("127.0.0.1", sentinel_port), timeout=1):
            pass
        print("PASS: native API/no-port worker, idempotent up, direct status, daemon rehydration, scoped down and unrelated listener survival")
        return {"platform": os.name, "api_port": port, "checks": 7}
    finally:
        # Only fixtures read these private per-project control files.
        for mode in ("api", "worker"):
            (project / ("stop-" + mode)).touch()
        try:
            cli("down", "--force", "--json", accept_failure=True)
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
            pass
        try:
            cli("daemon", "stop", "--json", cwd=home, accept_failure=True)
        except (OSError, ValueError, RuntimeError, subprocess.SubprocessError):
            pass
        for process in daemons:
            try:
                process.wait(timeout=5)
            except subprocess.TimeoutExpired:
                process.kill(); process.wait(timeout=5)
        if owned is not None:
            owned.close()
        sentinel.close(); log.close()
        time.sleep(0.3)


if __name__ == "__main__":
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--binary", type=Path, required=True)
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    if not binary.is_file():
        parser.error("binary must be a built executable")
    with tempfile.TemporaryDirectory(prefix="ol-", dir=tempfile.gettempdir()) as directory:
        verify(binary, Path(directory))
