//go:build !windows

package killer

import (
	"context"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/testenv"
)

// This file runs the killer against real processes: a shell that spawns a
// listener, which spawns a second listener of its own —
//
//	sh ── helper (port A) ── helper (port B)
//
// which is the shape of every dev server worth killing (`npm` → `vite` → its
// HMR socket). Stopping port A must leave nothing behind on either port.
//
// Each helper reports its own pid to a file, so a case never has to guess which
// process owns a port: the answer comes from the process itself, not from
// matching command lines in a table.

const (
	envHelperPort    = "BERTH_KILLER_HELPER_PORT"
	envHelperChild   = "BERTH_KILLER_HELPER_CHILD_PORT"
	envHelperIgnore  = "BERTH_KILLER_HELPER_IGNORE_TERM"
	envHelperPIDFile = "BERTH_KILLER_HELPER_PIDFILE"
)

// TestMain doubles as the helper process: re-executing the test binary with a
// helper port set turns it into a listener instead of a test run. A helper is
// not a test run, so it is not the thing testenv isolates — it inherits the
// environment its parent was already isolated into.
func TestMain(m *testing.M) {
	if port := os.Getenv(envHelperPort); port != "" {
		runHelper(port, os.Getenv(envHelperChild), os.Getenv(envHelperPIDFile),
			os.Getenv(envHelperIgnore) != "")
		return
	}
	os.Exit(testenv.Run(m))
}

// runHelper listens on port, optionally spawns a child helper on childPort, and
// then blocks until it is signalled. With ignoreTerm it plays the wedged dev
// server that only SIGKILL can stop; otherwise the default SIGTERM disposition
// is what a well-behaved one gives us.
//
// Every helper reports its own pid into pidDir, named after the port it serves.
// A helper knows its pid and its port and nothing else, so that name is the one
// thing it can write without being told; a parent spawning a child therefore
// does not have to pass an identity down.
func runHelper(port, childPort, pidDir string, ignoreTerm bool) {
	if ignoreTerm {
		signal.Ignore(syscall.SIGTERM)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(1)
	}
	defer ln.Close()
	// Report this process before serving, so a test never races the answer.
	if pidDir != "" {
		if err := os.WriteFile(pidFileFor(pidDir, port), []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
			os.Exit(1)
		}
	}

	if childPort != "" {
		child := exec.Command(os.Args[0])
		child.Env = append(os.Environ(), envHelperPort+"="+childPort, envHelperChild+"=")
		if err := child.Start(); err != nil {
			os.Exit(1)
		}
	}
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		_ = conn.Close()
	}
}

// pidFileFor is the path a helper serving port reports itself to.
func pidFileFor(dir, port string) string { return filepath.Join(dir, "helper-"+port+".pid") }

// freePort asks the kernel for a port nothing is using and hands it back.
func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// portAnswers is the test's own question — "is something accepting here?" — and
// it folds the probe's third answer into "no": a helper in these tests either
// accepts or is gone, and `waitForPort(false)` is about the port being gone.
func portAnswers(port int) bool { return probePort(port, "127.0.0.1") == portOpen }

func waitForPort(t *testing.T, port int, want bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if portAnswers(port) == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("port %d: still open=%v after 10s, wanted open=%v", port, !want, want)
}

// readPID waits for a helper to report the pid it runs as.
func readPID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no pid reported at %s within 10s", path)
	return 0
}

// tree is one started helper tree: the ports it serves, the pids that serve
// them, and the shell waiting on them.
type tree struct {
	parentPort, childPort int
	parentPID, childPID   int
	shell                 *exec.Cmd
}

// startTree launches `sh -c '<test binary> & wait'` so that the listener really
// is a child of a shell, and waits until both listeners answer.
func startTree(t *testing.T) tree {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	dir := t.TempDir()
	parentPort, childPort := freePort(t), freePort(t)
	got := tree{parentPort: parentPort, childPort: childPort}

	shell := exec.Command("sh", "-c", `"$0" & wait`, exe)
	shell.Env = append(os.Environ(),
		envHelperPort+"="+strconv.Itoa(parentPort),
		envHelperChild+"="+strconv.Itoa(childPort),
		envHelperPIDFile+"="+dir,
	)
	if err := shell.Start(); err != nil {
		t.Fatalf("starting the helper tree: %v", err)
	}
	got.shell = shell
	t.Cleanup(func() {
		// Whatever the test did, leave no strays behind.
		if shell.Process != nil {
			killTreeNow(shell.Process.Pid)
		}
		_ = shell.Wait()
	})

	waitForPort(t, parentPort, true)
	waitForPort(t, childPort, true)
	got.parentPID = readPID(t, pidFileFor(dir, strconv.Itoa(parentPort)))
	got.childPID = readPID(t, pidFileFor(dir, strconv.Itoa(childPort)))
	return got
}

