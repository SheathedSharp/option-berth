//go:build integration

// Integration tests for the daemon, run with `go test -tags integration
// ./internal/daemon/...`. They compile the real `option-berth` binary, run it
// as `oberth serve` against a temporary HOME on a temporary socket, and drive
// it with the Go client — the path the CLI, the MCP server and the desktop app
// all take.
//
// What lives here: spec integration test 1 (hello, version, capabilities),
// spec integration test 5 (a second instance is refused, and a stale socket is
// cleaned up after SIGKILL), plus the step's acceptance demo (a subscriber sees
// a listener appear and then disappear).
package daemon_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/testenv"
)

// buildBinary compiles the option-berth CLI once per test run.
//
// The binary has to live under testenv.Root(): the leak gate only claims a
// `serve` whose executable sits inside this run's own temp root, so a daemon
// this harness starts must be built there to be recognised — and a binary that
// another run built has to be somewhere else, so it is left alone.
var buildBinary = sync.OnceValues(func() (string, error) {
	buildDir, err := os.MkdirTemp(testenv.Root(), "option-berth-itest-bin")
	if err != nil {
		return "", err
	}
	built := filepath.Join(buildDir, "oberth")
	if runtime.GOOS == "windows" {
		built += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", built, "./cmd/oberth")
	cmd.Dir = repoRoot()
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("building oberth: %v: %s", err, out)
	}
	return built, nil
})

// repoRoot walks up from this file until it finds the module root.
func repoRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for depth := 0; depth < 8; depth++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	return "."
}

// env is one isolated daemon: a HOME of its own, a socket of its own, a log of
// its own.
type env struct {
	t    *testing.T
	bin  string
	home string
	// socket is this env's transport address: a unix socket, or a named pipe
	// on Windows.
	socket string
	// extra is appended after everything else in every command's environment,
	// so it wins: a test puts a fake tool first on PATH with it.
	extra []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	built, err := buildBinary()
	if err != nil {
		t.Fatal(err)
	}

	home := t.TempDir()
	// macOS caps a unix socket path at about 104 bytes and t.TempDir() is long,
	// so the socket gets a short directory of its own.
	runtimeDir, err := os.MkdirTemp("", "oberth")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtimeDir) })

	addr := filepath.Join(runtimeDir, "d.sock")
	if runtime.GOOS == "windows" {
		// Named pipes share one flat namespace, so the pipe is numbered as well
		// as pid-stamped: two envs in one test binary must not land on the same
		// address, or a daemon still shutting down from an earlier test owns the
		// name this one is about to bind.
		addr = fmt.Sprintf(`\\.\pipe\option-berth-itest-%d-%d`, os.Getpid(), pipeSeq.Add(1))
	}
	return &env{t: t, bin: built, home: home, socket: addr}
}

// waitDelay bounds how long Wait may block on a pipe after the process it was
// waiting for has exited. A stray grandchild holding the inherited stdout keeps
// that pipe open forever, so without this bound a test that has already failed
// hangs until `go test -timeout` kills the binary.
const waitDelay = 5 * time.Second

// pipeSeq numbers the named pipes this test binary creates.
var pipeSeq atomic.Int64

// lockPath is where this env's daemon takes its single-instance lock — beside
// the socket, or in the config directory when the transport is a named pipe.
func (e *env) lockPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(e.home, ".option-berth", "daemon.lock")
	}
	return filepath.Join(filepath.Dir(e.socket), "daemon.lock")
}

