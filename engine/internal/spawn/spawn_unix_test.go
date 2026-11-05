//go:build !windows

package spawn

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These tests fork real processes next to the rest of the suite, so every wait
// is a poll with one generous bound. A deadline tight enough to feel fast is
// only a bet that the machine is idle: on a loaded runner a fork, an exec and a
// first write can take seconds. A correct run reaches the condition long before
// the deadline and never spends it, so the bound is paid only by a real failure.
const (
	waitTimeout  = 30 * time.Second
	waitInterval = 50 * time.Millisecond
)

// isRunning reports whether a pid still exists; signal 0 is the portable probe.
func isRunning(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// pollUntil evaluates condition until it holds or the shared bound expires. It
// is evaluated once more after the deadline, so a condition that becomes true
// during the final sleep is not reported as a timeout.
func pollUntil(condition func() bool) bool {
	deadline := time.Now().Add(waitTimeout)
	for {
		if condition() {
			return true
		}
		if time.Now().After(deadline) {
			return condition()
		}
		time.Sleep(waitInterval)
	}
}

// awaitExit fails unless pid is gone within the bound.
func awaitExit(t *testing.T, what string, pid int) {
	t.Helper()
	if !pollUntil(func() bool { return !isRunning(pid) }) {
		t.Fatalf("%s (pid %d) is still running after %s", what, pid, waitTimeout)
	}
}

// readWhenReady polls until path holds something other than whitespace and
// returns it. Readiness files are published atomically — written to a temporary
// name and renamed — so any content at all is the whole content.
func readWhenReady(t *testing.T, path string) string {
	t.Helper()
	var last string
	ready := pollUntil(func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = string(content)
		return len(bytes.TrimSpace(content)) > 0
	})
	if !ready {
		t.Fatalf("%s did not appear within %s (last read %q)", path, waitTimeout, last)
	}
	return last
}

// readWhenContaining polls until path contains want.
//
// One read is never enough for a file a child is still writing: the read can
// land between the open and the first write, in the middle of a line, or — under
// dash — after a warning the shell printed ahead of the output under test.
// Waiting for the content that matters is the only check that means what it
// says.
func readWhenContaining(t *testing.T, path, want string) string {
	t.Helper()
	var last string
	found := pollUntil(func() bool {
		content, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		last = string(content)
		return strings.Contains(last, want)
	})
	if !found {
		t.Fatalf("%s never contained %q within %s (last read %q)", path, want, waitTimeout, last)
	}
	return last
}

// awaitPID is readWhenReady plus the parse every caller here does.
func awaitPID(t *testing.T, path string) int {
	t.Helper()
	raw := strings.TrimSpace(readWhenReady(t, path))
	pid, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("%s = %q, want a pid: %v", path, raw, err)
	}
	return pid
}

// announcePID is the shell fragment a helper uses to publish its own pid:
// write to a temporary name, then rename, so a reader sees either no file or the
// complete pid and never the empty file a plain `>` leaves between open and
// write. The destination is $0, which `sh -c SCRIPT NAME` sets to NAME — that
// keeps the path out of the quoted script.
//
// POSIX sh and nothing more, because /bin/sh is dash on Debian and Ubuntu: no
// bashisms, and every expansion of $0 quoted.
const announcePID = `echo $$ > "$0.tmp" && mv "$0.tmp" "$0"`

