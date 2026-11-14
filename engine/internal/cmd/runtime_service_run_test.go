package cmd

import (
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// The runtime service runs in option-berth's state directory, not in whatever checkout
// invoked it: a runtime service started from a worktree must not keep that checkout as
// its working directory after the checkout is deleted.
func TestRuntimeServiceWorkDirIsTheStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	got, err := runtimeServiceWorkDir()
	if err != nil {
		t.Fatalf("runtimeServiceWorkDir: %v", err)
	}
	if got != home {
		t.Errorf("runtimeServiceWorkDir = %q, want the state directory %q", got, home)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("state directory %q is not usable: %v", got, err)
	}
}

// TestAlreadyRunningErrorFollowsTheSocket pins the blocked-start decision:
// `runtime service run --detach` refuses only while another process really answers on this
// socket, so a stale socket file does not block a legitimate restart.
func TestAlreadyRunningErrorFollowsTheSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the runtime service socket harness is unix-only")
	}
	socket := shortSocketPath(t, "d.sock")
	if err := alreadyRunningError(socket); err != nil {
		t.Fatalf("a socket nobody listens on must not block the start: %v", err)
	}

	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = alreadyRunningError(socket)
	if err == nil || !strings.Contains(err.Error(), "runtime service already running") {
		t.Fatalf("err = %v, want the already-running refusal", err)
	}
}

// TestOpenRuntimeServiceLogCreatesTheLogOwnerOnly: the detached child writes its only
// pre-logger diagnostics here, so the file has to exist before the fork and
// must not be readable by other users.
func TestOpenRuntimeServiceLogCreatesTheLogOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	f, err := openRuntimeServiceLog()
	if err != nil {
		t.Fatalf("openRuntimeServiceLog: %v", err)
	}
	path := f.Name()
	defer f.Close()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("the log file was not created: %v", err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Errorf("log mode = %o, want no group or other access", perm)
	}
}

// TestEnterRuntimeServiceWorkDirRefusesAnUnusableStateDirectory: the runtime service must not
// start in a directory it cannot move into — that would leave it in whatever
// checkout happened to launch it.
func TestEnterRuntimeServiceWorkDirRefusesAnUnusableStateDirectory(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", notADir)

	if err := enterRuntimeServiceWorkDir(); err == nil {
		t.Fatal("enterRuntimeServiceWorkDir accepted a state directory that is a file")
	}
}

// TestOpenRuntimeServiceLoggerWritesTheRuntimeServiceLog: the runtime service's own log is opened at the
// configured level, and the file it returns is what closes it.
func TestOpenRuntimeServiceLoggerWritesTheRuntimeServiceLog(t *testing.T) {
	isolateRuntimeServicePaths(t)
	logger, closer, err := openRuntimeServiceLogger()
	if err != nil {
		t.Fatalf("openRuntimeServiceLogger: %v", err)
	}
	logger.Info("runtime service log test")
	if err := closer.Close(); err != nil {
		t.Fatalf("closing the log: %v", err)
	}
	body, err := os.ReadFile(runtimesvc.LogPath())
	if err != nil {
		t.Fatalf("the runtime service log was not written: %v", err)
	}
	if !strings.Contains(string(body), "runtime service log test") {
		t.Fatalf("log = %q, want the line this test logged", body)
	}
}

// TestOpenRuntimeServiceLoggerFailsWhenTheLogCannotBeCreated: an unusable log path is a
// start failure, not a runtime service that silently logs nowhere.
func TestOpenRuntimeServiceLoggerFailsWhenTheLogCannotBeCreated(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", notADir)

	if _, closer, err := openRuntimeServiceLogger(); err == nil {
		closer.Close()
		t.Fatal("openRuntimeServiceLogger accepted a log path it cannot create")
	}
}

// TestDetachedChildIsThisBinaryRunningServe pins the detached child's identity:
// the runtime service is this same binary re-executed as `runtime service run`, it runs in the state
// directory, it inherits nothing but the environment, and its only output is the
// runtime service log.
func TestDetachedChildIsThisBinaryRunningServe(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	child, logFile, err := detachedChild()
	if err != nil {
		t.Fatalf("detachedChild: %v", err)
	}
	defer logFile.Close()

	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if child.Path != exe {
		t.Errorf("child path = %q, want this binary %q", child.Path, exe)
	}
	if len(child.Args) != 4 || strings.Join(child.Args[1:], " ") != "runtime service run" {
		t.Errorf("child args = %v, want `runtime service run` only", child.Args)
	}
	if child.Dir != paths.Dir() {
		t.Errorf("child dir = %q, want the state directory %q", child.Dir, paths.Dir())
	}
	if child.Stdin != nil {
		t.Error("the child inherited stdin; it is a runtime service, not a terminal job")
	}
	if child.Stdout != logFile || child.Stderr != logFile {
		t.Error("the child's output does not go to the runtime service log")
	}
	marked := false
	for _, kv := range child.Env {
		if kv == detachedEnv+"=1" {
			marked = true
		}
	}
	if !marked {
		t.Errorf("the child is not marked %s=1, so its stderr would be mirrored twice", detachedEnv)
	}
	if socket == "" {
		t.Fatal("the harness produced no socket")
	}
}

