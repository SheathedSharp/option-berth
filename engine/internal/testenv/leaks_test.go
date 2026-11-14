package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The gate must match this run's runtime services and nobody else's. It used to match
// any `oberth runtime service run` under the machine's temp directory, so a run killed the
// runtime services of every other worktree building into temp at the same moment and
// failed those runs for a leak they had not caused.
//
// One directory here has a space in it on purpose: ps quotes nothing, so a gate
// that split the command line on whitespace would read one path as two
// arguments.
func TestGateMatchesOnlyThisRunsRoot(t *testing.T) {
	requiringPS(t)

	ours := fakeRuntimeService(t, filepath.Join(t.TempDir(), "gate inside"))
	theirs := fakeRuntimeService(t, dirOutsideThisRun(t))

	t.Setenv(gateAllEnv, "")
	leaked := LeakedRuntimeServices()
	if !carriesPID(leaked, ours) {
		t.Errorf("the gate missed pid %d, a `runtime service run` under this run's root %s: %v", ours, Root(), leaked)
	}
	if carriesPID(leaked, theirs) {
		t.Errorf("the gate claimed pid %d, a `runtime service run` outside this run's root %s: %v", theirs, Root(), leaked)
	}

	// CI is one runner with one suite, so it opts back into the wide net.
	t.Setenv(gateAllEnv, "1")
	if leaked := LeakedRuntimeServices(); !carriesPID(leaked, theirs) {
		t.Errorf("%s=1 did not restore the machine-wide match for pid %d: %v", gateAllEnv, theirs, leaked)
	}
}

// A child of the test process writes its temp files into this run's root, which
// is what makes the ownership rule true of the binaries the integration
// harnesses build with `go build -o`.
func TestChildrenInheritTheRunRootAsTMPDIR(t *testing.T) {
	if !sameDir(os.TempDir(), Root()) {
		t.Fatalf("os.TempDir() = %s, want this run's root %s", os.TempDir(), Root())
	}
	RequireIsolated(t, t.TempDir())

	reported, err := childTempDir(t)
	if err != nil {
		t.Fatalf("asking a child process for its temp directory: %v", err)
	}
	if !sameDir(reported, Root()) {
		t.Errorf("a child process resolved its temp directory to %s, want %s", reported, Root())
	}
}

// fakeRuntimeService starts a process that looks to `ps` exactly like a leaked runtime service —
// a binary named oberth in dir, with `runtime service run` as its first argument — and
// returns its pid. It is this test binary under another name, told by the
// environment to sit still rather than run the suite.
func fakeRuntimeService(t *testing.T, dir string) int {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating %s: %v", dir, err)
	}
	bin := filepath.Join(dir, "oberth")
	if err := cloneExecutable(executable(t), bin); err != nil {
		t.Fatalf("putting a stand-in binary at %s: %v", bin, err)
	}

	return startStanding(t, bin)
}

// cloneExecutable puts a runnable copy of src at dst. It links where the
// filesystem allows it, because the test binary is not small.
func cloneExecutable(src, dst string) error {
	if err := os.Link(src, dst); err == nil {
		return nil
	}
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, body, 0o755)
}

// dirOutsideThisRun returns a directory under the machine's temp directory that
// this run does not own, cleaned up when the test ends.
func dirOutsideThisRun(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(machineTempDir(), "option-berth-gate-outside")
	if err != nil {
		t.Fatalf("creating a directory outside this run's root: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func executable(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatalf("finding this test binary: %v", err)
	}
	return self
}

func carriesPID(runtimeServices []RuntimeService, pid int) bool {
	for _, d := range runtimeServices {
		if d.PID == pid {
			return true
		}
	}
	return false
}

// requiringPS skips a case the leak gate cannot serve here: Windows has no ps,
// and a minimal container may not either.
func requiringPS(t *testing.T) {
	t.Helper()
	if reason := missingPS(); reason != "" {
		t.Skip(reason)
	}
}

// missingPS names why the gate cannot run here, or "" when it can.
func missingPS() string {
	if runtime.GOOS == "windows" {
		return "the leak gate reads ps, which Windows does not have"
	}
	if _, err := exec.LookPath("ps"); err != nil {
		return "no ps on this machine: " + err.Error()
	}
	return ""
}

// childTempDir asks a child process what it resolved its temp directory to.
func childTempDir(t *testing.T) (string, error) {
	t.Helper()
	cmd := exec.Command(executable(t))
	cmd.Env = append(os.Environ(), helperEnv+"="+helperTempDir)
	out, err := cmd.Output()
	return string(out), err
}

// startStanding starts bin with `runtime service run` as its argument and leaves it running
// until the test ends, returning its pid.
func startStanding(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin, "runtime", "service", "run")
	cmd.Env = append(os.Environ(), helperEnv+"="+helperRuntimeServiceRun)
	keepAlive, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("opening the stand-in's stdin: %v", err)
	}
	halt := func() {
		_ = keepAlive.Close() // the stand-in stops when its stdin closes
		_ = cmd.Wait()
		_ = cmd.Process.Kill()
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in runtime_service: %v", err)
	}
	t.Cleanup(halt)
	return cmd.Process.Pid
}

// A cleanup that scanned the process table must not signal a pid that now
// belongs to something else: the scan and the signal are separated by the
// scheduler, and a leaked process can exit and have its pid reused in between.
// KillIfMatches closes that window by re-reading the process before signalling.
func TestKillIfMatchesRechecksTheProcessBeforeSignalling(t *testing.T) {
	prevCommand, prevKill := processCommand, killProcess
	t.Cleanup(func() { processCommand, killProcess = prevCommand, prevKill })

	var killed []int
	killProcess = func(pid int) error {
		killed = append(killed, pid)
		return nil
	}
	processCommand = func(int) (string, error) { return "old command line", nil }

	if !KillIfMatches(4242, func(line string) bool { return line == "old command line" }) {
		t.Fatal("a still-matching process was not signalled")
	}
	if len(killed) != 1 || killed[0] != 4242 {
		t.Fatalf("killed = %v, want [4242]", killed)
	}

	processCommand = func(int) (string, error) { return "a different process", nil }
	if KillIfMatches(4242, func(line string) bool { return line == "old command line" }) {
		t.Fatal("a pid whose command changed was signalled")
	}
	if len(killed) != 1 {
		t.Fatalf("killed = %v, want no second signal", killed)
	}

	if KillIfMatches(os.Getpid(), func(string) bool { return true }) {
		t.Fatal("the test process itself was a kill candidate")
	}
	if KillIfMatches(1, func(string) bool { return true }) {
		t.Fatal("pid 1 was a kill candidate")
	}
}

// The re-read uses the same ps the scan uses, including the environment, and a
// process this test owns is really stopped through it.
func TestKillIfMatchesStopsARealProcess(t *testing.T) {
	requiringPS(t)
	if runtime.GOOS == "windows" {
		t.Skip("the re-read reads ps")
	}

	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the stand-in: %v", err)
	}
	pid := cmd.Process.Pid
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	line, err := processCommand(pid)
	if err != nil {
		t.Fatalf("reading pid %d: %v", pid, err)
	}
	if !strings.Contains(line, "sleep") {
		t.Fatalf("processCommand(%d) = %q, want the sleep command", pid, line)
	}
	if !KillIfMatches(pid, func(line string) bool { return strings.Contains(line, "sleep") }) {
		t.Fatal("the scanned process was not signalled")
	}
	_ = cmd.Wait()
}
