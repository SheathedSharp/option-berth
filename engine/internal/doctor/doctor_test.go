package doctor

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// isolatedHome is a throwaway installation tree. Every path a check might
// read or create lives under t.TempDir(), so a test never inspects the
// operator's real home, ledger or socket.
type isolatedHome struct {
	root      string
	configDir string
}

func newIsolatedHome(t *testing.T) isolatedHome {
	t.Helper()
	root := t.TempDir()
	return isolatedHome{root: root, configDir: filepath.Join(root, ".option-berth")}
}

func (h isolatedHome) installPaths(env *Env) {
	env.Home = h.root
	env.Project = h.root
	env.ConfigDir = h.configDir
	env.ConfigPath = filepath.Join(h.configDir, "config.yaml")
	env.SocketPath = filepath.Join(h.configDir, "runtime-service.sock")
	env.DBPath = filepath.Join(h.configDir, "option-berth.db")
}

// refuseEscapes closes the seams that could leave the temp tree: no PATH
// lookup, no socket dial, no docker probe, no release fetch. A check that
// still needs a file reads one the test wrote under Home.
func refuseEscapes(env *Env) {
	self := filepath.Join(env.Home, "bin", "option-berth")
	env.Executable = func() (string, error) { return self, nil }
	env.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	env.RuntimeService = func(context.Context) RuntimeServiceInfo {
		return RuntimeServiceInfo{Err: errors.New("no runtime service")}
	}
	env.Docker = func(context.Context) (string, error) {
		return "", errors.New("no docker in tests")
	}
	env.ProbeObservation = func(context.Context) ports.ProbeResult {
		return ports.ProbeResult{State: ports.ObservationObservable}
	}
}

// fakeEnv is a CLI-mode Env rooted in an isolatedHome, with every escape
// seam refused.
func fakeEnv(t *testing.T) *Env {
	t.Helper()
	env := &Env{Mode: ModeCLI, Version: "v1.2.3", UID: os.Getuid()}
	env.GOOS = runtime.GOOS
	newIsolatedHome(t).installPaths(env)
	refuseEscapes(env)
	return env
}

// write puts content at path, creating parents.
func write(t *testing.T, path, content string) string {
	t.Helper()
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// run executes one check against an env with the standard defaults installed.
func run(t *testing.T, env *Env, check func(context.Context, *Env) rpc.DoctorCheck) rpc.DoctorCheck {
	t.Helper()
	env.resolve()
	return check(context.Background(), env)
}

func wantStatus(t *testing.T, row rpc.DoctorCheck, status string) {
	t.Helper()
	if row.Status != status {
		t.Fatalf("status = %q (%s / %s), want %q", row.Status, row.Summary, row.Detail, status)
	}
}

func TestResolveFillsTheUserID(t *testing.T) {
	env := *fakeEnv(t)
	env.UID = 0
	env.uidSet = false
	env.resolve()
	if env.UID != os.Getuid() {
		t.Fatalf("UID = %d, want the process uid %d", env.UID, os.Getuid())
	}
}

func TestResolveKeepsExplicitRootUID(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("this test needs a non-root user to tell the sentinel from root")
	}
	env := *fakeEnv(t)
	env.UID = 0
	env.uidSet = true
	env.resolve()
	if env.UID != 0 {
		t.Fatalf("UID = %d, want the explicit 0 (root)", env.UID)
	}
}

// -------------------------------------------------------------- selectors ---

func TestOnlySelectsExactIDs(t *testing.T) {
	if got := len(IDs()); got < 8 {
		t.Fatalf("only %d checks: %v", got, IDs())
	}
	if !newSelection(nil).includes("db_ok") {
		t.Error("an empty --only should select everything")
	}
	one := newSelection([]string{"db_ok"})
	if !one.includes("db_ok") {
		t.Error("--only db_ok should select db_ok")
	}
	if one.includes("docker") {
		t.Error("--only db_ok should not select docker")
	}
	if got := UnknownSelectors([]string{"db_ok", "nope"}); len(got) != 1 || got[0] != "nope" {
		t.Errorf("unknown selectors = %v, want [nope]", got)
	}
	// No check id has a dot any more, so there is no accepted prefix.
	if prefixes := Prefixes(); len(prefixes) != 0 {
		t.Errorf("prefixes = %v, want none: no check id has a dot", prefixes)
	}
}