// stopDaemon stops a daemon this env started detached, and waits until it is
// really gone rather than merely unreachable.
//
// Windows will not delete a file that another process still holds open, so a
// test whose temp HOME holds the daemon's lock and log must watch that process
// exit before t.TempDir's own cleanup runs; otherwise the test fails during
// teardown with "the process cannot access the file because it is being used
// by another process". The pid may be 0 when the caller does not know it, in
// which case the socket and the lock are the only evidence available.
func (e *env) stopDaemon(pid int) {
	_ = e.command("daemon", "stop").Run()

	// Give it twenty seconds to go, then look at the lock regardless.
	limit := time.Now().Add(20 * time.Second)
	for time.Now().Before(limit) {
		if gone := !daemon.SocketAlive(e.socket) && (pid <= 0 || !runs.PIDAlive(pid)); gone {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// A daemon lets go of its lock last of all.
	if err := daemon.WaitForLockRelease(e.lockPath(), 10*time.Second); err != nil {
		e.t.Logf("the daemon still holds %s: %v", e.lockPath(), err)
	}
}

// command builds a `option-berth` invocation pinned to this env.
func (e *env) command(args ...string) *exec.Cmd {
	run := exec.Command(e.bin, args...)
	run.WaitDelay = waitDelay
	run.Env = append(os.Environ(),
		"HOME="+e.home,
		"USERPROFILE="+e.home,
		"BERTH_SOCKET="+e.socket,
		// XDG_RUNTIME_DIR outranks HOME for the log path when it is set.
		"XDG_RUNTIME_DIR=",
		// The test binary is isolated with one BERTH_DB for the whole run
		// (internal/testenv); clearing it puts this env's database under this
		// env's HOME, which is what a test that restarts a daemon and expects
		// its own rows back is asking for.
		"BERTH_DB=",
		// These children are the real `option-berth`, and the tests here are
		// the ones that drive its autostart on purpose.
		testenv.ChildEnv(),
	)
	run.Env = append(run.Env, e.extra...)
	return run
}

// serve starts `oberth serve` in the background and waits for its socket.
func (e *env) serve() *exec.Cmd {
	e.t.Helper()
	run := e.command("serve")
	var output strings.Builder
	run.Stdout, run.Stderr = &output, &output
	ownProcessGroup(run)
	if err := run.Start(); err != nil {
		e.t.Fatalf("starting oberth serve: %v", err)
	}
	e.t.Cleanup(func() {
		stopCommand(run)
		if e.t.Failed() && output.Len() > 0 {
			e.t.Logf("daemon output:\n%s", output.String())
		}
	})

	waitCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.WaitForSocket(waitCtx, e.socket, 10*time.Second); err != nil {
		e.t.Fatalf("daemon never came up: %v\n%s", err, output.String())
	}
	return run
}

// connect dials the daemon as a test client.
func (e *env) connect(ctx context.Context) *client.Client {
	e.t.Helper()
	conn, err := client.Dial(ctx, client.ClientInfo{
		Name: "cli", Version: "itest", Socket: e.socket,
	})
	if err != nil {
		e.t.Fatalf("connecting to the daemon: %v", err)
	}
	e.t.Cleanup(func() { conn.Close() })
	return conn
}

// TestHelloReportsVersionAndCapabilities is spec integration test 1.
func TestHelloReportsVersionAndCapabilities(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	hello := cl.Hello()
	if hello.ProtocolVersion != rpc.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", hello.ProtocolVersion, rpc.ProtocolVersion)
	}
	if hello.DaemonVersion == "" {
		t.Error("hello reported an empty daemon_version")
	}
	if hello.PID <= 0 {
		t.Errorf("pid = %d, want a live process id", hello.PID)
	}
	if hello.StartedAt == "" {
		t.Error("started_at is empty")
	}
	if hello.Socket != sandbox.socket {
		t.Errorf("socket = %q, want %q", hello.Socket, sandbox.socket)
	}
	if hello.BinaryPath == "" {
		t.Error("binary_path is empty; the contract requires it for autostart")
	}
	expected := map[string]bool{"state": true, rpc.CapabilityStateScope: true, "ports.read": true}
	for _, capability := range hello.Capabilities {
		delete(expected, capability)
	}
	if len(expected) > 0 {
		t.Errorf("capabilities = %v, missing %v", hello.Capabilities, expected)
	}

	var status rpc.DaemonStatusResult
	if err := cl.Call(ctx, "daemon.status", rpc.Empty{}, &status); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if status.PID != hello.PID {
		t.Errorf("daemon.status reports pid %d, hello reports %d", status.PID, hello.PID)
	}
}

// TestSubscriberSeesAPortAppearAndDisappear is the step's acceptance demo, run
// in-process: a listener on a free port turns up in a state.delta, and its
// close turns up in a later one.
func TestSubscriberSeesAPortAppearAndDisappear(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cl := sandbox.connect(ctx)

	sub, err := cl.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}
	t.Logf("subscribed at seq %d with %d ports", sub.Snapshot.Seq, len(sub.Snapshot.Ports))

	// A real listener on a real free port, so the daemon's own lsof/ss scan is
	// the thing that has to find it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("opening a test listener: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	t.Logf("test listener is on port %d", port)

	if !waitForAdded(t, sub, port, 30*time.Second) {
		listener.Close()
		t.Fatalf("port %d never turned up in a state.delta within 30s", port)
	}

	listener.Close()
	if !waitForRemoved(t, sub, port, 30*time.Second) {
		t.Fatalf("port %d never went away in a state.delta within 30s", port)
	}

	var status rpc.DaemonStatusResult
	if err := cl.Call(ctx, "daemon.status", rpc.Empty{}, &status); err != nil {
		t.Fatalf("daemon.status: %v", err)
	}
	if got := status.Subscribers; got != 1 {
		t.Errorf("daemon.status reports %d subscribers, want 1", got)
	}
}

