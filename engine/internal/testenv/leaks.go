package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The leak gate.
//
// A test that reaches the CLI's autostart path spawns "<this binary> serve
// --detach". The guards in internal/daemon and internal/daemon/client stop that
// spawn from happening at all, but a guard can be bypassed (a harness sets
// BERTH_ALLOW_TEST_DAEMON, a new code path forgets to consult it), and a leak is
// silent: the parent test passes and the stray process lives on. So every
// isolated package also looks, after its tests have run, and fails the run if
// anything is still there.
//
// What it may look at is bounded by ownership. The gate kills what it finds, so
// a rule that matched more than this run started would make one suite kill
// another's daemon and fail that run for a leak it did not cause — which is what
// "any oberth serve under the temp directory" did on a machine running several
// worktrees at once.

// Daemon is one stray process left behind by this test binary.
type Daemon struct {
	PID     int    // the process to stop
	Command string // the command line ps reported, for the message
}

// String is how a leak is named in the failure message.
func (d Daemon) String() string { return "pid " + strconv.Itoa(d.PID) + ": " + d.Command }

// AssertNoLeakedDaemons finds whatever this test binary left running as a
// daemon, kills it, and says so on stderr. It returns what it found, so the
// caller can turn a non-empty result into a failing exit code; Run does.
//
// Windows has no ps and no cheap way to read another process's command line from
// Go, so the check is unix-only. The guards it backs up are not.
func AssertNoLeakedDaemons() []Daemon {
	match := ownedDaemonMatcher()
	leaked := waitForStrays(2*time.Second, 100*time.Millisecond, LeakedDaemons)
	if len(leaked) == 0 {
		return nil
	}
	reportAndKill(leaked, match)
	return leaked
}

// ownedDaemonMatcher is the ownership rule the scan used, in the form the
// re-read before a signal needs. It returns nil when this process cannot name
// its own executable, in which case nothing is a candidate anyway.
func ownedDaemonMatcher() func(string) bool {
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	gateAll := os.Getenv(gateAllEnv) == "1"
	return func(cmdline string) bool { return isOwnedProcess(exe, cmdline, gateAll) }
}

// RequireNoLeakedDaemons is AssertNoLeakedDaemons for a single test: one test
// that started a daemon and did not stop it fails on its own.
func RequireNoLeakedDaemons(t testing.TB) {
	t.Helper()
	if survive := AssertNoLeakedDaemons(); len(survive) > 0 {
		t.Fatalf("the test left %d daemon process(es) running: %v", len(survive), survive)
	}
}

// waitForStrays polls until there is nothing left or the budget runs out.
//
// A cleanup that has asked a daemon to stop is racing us: the socket is already
// gone but the process has not been reaped. Giving teardown a moment is what
// keeps the gate reporting real leaks instead of slow exits.
func waitForStrays(window, every time.Duration, find func() []Daemon) []Daemon {
	deadline := time.Now().Add(window)
	for {
		leaked := find()
		if len(leaked) == 0 || time.Now().After(deadline) {
			return leaked
		}
		time.Sleep(every)
	}
}

// reportAndKill says what survived and stops what is still the same process,
// so a failed run does not leave the machine dirtier than it found it. The
// ownership rule is applied again immediately before each signal: the scan and
// the kill are separated by the scheduler, and a leaked daemon can exit and
// have its pid reused in that window.
func reportAndKill(leaked []Daemon, match func(string) bool) {
	lines := []string{"", fmt.Sprintf("testenv: %d leaked daemon process(es) survived this test binary:", len(leaked))}
	killed := 0
	for _, d := range leaked {
		line := "  " + d.String()
		if KillIfMatches(d.PID, match) {
			killed++
		} else {
			line += " (not signalled: the pid no longer names this run's daemon)"
		}
		lines = append(lines, line)
	}
	switch {
	case killed == len(leaked):
		lines = append(lines, "testenv: they were killed, but a test started a daemon it did not stop.")
	case killed > 0:
		lines = append(lines, fmt.Sprintf("testenv: %d of them were killed; the rest no longer matched this run, but a test started a daemon it did not stop.", killed))
	default:
		lines = append(lines, "testenv: none were signalled because none still matched this run, but a test started a daemon it did not stop.")
	}
	fmt.Fprintln(os.Stderr, strings.Join(lines, "\n"))
}

// KillIfMatches stops pid only when a fresh read of its command line still
// satisfies match. It is the cleanup primitive for a process this run did not
// start and therefore cannot hold an *os.Process for: the scan that named the
// pid is not ownership evidence by itself.
//
// It returns whether a signal was delivered. pid 1 and this process are never
// candidates, and a nil match refuses.
func KillIfMatches(pid int, match func(string) bool) bool {
	if pid <= 1 || pid == os.Getpid() || match == nil {
		return false
	}
	line, err := processCommand(pid)
	if err != nil || line == "" || !match(line) {
		return false
	}
	return killProcess(pid) == nil
}