// shellQuote wraps s as one POSIX sh single-quoted word.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func TestSpawnInjectsTheRunEnvironment(t *testing.T) {
	var out bytes.Buffer
	handle, err := Spawn(context.Background(), Request{
		Argv:     []string{"sh", "-c", `echo "$BERTH_GROUP/$BERTH_NAME/$BERTH_RUN_ID/$BERTH_PORT"`},
		Cwd:      t.TempDir(),
		Group:    "itest",
		Name:     "web",
		PortHint: 8123,
		ID:       "run-1",
		Stdout:   &out,
		Stderr:   &out,
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := handle.Wait()
	if err != nil || code != 0 {
		t.Fatalf("child exited %d: %v (%s)", code, err, out.String())
	}
	if got := strings.TrimSpace(out.String()); got != "itest/web/run-1/8123" {
		t.Fatalf("child environment = %q", got)
	}
}

func TestSpawnPropagatesTheExitCode(t *testing.T) {
	handle, err := Spawn(context.Background(), Request{
		Argv:   []string{"sh", "-c", "exit 7"},
		Cwd:    t.TempDir(),
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
	})
	if err != nil {
		t.Fatal(err)
	}
	code, err := handle.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if code != 7 {
		t.Fatalf("exit code = %d, want 7", code)
	}
}

// TestForwardedInterruptKillsTheWholeProcessGroup is the acceptance behaviour
// of `oberth start`: Ctrl+C on the starter takes down the command *and* the
// listener it spawned, because the child runs in its own process group.
func TestForwardedInterruptKillsTheWholeProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pidFile := filepath.Join(dir, "pids")
	script := filepath.Join(dir, "dev.sh")
	// A dev.sh whose real work happens in a grandchild, the shape of every
	// `npm run dev`: killing the child alone would orphan the listener. The
	// grandchild publishes its pid before doing anything else, so the wait is
	// bounded by one fork rather than by whatever the helper does next.
	body := "#!/bin/sh\n" +
		"sh -c '" + announcePID + "; exec sleep 300' " + shellQuote(pidFile) + "\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}

	// A file rather than a pipe: a surviving grandchild would hold a pipe open
	// and hide the very failure this test looks for.
	logPath := filepath.Join(dir, "out.log")
	out, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()

	// The child is in its own process group, so the terminal's SIGINT would
	// never reach it: the forwarder is what makes Ctrl+C work, and it is
	// installed before the child so the child starts with a default SIGINT.
	forwarder := CatchSignals()
	defer forwarder.Stop()

	handle, err := Spawn(context.Background(), Request{
		Argv: []string{script}, Cwd: dir, Group: "itest", Name: "dev.sh",
		Stdout: out, Stderr: out,
	})
	if err != nil {
		t.Fatal(err)
	}
	forwarder.Forward(handle)
	// Only on failure: a passing run has already reaped both processes, and
	// signalling a reaped pid risks hitting whatever the OS recycled it into.
	t.Cleanup(func() {
		if t.Failed() {
			_ = handle.Kill()
		}
	})

	grandchild := awaitPID(t, pidFile)
	if !isRunning(grandchild) {
		t.Fatalf("the grandchild (pid %d) never started", grandchild)
	}
	t.Cleanup(func() {
		if t.Failed() {
			_ = syscall.Kill(grandchild, syscall.SIGKILL)
		}
	})

	if err := syscall.Kill(os.Getpid(), syscall.SIGINT); err != nil {
		t.Fatal(err)
	}

	exited := make(chan int, 1)
	go func() { code, _ := handle.Wait(); exited <- code }()
	select {
	case code := <-exited:
		if code != 130 {
			t.Errorf("exit code = %d, want 130 (128+SIGINT)", code)
		}
	case <-time.After(waitTimeout):
		_ = handle.Kill()
		log, _ := os.ReadFile(logPath)
		t.Fatalf("the child did not exit after a forwarded SIGINT within %s (log %q)", waitTimeout, log)
	}
	awaitExit(t, "the grandchild", grandchild)
}

// TestDetachedRunSurvivesItsStarter re-execs this test binary as the starter, so
// the child really is orphaned when the process that spawned it goes away.
func TestDetachedRunSurvivesItsStarter(t *testing.T) {
	dir := t.TempDir()
	logDir := filepath.Join(dir, "logs")
	pidFile := filepath.Join(dir, "pid")
	// The detached child's working directory has to outlive the starter, so it
	// is this test's temp dir and not one the starter cleans up on its way out.
	// A child left standing in a deleted directory is not merely untidy: dash
	// greets it with `sh: 0: getcwd() failed` on the run's own log, ahead of
	// anything the command prints.
	childCwd := filepath.Join(dir, "cwd")
	if err := os.MkdirAll(childCwd, 0o755); err != nil {
		t.Fatal(err)
	}

	starter := exec.Command(os.Args[0], "-test.run=TestSpawnDetachedHelper", "-test.v")
	starter.Env = append(os.Environ(),
		"BERTH_SPAWN_HELPER=1",
		"BERTH_SPAWN_LOGDIR="+logDir,
		"BERTH_SPAWN_PIDFILE="+pidFile,
		"BERTH_SPAWN_CWD="+childCwd,
	)
	if out, err := starter.CombinedOutput(); err != nil {
		t.Fatalf("starter failed: %v: %s", err, out)
	}

	// The starter publishes the pid only once the child's output is on disk, so
	// by here the log is readable — but it is still polled, because the starter
	// is not the only writer this test does not synchronise with.
	pid := awaitPID(t, pidFile)
	t.Cleanup(func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })

	if !isRunning(pid) {
		t.Fatalf("the detached run (pid %d) died with its starter", pid)
	}
	readWhenContaining(t, filepath.Join(logDir, "itest", "sleeper.log"), detachedMarker)
}