func waitForAdded(t *testing.T, sub *client.Subscription, port int, timeout time.Duration) bool {
	t.Helper()
	expired := time.After(timeout)
	for {
		select {
		case delta, open := <-sub.Deltas:
			if !open {
				return false
			}
			for _, row := range delta.Ports.Added {
				if row.Port == port {
					t.Logf("delta seq %d added %d (%s)", delta.Seq, row.Port, row.DisplayName)
					return true
				}
			}
		case <-expired:
			return false
		}
	}
}

func waitForRemoved(t *testing.T, sub *client.Subscription, port int, timeout time.Duration) bool {
	t.Helper()
	prefix := fmt.Sprintf("%d:", port)
	expired := time.After(timeout)
	for {
		select {
		case delta, open := <-sub.Deltas:
			if !open {
				return false
			}
			for _, key := range delta.Ports.Removed {
				if strings.HasPrefix(key, prefix) {
					t.Logf("delta seq %d removed %s", delta.Seq, key)
					return true
				}
			}
		case <-expired:
			return false
		}
	}
}

// TestSecondServeRefusesAndStaleSocketIsCleaned is spec integration test 5.
func TestSecondServeRefusesAndStaleSocketIsCleaned(t *testing.T) {
	sandbox := newEnv(t)
	first := sandbox.serve()

	// A second `oberth serve` must refuse rather than steal the socket.
	second := sandbox.command("serve")
	out, err := second.CombinedOutput()
	if err == nil {
		t.Fatalf("a second `oberth serve` succeeded; output:\n%s", out)
	}
	if !strings.Contains(string(out), "already running") {
		t.Errorf("the second serve said %q; it should have said it is already running", out)
	}
	if pid := first.Process.Pid; !strings.Contains(string(out), fmt.Sprint(pid)) {
		t.Errorf("the second serve did not name the holder pid %d: %q", pid, out)
	}

	// The first daemon carries on serving.
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sandbox.connect(ctx)

	// SIGKILL leaves the socket file behind with no chance to tidy up.
	if err := first.Process.Kill(); err != nil {
		t.Fatalf("killing the first daemon: %v", err)
	}
	_ = first.Wait()

	if runtime.GOOS != "windows" {
		if _, err := os.Stat(sandbox.socket); err != nil {
			t.Fatalf("expected a stale socket file at %s after SIGKILL: %v", sandbox.socket, err)
		}
		if _, err := daemon.Dial(sandbox.socket); err == nil {
			t.Fatal("the stale socket still accepts connections")
		}
	}

	// A fresh daemon must remove the stale socket and take the lock.
	sandbox.serve()
	replacement := sandbox.connect(ctx)
	if replacement.Hello().PID == first.Process.Pid {
		t.Error("the new daemon reports the dead daemon's pid")
	}
}

// TestDaemonStatusAndStopThroughTheCLI exercises the `oberth daemon` surface
// the demo uses.
func TestDaemonStatusAndStopThroughTheCLI(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()

	out, err := sandbox.command("daemon", "status").CombinedOutput()
	if err != nil {
		t.Fatalf("oberth daemon status: %v\n%s", err, out)
	}
	for _, want := range []string{"running       yes", "pid", "subscribers", "socket"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("`oberth daemon status` output is missing %q:\n%s", want, out)
		}
	}

	pathOut, err := sandbox.command("daemon", "path").CombinedOutput()
	if err != nil {
		t.Fatalf("oberth daemon path: %v\n%s", err, pathOut)
	}
	if got, want := strings.TrimSpace(string(pathOut)), sandbox.socket; got != want {
		t.Errorf("`oberth daemon path` = %q, want %q", got, want)
	}

	stopOut, err := sandbox.command("daemon", "stop").CombinedOutput()
	if err != nil {
		t.Fatalf("oberth daemon stop: %v\n%s", err, stopOut)
	}
	if daemon.SocketAlive(sandbox.socket) {
		t.Error("the daemon still accepts connections after `oberth daemon stop`")
	}

	// With no daemon left, `status` exits non-zero and says so.
	downOut, err := sandbox.command("daemon", "status").CombinedOutput()
	if err == nil {
		t.Errorf("`oberth daemon status` with no daemon exited 0:\n%s", downOut)
	}
	if !strings.Contains(string(downOut), "not running") {
		t.Errorf("status output = %q, want a \"not running\" message", downOut)
	}
}