// processCommand is one process's command line from a fresh ps read, including
// its environment where the platform's ps supports it. The environment is what
// the scenario harness owns its processes by. Tests replace it.
var processCommand = func(pid int) (string, error) {
	out, err := exec.Command("ps", "eww", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	return strings.TrimSpace(string(out)), err
}

// killProcess delivers the signal a cleanup sends. Tests replace it so the
// ownership decision can be checked without a real process.
var killProcess = func(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Kill()
}

// LeakedDaemons lists the running `serve` processes this run owns.
//
// Two rules apply, and both are about ownership rather than about what the
// process is called. This test binary running as `serve` is ours; so is a
// `serve` from any executable under this run's temp root (Root), which is where
// the integration harnesses build the binary they drive. Nothing else is ever a
// candidate, so neither a developer's installed daemon nor another `go test`
// sharing the machine can be mistaken for this run's leak.
//
// BERTH_TESTENV_GATE_ALL=1 widens the net to any `oberth serve` running from
// anywhere under the machine's temp directory. That is right on a CI runner,
// which has one checkout and one suite and would rather over-match than miss a
// stray daemon, and wrong on a developer's machine, where several worktrees run
// their suites at once.
func LeakedDaemons() []Daemon {
	if runtime.GOOS == "windows" {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return nil
	}
	out, err := processTable()
	if err != nil {
		return nil
	}
	gateAll := os.Getenv(gateAllEnv) == "1"
	return scanProcesses(string(out), os.Getpid(), exe, gateAll)
}

// scanProcesses applies the ownership rules to `ps -axo pid=,command=` output.
func scanProcesses(psOut string, self int, exe string, gateAll bool) []Daemon {
	var leaked []Daemon
	for _, line := range strings.Split(psOut, "\n") {
		pid, cmdline, ok := splitPS(line)
		if !ok || pid == self {
			continue
		}
		if isOwnedProcess(exe, cmdline, gateAll) {
			leaked = append(leaked, Daemon{PID: pid, Command: cmdline})
		}
	}
	return leaked
}

// isOwnedProcess applies the two ownership rules plus the opt-in machine-wide
// one.
func isOwnedProcess(exe, cmdline string, gateAll bool) bool {
	path, isServe := splitServe(cmdline)
	if !isServe {
		return false
	}
	switch {
	case path == exe:
		// This test binary running as a daemon: `go test ./...` runs packages
		// in parallel and names every test binary <pkg>.test, so the match is
		// on the full path and nothing looser.
		return true
	case current.Root != "" && under(path, current.Root):
		// A binary this run built. Only this run puts anything there, so
		// another run's daemon can never match.
		return true
	case gateAll:
		return isNamedOberth(path) && under(path, machineTempDir())
	}
	return false
}

// isNamedOberth reports whether path is an `oberth` executable, with the
// Windows `.exe` suffix allowed.
func isNamedOberth(path string) bool {
	name := strings.TrimSuffix(strings.ToLower(filepath.Base(path)), ".exe")
	return name == "oberth"
}

// machineTempDir is the temp directory the machine had before Isolate pointed
// TMPDIR at this run's root.
func machineTempDir() string {
	if current.realTemp != "" {
		return current.realTemp
	}
	return os.TempDir()
}

// splitPS reads one `ps -axo pid=,command=` row: a pid, a space, then the
// command line as ps joined it.
func splitPS(line string) (pid int, cmdline string, ok bool) {
	fields := strings.SplitN(strings.TrimSpace(line), " ", 2)
	if len(fields) != 2 {
		return 0, "", false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, "", false
	}
	return pid, strings.TrimSpace(fields[1]), true
}

// serveArg is the argument that makes a process a daemon.
const serveArg = " serve"

// splitServe returns the executable of a `ps` command line whose first argument
// is `serve`.
//
// `ps -axo command=` joins argv with single spaces and quotes nothing, so a path
// containing spaces cannot be recovered by splitting on whitespace: an
// executable at "/tmp/my dir/oberth" would look like the two arguments
// "/tmp/my" and "dir/oberth". The recoverable thing is the boundary — the
// executable is everything before the first " serve" that stands as a whole
// argument rather than as part of a longer one.
func splitServe(cmdline string) (string, bool) {
	for at := 0; at < len(cmdline); {
		found := strings.Index(cmdline[at:], serveArg)
		if found < 0 {
			return "", false
		}
		found += at
		next := found + len(serveArg)
		if next == len(cmdline) || cmdline[next] == ' ' {
			return cmdline[:found], true
		}
		at = found + 1
	}
	return "", false
}

// processTable is the machine's `ps -axo pid=,command=` output.
//
// No ps is not a failure: a minimal container may not have one, and the gate is
// best-effort — the guards in the product are what actually prevent a leak.
func processTable() ([]byte, error) {
	return exec.Command("ps", "-axo", "pid=,command=").Output()
}
