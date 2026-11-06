package testenv

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// The gate must match this run's daemons and nobody else's. It used to match
// any `oberth serve` under the machine's temp directory, so a run killed the
// daemons of every other worktree building into temp at the same moment and
// failed those runs for a leak they had not caused.
//
// One directory here has a space in it on purpose: ps quotes nothing, so a gate
// that split the command line on whitespace would read one path as two
// arguments.
func TestGateMatchesOnlyThisRunsRoot(t *testing.T) {
	requiringPS(t)

	ours := fakeDaemon(t, filepath.Join(t.TempDir(), "gate inside"))
	theirs := fakeDaemon(t, dirOutsideThisRun(t))

	t.Setenv(gateAllEnv, "")
	leaked := LeakedDaemons()
	if !carriesPID(leaked, ours) {
		t.Errorf("the gate missed pid %d, a `serve` under this run's root %s: %v", ours, Root(), leaked)
	}
	if carriesPID(leaked, theirs) {
		t.Errorf("the gate claimed pid %d, a `serve` outside this run's root %s: %v", theirs, Root(), leaked)
	}

	// CI is one runner with one suite, so it opts back into the wide net.
	t.Setenv(gateAllEnv, "1")
	if leaked := LeakedDaemons(); !carriesPID(leaked, theirs) {
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

// fakeDaemon starts a process that looks to `ps` exactly like a leaked daemon —
// a binary named oberth in dir, with `serve` as its first argument — and
// returns its pid. It is this test binary under another name, told by the
// environment to sit still rather than run the suite.
func fakeDaemon(t *testing.T, dir string) int {
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

func carriesPID(daemons []Daemon, pid int) bool {
	for _, d := range daemons {
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

// startStanding starts bin with `serve` as its argument and leaves it running
// until the test ends, returning its pid.
func startStanding(t *testing.T, bin string) int {
	t.Helper()
	cmd := exec.Command(bin, "serve")
	cmd.Env = append(os.Environ(), helperEnv+"="+helperServe)
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
		t.Fatalf("starting the stand-in daemon: %v", err)
	}
	t.Cleanup(halt)
	return cmd.Process.Pid
}