// TestDetachRuntimeServiceRefusesWhenOneIsAlreadyListening: `runtime service run --detach` must not
// start a second runtime service on a socket another one answers on.
func TestDetachRuntimeServiceRefusesWhenOneIsAlreadyListening(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = detachRuntimeService(context.Background(), socket)
	if err == nil || !strings.Contains(err.Error(), "runtime service already running") {
		t.Fatalf("err = %v, want the already-running refusal", err)
	}
}

// TestDetachRuntimeServiceReportsAChildThatCouldNotStart: a fork that fails is reported
// with its own cause.
func TestDetachRuntimeServiceReportsAChildThatCouldNotStart(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	stubRuntimeServiceChild(t, func(*exec.Cmd) (int, error) {
		return 0, errors.New("fork failed")
	})

	err := detachRuntimeService(context.Background(), socket)
	if err == nil || !strings.Contains(err.Error(), "fork failed") {
		t.Fatalf("err = %v, want the launch failure", err)
	}
}

// TestDetachRuntimeServiceReportsARuntimeServiceThatNeverListened: the command only returns once
// the socket answers, so a child that dies on the way reports the wait failure
// and points at the log it wrote.
func TestDetachRuntimeServiceReportsARuntimeServiceThatNeverListened(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	stubRuntimeServiceChild(t, func(*exec.Cmd) (int, error) { return 4242, nil })

	err := detachRuntimeService(context.Background(), socket)
	if err == nil {
		t.Fatal("detachRuntimeService returned nil for a socket nobody answers on")
	}
	if !strings.Contains(err.Error(), runtimesvc.LogPath()) {
		t.Fatalf("err = %v, want it to name the log %s", err, runtimesvc.LogPath())
	}
}

// TestDetachRuntimeServiceReturnsOnceTheRuntimeServiceAnswers: the wait is what makes
// `runtime service run --detach` usable from a script — it prints the pid and socket the
// caller can use, and returns nothing until that runtime service is reachable.
func TestDetachRuntimeServiceReturnsOnceTheRuntimeServiceAnswers(t *testing.T) {
	socket := isolateRuntimeServicePaths(t)
	stubRuntimeServiceChild(t, func(*exec.Cmd) (int, error) {
		// Stands in for the child that becomes the runtime_service: the contract is that
		// a runtime service answers on this socket by the time the command returns.
		runRuntimeServiceAt(t, socket)
		return 4242, nil
	})

	var err error
	out := captureStdout(t, func() { err = detachRuntimeService(context.Background(), socket) })
	if err != nil {
		t.Fatalf("detachRuntimeService: %v", err)
	}
	want := "oberth runtime service started (pid 4242), listening on " + socket + "\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

// stubRuntimeServiceChild replaces the launch for one test. What is not replaced is the
// child command itself, the log file, the working directory and the wait.
func stubRuntimeServiceChild(t *testing.T, start func(*exec.Cmd) (int, error)) {
	t.Helper()
	prev := startRuntimeServiceChild
	startRuntimeServiceChild = start
	t.Cleanup(func() { startRuntimeServiceChild = prev })
}

// TestRunForegroundRuntimeServiceServesUntilItsContextEnds runs the process that becomes
// the runtime_service: it moves into the state directory, listens on the socket the CLI
// looks at, answers status from this very process, and returns when its context
// ends.
func TestRunForegroundRuntimeServiceServesUntilItsContextEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateRuntimeServicePaths(t)

	// This function changes two process-wide things on purpose; put them back.
	was, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	pathBefore := os.Getenv("PATH")
	t.Cleanup(func() {
		_ = os.Chdir(was)
		_ = os.Setenv("PATH", pathBefore)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runForegroundRuntimeService(ctx) }()

	if err := client.WaitForSocket(context.Background(), socket, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("the foreground runtime service never listened on %s: %v (%v)", socket, err, <-done)
	}
	c, err := client.Dial(context.Background(), client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	if err != nil {
		cancel()
		t.Fatalf("connecting: %v", err)
	}
	var status rpc.RuntimeServiceStatusResult
	callErr := c.Call(context.Background(), "runtime.service.status", rpc.Empty{}, &status)
	c.Close()
	if callErr != nil {
		cancel()
		t.Fatalf("runtime.service.status: %v", callErr)
	}
	if status.PID != os.Getpid() {
		t.Errorf("runtime service pid = %d, want this process %d", status.PID, os.Getpid())
	}

	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("runForegroundRuntimeService returned %v, want a clean stop", serveErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runForegroundRuntimeService did not return after its context ended")
	}
}