// TestAutostart is the autostart path: no daemon, one Connect, a daemon within
// three seconds.
func TestAutostart(t *testing.T) {
	sandbox := newEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	if daemon.SocketAlive(sandbox.socket) {
		t.Fatal("something is already listening on the test socket")
	}

	// Connect defaults to the running executable, so point it at the built
	// binary and hand the child the same isolated HOME. Autostart is disabled
	// for the whole test binary, and this is the test that drives it on purpose.
	testenv.AllowAutostart(t)
	t.Setenv("HOME", sandbox.home)
	t.Setenv("USERPROFILE", sandbox.home)
	t.Setenv("BERTH_SOCKET", sandbox.socket)
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("BERTH_DB", "")

	cl, err := client.Connect(ctx, client.ClientInfo{
		Name: "cli", Version: "itest", Socket: sandbox.socket, BinaryPath: sandbox.bin,
	})
	if err != nil {
		t.Fatalf("autostart: %v", err)
	}
	defer cl.Close()

	if cl.Hello().PID <= 0 {
		t.Errorf("autostarted daemon reported pid %d", cl.Hello().PID)
	}
	// The daemon autostart created is detached: nothing else in this test owns
	// it. Cleanups run after the test's own defers, so this one cannot reuse cl
	// (already closed) — it stops the daemon through the CLI and waits for the
	// process, so the temp HOME holding the lock and log can be removed.
	daemonPID := cl.Hello().PID
	t.Cleanup(func() { sandbox.stopDaemon(daemonPID) })

	var snap state.Snapshot
	if err := cl.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		t.Fatalf("state.snapshot against the autostarted daemon: %v", err)
	}
	if snap.Ports == nil {
		t.Error("snapshot ports marshalled as null")
	}
}

// TestProtocolMismatchIsReported exercises the client-side version guard, with
// no second daemon build needed.
func TestProtocolMismatchIsReported(t *testing.T) {
	failure := client.CheckProtocol("9.0.0")
	var mismatch *client.ProtocolMismatchError
	if !errors.As(failure, &mismatch) {
		t.Fatalf("CheckProtocol(9.0.0) = %v, want a ProtocolMismatchError", failure)
	}
}

// TestDaemonRestartIsRepeatable is the smoke-test regression: `oberth daemon
// restart` waited only for the socket to stop accepting, but a daemon releases
// its single-instance lock after it closes the socket. The replacement raced
// that release, lost the lock and exited "already running" — two attempts out
// of three left nothing running at all. Five restarts in a row must each leave
// a daemon up.
func TestDaemonRestartIsRepeatable(t *testing.T) {
	sandbox := newEnv(t)
	sandbox.serve()
	// After the first restart the daemon is detached, so the env's own cleanup
	// (which kills the foreground child) no longer owns it.
	t.Cleanup(func() { sandbox.stopDaemon(0) })

	previousPID := ""
	for attempt := 1; attempt <= 5; attempt++ {
		out, err := sandbox.command("daemon", "restart").CombinedOutput()
		if err != nil {
			t.Fatalf("restart %d of 5 failed: %v\n%s", attempt, err, out)
		}

		statusOut, err := sandbox.command("daemon", "status").CombinedOutput()
		if err != nil {
			t.Fatalf("no daemon running after restart %d of 5: %v\nrestart said:\n%s\nstatus said:\n%s",
				attempt, err, out, statusOut)
		}
		if !strings.Contains(string(statusOut), "running       yes") {
			t.Fatalf("after restart %d of 5 the daemon does not report itself running:\n%s", attempt, statusOut)
		}

		pid := statusField(string(statusOut), "pid")
		if pid == "" {
			t.Fatalf("`oberth daemon status` reported no pid after restart %d:\n%s", attempt, statusOut)
		}
		if pid == previousPID {
			t.Errorf("restart %d reported the same pid %s as the previous one; the daemon was not replaced",
				attempt, pid)
		}
		previousPID = pid

		// The running daemon must own the single-instance lock too. When the
		// replacement raced the old daemon's teardown, the lock file was left
		// holding a dead pid, or removed from under the live daemon.
		if runtime.GOOS != "windows" {
			lockPath := sandbox.lockPath()
			if holder := daemon.LockHolderPID(lockPath); fmt.Sprint(holder) != pid {
				t.Errorf("after restart %d the lock at %s records pid %d, but the daemon reports pid %s",
					attempt, lockPath, holder, pid)
			}
		}
	}
}

// statusField pulls one value out of `oberth daemon status`'s aligned output.
func statusField(out, field string) string {
	for _, line := range strings.Split(out, "\n") {
		columns := strings.Fields(line)
		if len(columns) >= 2 && columns[0] == field {
			return columns[1]
		}
	}
	return ""
}
