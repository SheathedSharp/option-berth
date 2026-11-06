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

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// fakeEnv builds an Env whose every seam is either a t.TempDir() or a
// deliberate refusal: no PATH lookup escapes it, no socket is dialled, no
// release is fetched. Paths point into the temp directory, so a check that
// reads a file reads one the test wrote.
func fakeEnv(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	configDir := filepath.Join(home, ".option-berth")
	env := &Env{
		Mode:       ModeCLI,
		GOOS:       runtime.GOOS,
		Version:    "v1.2.3",
		Home:       home,
		Project:    home,
		ConfigPath: filepath.Join(configDir, "config.yaml"),
		ConfigDir:  configDir,
		SocketPath: filepath.Join(configDir, "daemon.sock"),
		DBPath:     filepath.Join(configDir, "option-berth.db"),
		UID:        os.Getuid(),
	}
	env.Executable = func() (string, error) { return filepath.Join(home, "bin", "option-berth"), nil }
	env.LookPath = func(string) (string, error) { return "", exec.ErrNotFound }
	env.Daemon = func(context.Context) DaemonInfo { return DaemonInfo{Err: errors.New("no daemon")} }
	env.Docker = func(context.Context) (string, error) { return "", errors.New("no docker in tests") }
	env.ProbeObservation = func(context.Context) ports.ProbeResult {
		return ports.ProbeResult{State: ports.ObservationObservable}
	}
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
	if len(got.Checks) != 2 {
		t.Fatalf("ran %d checks, want 2: %v", len(got.Checks), got.Checks)
	}
	if got.Checks[0].ID != "db_ok" || got.Checks[1].ID != "docker" {
		t.Errorf("ids = %s, %s", got.Checks[0].ID, got.Checks[1].ID)
	}
	if got.Version != "v1.2.3" {
		t.Errorf("version = %q", got.Version)
	}
}

func TestRunDialsTheDaemonOnce(t *testing.T) {
	env := *fakeEnv(t)
	dials := 0
	env.Daemon = func(context.Context) DaemonInfo {
		dials++
		return DaemonInfo{Reachable: true, Version: "v9.9.9", ProtocolVersion: rpc.ProtocolVersion}
	}
	result := Run(context.Background(), env, nil)
	if dials != 1 {
		t.Fatalf("dialled the daemon %d times, want 1", dials)
	}
	if result.DaemonVersion != "v9.9.9" {
		t.Errorf("daemon version = %q, want the probed answer", result.DaemonVersion)
	}
}

func TestDaemonModeSkipsTheCLIOnlyChecks(t *testing.T) {
	env := *fakeEnv(t)
	env.Mode = ModeDaemon
	got := Run(context.Background(), env, nil)

	cliOnly := map[string]bool{
		"cli_on_path":            true,
		"daemon_version_matches": true,
		"daemon_build_matches":   true,
	}
	seen := 0
	for _, row := range got.Checks {
		if !cliOnly[row.ID] {
			continue
		}
		seen++
		wantStatus(t, row, StatusSkip)
		if !strings.Contains(row.Detail, "CLI-only") {
			t.Errorf("%s detail = %q, want it to say the check is CLI-only", row.ID, row.Detail)
		}
	}
	if seen != len(cliOnly) {
		t.Errorf("saw %d CLI-only checks, want %d", seen, len(cliOnly))
	}
	// The list of rows must not change shape with the answering process.
	if len(got.Checks) != len(IDs()) {
		t.Errorf("daemon mode ran %d checks, want all %d", len(got.Checks), len(IDs()))
	}
}

func TestRunIsNotOKOnlyWhenSomethingFailed(t *testing.T) {
	for _, tc := range []struct {
		check  string
		wantOK bool
	}{
		{"daemon_reachable", false},
		{"project_config", true},
	} {
		env := *fakeEnv(t)
		if got := Run(context.Background(), env, []string{tc.check}); got.OK != tc.wantOK {
			t.Errorf("%s: ok = %v, want %v", tc.check, got.OK, tc.wantOK)
		}
	}
}
