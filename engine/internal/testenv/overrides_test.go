package testenv

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/paths"
)

// Re-enter TestMain with a caller's overrides, rather than setting them after
// Isolate has already run. The assertions use the real production resolvers.
func TestIsolateRejectsInheritedPathOverrides(t *testing.T) {
	const probeEnv = "BERTH_TESTENV_OVERRIDE_PROBE"
	if os.Getenv(probeEnv) == "1" {
		for _, key := range []string{paths.HomeEnv, paths.LogDirEnv} {
			if value, ok := os.LookupEnv(key); ok {
				t.Errorf("inherited %s survived isolation: %q", key, value)
			}
		}
		RequireIsolated(t, paths.Dir(), paths.ConfigPath(), paths.Runs(),
			paths.Logs(), paths.LogPath(), paths.DB())
		if runtime.GOOS == "windows" {
			if got := paths.Socket(); got != socketPath(sockRoot) {
				t.Fatalf("socket = %q, want this test process's pipe", got)
			}
		} else {
			RequireIsolated(t, paths.Socket())
		}

		// A test's own HOME must still move the default paths. Setting one
		// fixed BERTH_HOME in Isolate would just create a different shared home.
		nextHome := t.TempDir()
		t.Setenv("HOME", nextHome)
		t.Setenv("USERPROFILE", nextHome)
		wantDir := filepath.Join(nextHome, paths.DirName)
		if got := paths.Dir(); got != wantDir {
			t.Fatalf("fixture HOME ignored: Dir() = %q, want %q", got, wantDir)
		}
		if got := paths.Logs(); got != filepath.Join(wantDir, paths.LogsDir) {
			t.Fatalf("fixture HOME ignored for logs: %q", got)
		}

		// Explicit overrides installed by an individual test remain usable;
		// only inherited values are removed at the TestMain boundary.
		ownDir, ownLogs := t.TempDir(), t.TempDir()
		t.Setenv(paths.HomeEnv, ownDir)
		t.Setenv(paths.LogDirEnv, ownLogs)
		if paths.Dir() != ownDir || paths.Logs() != ownLogs {
			t.Fatal("test-local path overrides stopped working")
		}
		return
	}

	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	callerDir := filepath.Join(t.TempDir(), "caller-state")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "-test.run=^TestIsolateRejectsInheritedPathOverrides$")
	cmd.WaitDelay = time.Second
	cmd.Env = append(os.Environ(),
		probeEnv+"=1",
		paths.HomeEnv+"="+callerDir,
		paths.LogDirEnv+"="+filepath.Join(callerDir, "logs"),
		paths.DBEnv+"="+filepath.Join(callerDir, "state.db"),
		paths.SocketEnv+"="+filepath.Join(callerDir, "daemon.sock"),
		"XDG_RUNTIME_DIR="+filepath.Join(callerDir, "runtime"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("isolation subprocess: %v\n%s", err, out)
	}
	if _, err := os.Stat(callerDir); !os.IsNotExist(err) {
		t.Fatalf("isolation touched the caller's state directory: %v", err)
	}
}
