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

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/paths"
)

// The daemon runs in option-berth's state directory, not in whatever checkout
// invoked it: a daemon started from a worktree must not keep that checkout as
// its working directory after the checkout is deleted.
func TestDaemonWorkDirIsTheStateDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	got, err := daemonWorkDir()
	if err != nil {
		t.Fatalf("daemonWorkDir: %v", err)
	}
	if got != home {
		t.Errorf("daemonWorkDir = %q, want the state directory %q", got, home)
	}
	info, err := os.Stat(got)
	if err != nil || !info.IsDir() {
		t.Fatalf("state directory %q is not usable: %v", got, err)
	}
}

// TestAlreadyRunningErrorFollowsTheSocket pins the blocked-start decision:
// `serve --detach` refuses only while another process really answers on this
// socket, so a stale socket file does not block a legitimate restart.
func TestAlreadyRunningErrorFollowsTheSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the daemon socket harness is unix-only")
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
	if err == nil || !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("err = %v, want the already-running refusal", err)
	}
}

// TestOpenDaemonLogCreatesTheLogOwnerOnly: the detached child writes its only
// pre-logger diagnostics here, so the file has to exist before the fork and
// must not be readable by other users.
func TestOpenDaemonLogCreatesTheLogOwnerOnly(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)

	f, err := openDaemonLog()
	if err != nil {
		t.Fatalf("openDaemonLog: %v", err)
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

// TestEnterDaemonWorkDirRefusesAnUnusableStateDirectory: the daemon must not
// start in a directory it cannot move into — that would leave it in whatever
// checkout happened to launch it.
func TestEnterDaemonWorkDirRefusesAnUnusableStateDirectory(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", notADir)

	if err := enterDaemonWorkDir(); err == nil {
		t.Fatal("enterDaemonWorkDir accepted a state directory that is a file")
	}
}

// TestOpenDaemonLoggerWritesTheDaemonLog: the daemon's own log is opened at the
// configured level, and the file it returns is what closes it.
func TestOpenDaemonLoggerWritesTheDaemonLog(t *testing.T) {
	isolateDaemonPaths(t)
	logger, closer, err := openDaemonLogger()
	if err != nil {
		t.Fatalf("openDaemonLogger: %v", err)
	}
	logger.Info("daemon log test")
	if err := closer.Close(); err != nil {
		t.Fatalf("closing the log: %v", err)
	}
	body, err := os.ReadFile(daemon.LogPath())
	if err != nil {
		t.Fatalf("the daemon log was not written: %v", err)
	}
	if !strings.Contains(string(body), "daemon log test") {
		t.Fatalf("log = %q, want the line this test logged", body)
	}
}

// TestOpenDaemonLoggerFailsWhenTheLogCannotBeCreated: an unusable log path is a
// start failure, not a daemon that silently logs nowhere.
func TestOpenDaemonLoggerFailsWhenTheLogCannotBeCreated(t *testing.T) {
	notADir := filepath.Join(t.TempDir(), "state")
	if err := os.WriteFile(notADir, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", notADir)

	if _, closer, err := openDaemonLogger(); err == nil {
		closer.Close()
		t.Fatal("openDaemonLogger accepted a log path it cannot create")
	}
}

// TestDetachedChildIsThisBinaryRunningServe pins the detached child's identity:
// the daemon is this same binary re-executed as `serve`, it runs in the state
// directory, it inherits nothing but the environment, and its only output is the
// daemon log.
func TestDetachedChildIsThisBinaryRunningServe(t *testing.T) {
	socket := isolateDaemonPaths(t)
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
	if len(child.Args) != 2 || child.Args[1] != "serve" {
		t.Errorf("child args = %v, want `serve` only", child.Args)
	}
	if child.Dir != paths.Dir() {
		t.Errorf("child dir = %q, want the state directory %q", child.Dir, paths.Dir())
	}
	if child.Stdin != nil {
		t.Error("the child inherited stdin; it is a daemon, not a terminal job")
	}
	if child.Stdout != logFile || child.Stderr != logFile {
		t.Error("the child's output does not go to the daemon log")
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

// TestDetachDaemonRefusesWhenOneIsAlreadyListening: `serve --detach` must not
// start a second daemon on a socket another one answers on.
func TestDetachDaemonRefusesWhenOneIsAlreadyListening(t *testing.T) {
	socket := isolateDaemonPaths(t)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	err = detachDaemon(context.Background(), socket)
	if err == nil || !strings.Contains(err.Error(), "daemon already running") {
		t.Fatalf("err = %v, want the already-running refusal", err)
	}
}

// TestDetachDaemonReportsAChildThatCouldNotStart: a fork that fails is reported
// with its own cause.
func TestDetachDaemonReportsAChildThatCouldNotStart(t *testing.T) {
	socket := isolateDaemonPaths(t)
	stubDaemonChild(t, func(*exec.Cmd) (int, error) {
		return 0, errors.New("fork failed")
	})

	err := detachDaemon(context.Background(), socket)
	if err == nil || !strings.Contains(err.Error(), "fork failed") {
		t.Fatalf("err = %v, want the launch failure", err)
	}
}

// TestDetachDaemonReportsADaemonThatNeverListened: the command only returns once
// the socket answers, so a child that dies on the way reports the wait failure
// and points at the log it wrote.
func TestDetachDaemonReportsADaemonThatNeverListened(t *testing.T) {
	socket := isolateDaemonPaths(t)
	stubDaemonChild(t, func(*exec.Cmd) (int, error) { return 4242, nil })

	err := detachDaemon(context.Background(), socket)
	if err == nil {
		t.Fatal("detachDaemon returned nil for a socket nobody answers on")
	}
	if !strings.Contains(err.Error(), daemon.LogPath()) {
		t.Fatalf("err = %v, want it to name the log %s", err, daemon.LogPath())
	}
}

// TestDetachDaemonReturnsOnceTheDaemonAnswers: the wait is what makes
// `serve --detach` usable from a script — it prints the pid and socket the
// caller can use, and returns nothing until that daemon is reachable.
func TestDetachDaemonReturnsOnceTheDaemonAnswers(t *testing.T) {
	socket := isolateDaemonPaths(t)
	stubDaemonChild(t, func(*exec.Cmd) (int, error) {
		// Stands in for the child that becomes the daemon: the contract is that
		// a daemon answers on this socket by the time the command returns.
		runDaemonAt(t, socket)
		return 4242, nil
	})

	var err error
	out := captureStdout(t, func() { err = detachDaemon(context.Background(), socket) })
	if err != nil {
		t.Fatalf("detachDaemon: %v", err)
	}
	want := "oberth daemon started (pid 4242), listening on " + socket + "\n"
	if out != want {
		t.Fatalf("stdout = %q, want %q", out, want)
	}
}

// stubDaemonChild replaces the launch for one test. What is not replaced is the
// child command itself, the log file, the working directory and the wait.
func stubDaemonChild(t *testing.T, start func(*exec.Cmd) (int, error)) {
	t.Helper()
	prev := startDaemonChild
	startDaemonChild = start
	t.Cleanup(func() { startDaemonChild = prev })
}

// TestRunForegroundDaemonServesUntilItsContextEnds runs the process that becomes
// the daemon: it moves into the state directory, listens on the socket the CLI
// looks at, answers status from this very process, and returns when its context
// ends.
func TestRunForegroundDaemonServesUntilItsContextEnds(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the unix-socket harness does not apply to named pipes")
	}
	socket := isolateDaemonPaths(t)

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
	go func() { done <- runForegroundDaemon(ctx) }()

	if err := client.WaitForSocket(context.Background(), socket, 10*time.Second); err != nil {
		cancel()
		t.Fatalf("the foreground daemon never listened on %s: %v (%v)", socket, err, <-done)
	}
	c, err := client.Dial(context.Background(), client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
	if err != nil {
		cancel()
		t.Fatalf("connecting: %v", err)
	}
	var status rpc.DaemonStatusResult
	callErr := c.Call(context.Background(), "daemon.status", rpc.Empty{}, &status)
	c.Close()
	if callErr != nil {
		cancel()
		t.Fatalf("daemon.status: %v", callErr)
	}
	if status.PID != os.Getpid() {
		t.Errorf("daemon pid = %d, want this process %d", status.PID, os.Getpid())
	}

	cancel()
	select {
	case serveErr := <-done:
		if serveErr != nil {
			t.Fatalf("runForegroundDaemon returned %v, want a clean stop", serveErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("runForegroundDaemon did not return after its context ended")
	}
}
