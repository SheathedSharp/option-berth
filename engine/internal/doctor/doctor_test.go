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

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
)

// fakeEnv is an Env whose every seam answers without touching the machine: no
// PATH lookup escapes it, no socket is dialled, no release is fetched. Paths
// point into t.TempDir(), so a check that reads a file reads a real one the
// test wrote.
func fakeEnv(t *testing.T) *Env {
	t.Helper()
	home := t.TempDir()
	return &Env{
		Mode:       ModeCLI,
		GOOS:       runtime.GOOS,
		Version:    "v1.2.3",
		Home:       home,
		Project:    home,
		ConfigPath: filepath.Join(home, ".option-berth", "config.yaml"),
		ConfigDir:  filepath.Join(home, ".option-berth"),
		SocketPath: filepath.Join(home, ".option-berth", "daemon.sock"),
		DBPath:     filepath.Join(home, ".option-berth", "option-berth.db"),
		UID:        os.Getuid(),
		Executable: func() (string, error) { return filepath.Join(home, "bin", "option-berth"), nil },
		LookPath:   func(string) (string, error) { return "", exec.ErrNotFound },
		Daemon:     func(context.Context) DaemonInfo { return DaemonInfo{Err: errors.New("no daemon")} },
		Docker:     func(context.Context) (string, error) { return "", errors.New("no docker in tests") },
	}
}

// write puts content at path, creating parents.
func write(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func run(t *testing.T, env *Env, fn func(context.Context, *Env) rpc.DoctorCheck) rpc.DoctorCheck {
	t.Helper()
	env.fill()
	return fn(context.Background(), env)
}

func wantStatus(t *testing.T, got rpc.DoctorCheck, status string) {
	t.Helper()
	if got.Status != status {
		t.Fatalf("status = %q (%s / %s), want %q", got.Status, got.Summary, got.Detail, status)
	}
}

// -------------------------------------------------------------- selectors ---

func TestOnlySelectsExactIDs(t *testing.T) {
	ids := IDs()
	if len(ids) < 8 {
		t.Fatalf("only %d checks: %v", len(ids), ids)
	}
	if !picked(nil, "db_ok") {
		t.Error("an empty --only should select everything")
	}
	if !picked([]string{"db_ok"}, "db_ok") {
		t.Error("--only db_ok should select db_ok")
	}
	if picked([]string{"db_ok"}, "docker") {
		t.Error("--only db_ok should not select docker")
	}
	if got := UnknownSelectors([]string{"db_ok", "nope"}); len(got) != 1 || got[0] != "nope" {
		t.Errorf("unknown selectors = %v, want [nope]", got)
	}
	// A dotted family was the `mcp_registered.<client>` shape, and there is none
	// left: the checks that had it went with the agent integration
	// (docs/history/scope.md). A prefix that matches nothing is not a selector.
	if got := Prefixes(); len(got) != 0 {
		t.Errorf("prefixes = %v, want none: no check id has a dot any more", got)
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
	calls := 0
	env.Daemon = func(context.Context) DaemonInfo {
		calls++
		return DaemonInfo{Reachable: true, Version: "v1.2.3", ProtocolVersion: rpc.ProtocolVersion}
	}
	Run(context.Background(), env, nil)
	if calls != 1 {
		t.Errorf("dialled the daemon %d times, want 1", calls)
	}
}

func TestDaemonModeSkipsTheCLIOnlyChecks(t *testing.T) {
	env := *fakeEnv(t)
	env.Mode = ModeDaemon
	got := Run(context.Background(), env, nil)

	cliOnly := map[string]bool{"cli_on_path": true, "daemon_version_matches": true}
	seen := 0
	for _, c := range got.Checks {
		if !cliOnly[c.ID] {
			continue
		}
		seen++
		wantStatus(t, c, StatusSkip)
		if !strings.Contains(c.Detail, "CLI-only") {
			t.Errorf("%s detail = %q, want it to say the check is CLI-only", c.ID, c.Detail)
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
	env := *fakeEnv(t)
	// No daemon is a fail.
	if got := Run(context.Background(), env, []string{"daemon_reachable"}); got.OK {
		t.Error("a failing check must make the run not ok")
	}
	// A missing .oberth.yaml is only a warning.
	if got := Run(context.Background(), env, []string{"project_config"}); !got.OK {
		t.Error("a warning must leave the run ok")
	}
}
