package testenv

import (
	"os"
	"path/filepath"
	"testing"
)

// The guarantee the whole package exists for: by the time a test runs, nothing
// this binary resolves from the environment points at the machine's real
// install.
func TestIsolateMovesEverythingIntoTempDir(t *testing.T) {
	if Root() == "" {
		t.Fatal("Isolate did not run before the tests")
	}
	RequireIsolated(t, Home(), ConfigDir(), os.Getenv("HOME"), os.Getenv("BERTH_DB"))

	if resolved := executableHome(t); resolved != Home() {
		t.Fatalf("os.UserHomeDir() = %s, want the isolated %s", resolved, Home())
	}
	if _, err := os.Stat(ConfigDir()); err != nil {
		t.Errorf("the isolated config directory is not there: %v", err)
	}
}

// Autostart is off for the whole binary, and an opt-in a developer happens to
// have in their shell does not survive into a test run.
func TestIsolateDisablesAutostart(t *testing.T) {
	if os.Getenv(noAutostartEnv) == "" {
		t.Errorf("%s is unset, want it on", noAutostartEnv)
	}
	if value, present := os.LookupEnv(allowTestEnv); present {
		t.Errorf("%s = %q, want it removed", allowTestEnv, value)
	}
}

// Isolation must not take the Go build cache with it: the integration harnesses
// run `go build` to make the binary they drive, and a module cache inside the
// throwaway HOME means re-downloading the module graph every run and failing
// outright with no network.
func TestIsolateKeepsTheGoCachesWhereTheyWere(t *testing.T) {
	for _, key := range []string{"GOCACHE", "GOMODCACHE", "GOPATH"} {
		assertOutsideThisRun(t, key, os.Getenv(key))
	}
}

// assertOutsideThisRun fails when an environment variable is unset or points
// into the throwaway environment.
func assertOutsideThisRun(t *testing.T, key, value string) {
	t.Helper()
	if value == "" {
		t.Errorf("%s is unset; a `go build` from a test would rebuild the world", key)
		return
	}
	if Isolated(value) {
		t.Errorf("%s = %s, which is inside the throwaway test environment", key, value)
	}
}

func TestIsolatedRejectsPathsOutside(t *testing.T) {
	for _, outside := range []string{
		"/etc/passwd",
		// Another run's directory is under the same machine temp directory and
		// is no more this run's business than /etc is.
		filepath.Join(machineTempDir(), "snrun-some-other-run"),
	} {
		if Isolated(outside) {
			t.Errorf("%s counted as isolated", outside)
		}
	}
	if Isolated() {
		t.Error("no paths at all counted as isolated")
	}
	if !Isolated(filepath.Join(Home(), ".option-berth", "option-berth.db")) {
		t.Error("a path under the isolated HOME did not count as isolated")
	}
}

// The leak gate reads `ps` output, so the ownership rule is worth pinning: a
// gate that quietly matched nothing would be worse than no gate.
func TestTheOwnershipRules(t *testing.T) {
	const exe = "/tmp/go-build123/b001/cmd.test"
	// argv[1] is what makes a process a runtime service, and whose binary it is decides
	// whether it is this run's business.
	type processCase struct {
		what    string
		cmdline string
		mine    bool
	}
	cases := []processCase{
		{"runtime service run --detach from this binary", exe + " runtime service run --detach", true},
		{"serve from this binary", exe + " runtime service run", true},
		{"a test run of this binary", exe + " -test.run=TestX", false},
		{"this binary with no argument", exe, false},
		// Another package's test binary is another package's problem: `go test
		// ./...` runs them at the same time, under names that differ only in
		// their build directory.
		{"another package's test binary", "/tmp/go-build123/b002/cmd.test runtime service run --detach", false},
		{"a bare relative path", "cmd.test serve", false},
		{"the developer's own runtime service", "/usr/local/bin/oberth runtime service run", false},
		{"a grep", "grep serve", false},
		{"an empty row", "", false},
	}
	for _, c := range cases {
		if got := isOwnedProcess(exe, c.cmdline, false); got != c.mine {
			t.Errorf("%s: isOwnedProcess(%q) = %v, want %v", c.what, c.cmdline, got, c.mine)
		}
	}
}