func TestRunKeepsOnlyTheSelectedChecks(t *testing.T) {
	env := *fakeEnv(t)
	got := Run(context.Background(), env, []string{"db_ok,docker"})
	requireCheckOrder(t, got.Checks, "db_ok", "docker")
	if got.Version != env.Version {
		t.Errorf("report version = %q, want the env version %q", got.Version, env.Version)
	}
}

// requireCheckOrder pins both the length and the exact id sequence. A
// reordering or a silently dropped selector would still be a length-2 list.
func requireCheckOrder(t *testing.T, checks []rpc.DoctorCheck, want ...string) {
	t.Helper()
	got := make([]string, 0, len(checks))
	for _, row := range checks {
		got = append(got, row.ID)
	}
	if len(got) != len(want) {
		t.Fatalf("ran %d checks %v, want %d: %v", len(got), got, len(want), want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("check %d = %q, want %q (full list %v)", i, got[i], id, got)
		}
	}
}

func TestRunDialsTheRuntimeServiceOnce(t *testing.T) {
	env := *fakeEnv(t)
	dials := 0
	env.RuntimeService = func(context.Context) RuntimeServiceInfo {
		dials++
		return RuntimeServiceInfo{Reachable: true, Version: "v9.9.9", ProtocolVersion: rpc.ProtocolVersion}
	}
	result := Run(context.Background(), env, nil)
	if dials != 1 {
		t.Fatalf("dialled the runtime service %d times, want 1", dials)
	}
	if result.RuntimeServiceVersion != "v9.9.9" {
		t.Errorf("runtime service version = %q, want the probed answer", result.RuntimeServiceVersion)
	}
}

func TestRuntimeServiceModeSkipsTheCLIOnlyChecks(t *testing.T) {
	env := *fakeEnv(t)
	env.Mode = ModeRuntimeService
	got := Run(context.Background(), env, nil)

	cliOnly := map[string]bool{
		"cli_on_path":                     true,
		"runtime_service_version_matches": true,
		"runtime_service_build_matches":   true,
	}
	var seen []string
	for _, row := range got.Checks {
		if !cliOnly[row.ID] {
			continue
		}
		seen = append(seen, row.ID)
		wantStatus(t, row, StatusSkip)
		if !strings.Contains(row.Detail, "CLI-only") {
			t.Errorf("%s detail = %q, want it to say the check is CLI-only", row.ID, row.Detail)
		}
	}
	if len(seen) != len(cliOnly) {
		t.Errorf("CLI-only checks present = %v, want each of %v", seen, cliOnlyKeys(cliOnly))
	}
	// The answering process must still emit every registered check,
	// including the ones it skipped.
	requireCompleteRoster(t, got.Checks, IDs())
}

func cliOnlyKeys(set map[string]bool) []string {
	keys := make([]string, 0, len(set))
	for id := range set {
		keys = append(keys, id)
	}
	return keys
}

// requireCompleteRoster reports which ids were dropped or added, not just
// that the counts differ.
func requireCompleteRoster(t *testing.T, checks []rpc.DoctorCheck, roster []string) {
	t.Helper()
	want := make(map[string]bool, len(roster))
	for _, id := range roster {
		want[id] = true
	}
	got := make(map[string]int, len(checks))
	for _, row := range checks {
		got[row.ID]++
	}
	var missing, unexpected []string
	for _, id := range roster {
		if got[id] == 0 {
			missing = append(missing, id)
		}
	}
	for id, n := range got {
		if !want[id] {
			unexpected = append(unexpected, id)
		} else if n != 1 {
			t.Errorf("id %q appeared %d times, want once", id, n)
		}
	}
	if len(missing) != 0 || len(unexpected) != 0 || len(checks) != len(roster) {
		t.Errorf("runtime service mode roster length %d (want %d); missing %v, unexpected %v",
			len(checks), len(roster), missing, unexpected)
	}
}

func TestRunIsNotOKOnlyWhenSomethingFailed(t *testing.T) {
	for _, tc := range []struct {
		check  string
		wantOK bool
	}{
		{"runtime_service_reachable", false},
		{"project_config", true},
	} {
		env := *fakeEnv(t)
		if got := Run(context.Background(), env, []string{tc.check}); got.OK != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.check, got.OK, tc.wantOK)
		}
	}
}
