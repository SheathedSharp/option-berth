//go:build !windows

// The owned-run fixtures create a process group with Setpgid and read process
// liveness with signal 0; neither spelling exists on Windows.

package cmd

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
)

// ownedRun is a run option-berth knows about, present in this machine's process
// table: a wrapper that owns a listener and keeps running after it, exactly the
// `sh ── npm ── vite` shape a dev server has.
//
//	sh (the run root, registered in runs.json) ── helper (the listener) ── sleep
//
// The listener is what `--pid` names; the wrapper is what a run-scoped stop has
// to reach as well, and it would otherwise sit there until its sleep ended.
type ownedRun struct {
	port       int
	listenerID int
	rootID     int
	shell      *exec.Cmd
}

// alive reports whether a process is still running. A zombie is not running: it
// has exited and is only waiting to be reaped, which for this harness's own
// direct child is the test's job rather than the kernel's. Signal 0 alone would
// call that "alive" and turn a completed stop into a failure.
func (r ownedRun) alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	if syscall.Kill(pid, 0) != nil {
		return false
	}
	out, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		// No process record to read: the signal-0 answer is all there is.
		return true
	}
	state, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	return !strings.HasPrefix(state, "Z")
}

// waitGone waits for the kernel to finish taking a signalled process away. A
// signal is delivered asynchronously, so "it is not there" is a fact that comes
// true a moment after `kill` returns, not at the instant it does.
func (r ownedRun) waitGone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !r.alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("%s %d is still running 10s after it was signalled", what, pid)
}

// startOwnedRun starts the tree above, waits for the listener, and registers the
// wrapper in this machine's run registry, which is what gives the listener its
// attribution. BERTH_HOME is isolated per test by the caller so the registry
// file is that test's alone.
func startOwnedRun(t *testing.T) ownedRun {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("locating the test binary: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	ln.Close()
	port, _ := strconv.Atoi(portStr)
	pidFile := filepath.Join(t.TempDir(), "listener.pid")

	// The wrapper stays alive on its own (the sleep), so "did the run-scoped stop
	// reach it" is a question the test can still ask afterwards.
	shell := exec.Command("sh", "-c", `"$0" -test.run=TestKillHelperListener -test.timeout=90s & sleep 600`, exe)
	shell.Env = append(os.Environ(),
		"BERTH_TEST_LISTEN="+portStr,
		"BERTH_TEST_PIDFILE="+pidFile,
	)
	shell.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	born := time.Now().UTC()
	if err := shell.Start(); err != nil {
		t.Fatalf("starting the owned run: %v", err)
	}
	root := heldKillProcessGroup(shell.Process, born)
	run := ownedRun{port: port, rootID: shell.Process.Pid, shell: shell}
	t.Cleanup(func() {
		_ = killOwnedIdentities(root)
		_ = shell.Wait()
	})

	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", "127.0.0.1:"+portStr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// The listener reports its own pid, so the test never has to guess which
	// process holds the socket.
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				run.listenerID = pid
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.listenerID == 0 {
		t.Fatalf("the listener never reported its pid")
	}

	if err := runs.Add(runs.Entry{
		PID: run.rootID, Tag: "storefront", ID: "run-storefront-api",
		Group: "storefront", Name: "api", Cmd: `"$0" & sleep 600`,
		StartedAt: time.Now().Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatalf("recording the run: %v", err)
	}
	// The snapshot is this run's listener only, attributed from the isolated
	// registry. It is not a filtered machine-wide scan.
	ids := []isolatedProcessIdentity{root}
	if run.listenerID != run.rootID {
		// The listener is a child this test did not Start. Capture its birth
		// for this one pid; do not walk a live descendant table to claim
		// ownership of anyone else. This is not a create-time handle.
		ids = append(ids, mustCapturePIDIdentity(t, run.listenerID))
	}
	registerIsolatedKillFixture(t, ports.ListeningPort{
		Port: run.port, PID: run.listenerID, BindAddress: "127.0.0.1",
		Type: ports.PortTypeUser,
	}, ids...)
	useIsolatedEnrichedKillScan(t)
	return run
}

// TestKillJSONNamesAnOwnedRunForAPIDOnBothPaths is the same parity question for
// the selector the review found it missing from. Naming the listener's pid has
// to report what the runtime service path reports for that same pid — the run's service
// name and its port — because the attribution lives in the listener table both
// paths read, and the direct path used not to read it at all for `--pid`.
//
// The run is planned, never signalled: a dry run keeps the tree alive, so the
// test can go on to stop it deliberately.
func TestKillJSONNamesAnOwnedRunForAPIDOnBothPaths(t *testing.T) {
	resetRouting(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	run := startOwnedRun(t)
	withKillFlags(t, func() {
		killDryRunFlag, killJSONFlag, killYesFlag = true, true, true
		killPIDFlag = []int{run.listenerID}
	})

	noRuntimeServiceReachable(t)
	direct, err := runKillCmd(t)
	if err != nil {
		t.Fatalf("the direct path failed: %v\n%s", err, direct)
	}

	resetRouting(t)
	startScanningRuntimeService(t)
	routed, err := runKillCmd(t)
	if err != nil {
		t.Fatalf("the runtime service path failed: %v\n%s", err, routed)
	}

	if routed != direct {
		t.Fatalf("--json for --pid differs between the two paths:\n runtime_service:\n%s\n direct:\n%s", routed, direct)
	}
	// A run is one unit: the attributed row names the service and its port,
	// and the unit it is stopped through is the run root, not the leaf pid the
	// user typed. Both paths have to agree on that.
	var rows []killer.Result
	if err := json.Unmarshal([]byte(direct), &rows); err != nil {
		t.Fatalf("stdout is not the result list: %v\n%s", err, direct)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %+v, want the run reported as one unit", rows)
	}
	if got := rows[0]; got.PID != run.rootID || got.Name != "api" || got.Port != run.port || !got.OK {
		t.Fatalf("row = %+v, want the run root %d named api on port %d", got, run.rootID, run.port)
	}
}

// TestKillPIDStopsTheOwnedRunTreeAndReportsTheReleasedURL is the behavior the
// parity test above can only imply: `kill --pid <a run's listener>` stops the
// run — the wrapper goes with the listener — and reports the address that was
// released. Both facts come from the attributed table; without it the stop is
// the bare pid and the report has no address to name.
func TestKillPIDStopsTheOwnedRunTreeAndReportsTheReleasedURL(t *testing.T) {
	resetRouting(t)
	t.Setenv("BERTH_HOME", t.TempDir())
	run := startOwnedRun(t)
	withKillFlags(t, func() { killPIDFlag = []int{run.listenerID} })

	noRuntimeServiceReachable(t)
	out, err := runKillCmd(t)
	if err != nil {
		t.Fatalf("kill --pid: %v\n%s", err, out)
	}
	if !strings.Contains(out, "api (PID "+strconv.Itoa(run.rootID)+") on port "+strconv.Itoa(run.port)) {
		t.Fatalf("stdout = %q, want the run root named api on the listener's port", out)
	}
	// A listener bound to 127.0.0.1 is released at that address, which is the
	// URL the report has to name: without the attributed table there is no row
	// to look the address up in, and the line is simply missing.
	if !strings.Contains(out, "released http://127.0.0.1:"+strconv.Itoa(run.port)) {
		t.Fatalf("stdout = %q, want the released address", out)
	}
	run.waitGone(t, run.listenerID, "the listener")
	run.waitGone(t, run.rootID, "the run root")
}