// Anything running `runtime service run` from under this run's root is this run's, and nothing
// else is: nothing else put anything there.
func TestThisRunsRootIsOwned(t *testing.T) {
	const notMine = "/some/build/oberth"
	inRoot := filepath.Join(Root(), "option-berth-itest-bin123", "oberth")
	if !isOwnedProcess(notMine, inRoot+" runtime service run", false) {
		t.Errorf("%s runtime service run was not recognised as this run's runtime service", inRoot)
	}
	if isOwnedProcess(notMine, inRoot+" list", false) {
		t.Error("`oberth list` was mistaken for a runtime service")
	}
	if isOwnedProcess(notMine, "/usr/local/bin/oberth runtime service run", false) {
		t.Error("an installed oberth runtime service was mistaken for a test leak")
	}
	// Another run's binary is under the machine's temp directory too, and must
	// not be touched.
	other := filepath.Join(machineTempDir(), "snrun999", "option-berth-itest-bin", "oberth")
	if isOwnedProcess(notMine, other+" runtime service run", false) {
		t.Errorf("%s runtime service run, which belongs to another test run, matched", other)
	}
}

// The opt-in wide net for CI is any `oberth` named `runtime service run` from anywhere under
// the machine's temp directory.
func TestTheOptInWideNetIsMachineWide(t *testing.T) {
	const notMine = "/some/build/oberth"
	inTemp := filepath.Join(machineTempDir(), "option-berth-itest-bin123", "oberth")
	if !isOwnedProcess(notMine, inTemp+" runtime service run", true) {
		t.Errorf("%s runtime service run was not recognised as a test-built runtime service", inTemp)
	}
	if isOwnedProcess(notMine, "/usr/local/bin/oberth runtime service run", true) {
		t.Error("an installed oberth runtime service was mistaken for a test leak")
	}
	if isOwnedProcess(notMine, inTemp+" list", true) {
		t.Error("`oberth list` was mistaken for a runtime service")
	}
	// The wide net still wants a binary called oberth: a random temp binary is
	// someone else's business.
	if isOwnedProcess(notMine, filepath.Join(machineTempDir(), "other-bin", "tool")+" runtime service run", true) {
		t.Error("a non-oberth temp binary was counted as a leak")
	}
}

// `ps -axo command=` joins argv with spaces and quotes nothing, so the
// executable of a `runtime service run` has to be found by the argument boundary rather than
// by splitting on whitespace.
func TestSplitRuntimeServiceRunFindsTheExecutableBoundary(t *testing.T) {
	cases := []struct {
		cmdline   string
		execution string
		ok        bool
	}{
		{"/tmp/x/oberth runtime service run", "/tmp/x/oberth", true},
		{"/tmp/x/oberth runtime service run --detach", "/tmp/x/oberth", true},
		{"/tmp/my dir/oberth runtime service run --detach", "/tmp/my dir/oberth", true},
		// A directory called "serve" is not the argument.
		{"/tmp/a serve/oberth runtime service run", "/tmp/a serve/oberth", true},
		{"/tmp/x/oberth runtime service runall", "", false},
		{"/tmp/x/oberth list", "", false},
		{"/tmp/x/oberth", "", false},
		{"serve", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := splitRuntimeServiceRun(c.cmdline)
		if got != c.execution || ok != c.ok {
			t.Errorf("splitRuntimeServiceRun(%q) = (%q, %v), want (%q, %v)", c.cmdline, got, ok, c.execution, c.ok)
		}
	}
}

func TestSplitPSReadsAPSRow(t *testing.T) {
	pid, cmdline, readable := splitPS("  4242 /path/to/cmd.test runtime service run --detach")
	if !readable {
		t.Fatal("a well-formed ps row did not parse")
	}
	if pid != 4242 || cmdline != "/path/to/cmd.test runtime service run --detach" {
		t.Errorf("splitPS = (%d, %q), want (4242, the command line)", pid, cmdline)
	}
	if _, _, readable := splitPS("not-a-row"); readable {
		t.Error("a malformed ps row parsed as a process")
	}
}

// Nothing in this package starts a runtime service, so the gate has to agree.
func TestNoRuntimeServicesLeakFromThisBinary(t *testing.T) {
	RequireNoLeakedRuntimeServices(t)
}

func executableHome(t *testing.T) string {
	t.Helper()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("resolving the home directory: %v", err)
	}
	return home
}