// snapshotForPID is a hand-built scan row for one pid and port, used when the
// machine cannot show us the table itself.
func snapshotForPID(pid, port int) ports.ListeningPort {
	return ports.ListeningPort{
		Port: port, PID: pid, BindAddress: "127.0.0.1",
		Process: "killer.test", Type: ports.PortTypeUser,
	}
}

// killTreeNow is the test's own belt-and-braces cleanup: SIGKILL the process
// tree below pid so a failed case cannot leave a listener behind.
func killTreeNow(pid int) {
	table := scanProcessTable()
	for _, p := range table.Descendants(pid) {
		_ = signalProcess(p, true)
	}
}

// snapshotFor returns the scanner's view of a port when the machine can produce
// one, and a hand-built row otherwise. The fallback still exercises everything
// that matters here — the ps table, the signals, the port probe — so a sandbox
// that cannot see the socket table does not turn these cases into skips.
func snapshotFor(t *testing.T, port, knownPID int) []ports.ListeningPort {
	t.Helper()
	if found, err := ports.Scan(); err == nil {
		docker.EnrichPorts(found)
		ports.Enrich(found)
		for i := range found {
			if found[i].Port == port {
				return found
			}
		}
	}
	return []ports.ListeningPort{snapshotForPID(knownPID, port)}
}

func TestKillPortsStopsARealProcessTree(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	tree := startTree(t)

	results := KillPorts(context.Background(),
		[]Target{{Port: tree.parentPort}},
		Options{Tree: true, Grace: 2 * time.Second, Ports: snapshotFor(t, tree.parentPort, tree.parentPID)},
	)

	if len(results) == 0 {
		t.Fatal("no result rows")
	}
	for _, r := range results {
		if !r.OK {
			t.Errorf("row %+v failed: %s", r, r.Error)
		}
	}
	// The listener and the port it spawned are both gone.
	waitForPort(t, tree.parentPort, false)
	waitForPort(t, tree.childPort, false)

	// And the shell that was waiting on them has nothing left to wait for.
	done := make(chan struct{})
	go func() { _ = tree.shell.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the shell is still running after its tree was killed")
	}
}

func TestKillPortsDryRunLeavesARealTreeRunning(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	tree := startTree(t)

	results := KillPorts(context.Background(),
		[]Target{{Port: tree.parentPort}},
		Options{Tree: true, DryRun: true, Ports: snapshotFor(t, tree.parentPort, tree.parentPID)},
	)

	if len(results) < 2 {
		t.Fatalf("dry run planned %d rows, want the listener and its child", len(results))
	}
	if !portAnswers(tree.parentPort) || !portAnswers(tree.childPort) {
		t.Fatal("dry run killed something")
	}
	// The child is planned before the parent: the last row is the root, and
	// every other planned pid sits below it in the process table.
	root := results[len(results)-1].PID
	below := scanProcessTable().Descendants(root)
	for _, r := range results[:len(results)-1] {
		if !contains(below, r.PID) {
			t.Errorf("row %+v is not below the last row (PID %d)", r, root)
		}
	}
}

func contains(haystack []int, needle int) bool {
	for _, v := range haystack {
		if v == needle {
			return true
		}
	}
	return false
}

func TestKillPortsEscalatesOnAProcessThatIgnoresSIGTERM(t *testing.T) {
	if testing.Short() {
		t.Skip("spawns real processes")
	}
	port := freePort(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	pidDir := t.TempDir()
	wedged := exec.Command(exe)
	wedged.Env = append(os.Environ(),
		envHelperPort+"="+strconv.Itoa(port),
		envHelperIgnore+"=1",
		envHelperPIDFile+"="+pidDir,
	)
	if err := wedged.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if wedged.Process != nil {
			_ = signalProcess(wedged.Process.Pid, true)
		}
		_ = wedged.Wait()
	})
	waitForPort(t, port, true)

	// The listener ignores SIGTERM, so only the escalation to SIGKILL frees
	// the port. A short grace keeps the test quick.
	snapshot := []ports.ListeningPort{snapshotForPID(readPID(t, pidFileFor(pidDir, strconv.Itoa(port))), port)}
	results := KillPorts(context.Background(), []Target{{Port: port}},
		Options{Grace: 500 * time.Millisecond, Ports: snapshot})

	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	if results[0].Method != state.MethodSIGKILL {
		t.Fatalf("method = %q, want sigkill after the grace period", results[0].Method)
	}
	waitForPort(t, port, false)
}