// detachedMarker is the line the detached run prints, and the readiness signal
// the starter waits for before it publishes the run's pid.
const detachedMarker = "detached-hello"

// TestSpawnDetachedHelper is not a test: it is the starter process for
// TestDetachedRunSurvivesItsStarter, and does nothing in a normal run.
func TestSpawnDetachedHelper(t *testing.T) {
	if os.Getenv("BERTH_SPAWN_HELPER") == "" {
		t.Skip("helper process for TestDetachedRunSurvivesItsStarter")
	}
	// TestMain discards inherited product overrides. This path belongs to the
	// parent test, so install it explicitly after isolation rather than letting
	// an arbitrary caller's BERTH_LOG_DIR escape the sandbox.
	logDir := os.Getenv("BERTH_SPAWN_LOGDIR")
	if logDir == "" {
		t.Fatal("starter has no parent-owned log directory")
	}
	t.Setenv("BERTH_LOG_DIR", logDir)
	// `printf` rather than `echo`, and a marker with no escapes in it, so the
	// line is byte-for-byte the same under every /bin/sh.
	handle, err := Spawn(context.Background(), Request{
		Argv:   []string{"sh", "-c", fmt.Sprintf("printf '%%s\n' %s; exec sleep 300", shellQuote(detachedMarker))},
		Cwd:    os.Getenv("BERTH_SPAWN_CWD"),
		Group:  "itest",
		Name:   "sleeper",
		Detach: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Readiness is the output itself: the pid is published only once the marker
	// has reached the log, so the parent never races the child's first write.
	// Waiting on the log also flushes out the shell's own start-up noise, which
	// lands there first when it lands at all.
	readWhenContaining(t, handle.LogPath, detachedMarker)
	publishPID(t, os.Getenv("BERTH_SPAWN_PIDFILE"), handle.PID)
}

// publishPID writes a pid to path atomically, so the parent sees either no file
// or the whole pid.
func publishPID(t *testing.T, path string, pid int) {
	t.Helper()
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(temporary, path); err != nil {
		t.Fatal(err)
	}
}

// TestLogRotation pins the policy end to end: the path a group and name produce,
// the size that triggers a rotation, the generations that survive it, and the
// one that is dropped.
func TestLogRotation(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("BERTH_LOG_DIR", dir)

	// A group name is not a path, even when it contains separators.
	path := LogPath("group/with/slash", "name")
	if want := filepath.Join(dir, "group-with-slash", "name.log"); path != want {
		t.Fatalf("LogPath = %q, want %q", path, want)
	}

	fill := func(fillByte byte) {
		t.Helper()
		file, err := OpenLog(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write(bytes.Repeat([]byte{fillByte}, int(LogMaxBytes)+1)); err != nil {
			t.Fatal(err)
		}
		file.Close()
	}

	fill('x')
	// Reopening past the limit rotates; twice more fills the policy.
	fill('y')
	fill('z')

	for _, name := range []string{"name.log", "name.log.1", "name.log.2"} {
		if _, err := os.Stat(filepath.Join(dir, "group-with-slash", name)); err != nil {
			t.Errorf("missing generation %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "group-with-slash", "name.log.3")); err == nil {
		t.Error("name.log.3 should have been dropped: the policy keeps three generations")
	}
}
