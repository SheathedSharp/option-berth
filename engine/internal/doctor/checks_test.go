package doctor

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// --------------------------------------------------------------- the CLI ---

func TestCLIOnPath(t *testing.T) {
	t.Run("resolves to the running binary", func(t *testing.T) {
		env := fakeEnv(t)
		self := write(t, filepath.Join(t.TempDir(), "option-berth"), "#!/bin/sh\n")
		env.Executable = func() (string, error) { return self, nil }
		env.LookPath = func(string) (string, error) { return self, nil }
		got := run(t, env, checkCLIOnPath)
		wantStatus(t, got, StatusOK)
	})

	t.Run("another option-berth shadows this one", func(t *testing.T) {
		env := fakeEnv(t)
		dir := t.TempDir()
		self := write(t, filepath.Join(dir, "mine", "option-berth"), "#!/bin/sh\n")
		other := write(t, filepath.Join(dir, "theirs", "option-berth"), "#!/bin/sh\n")
		env.Executable = func() (string, error) { return self, nil }
		env.LookPath = func(string) (string, error) { return other, nil }

		got := run(t, env, checkCLIOnPath)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Detail, other) || !strings.Contains(got.Detail, self) {
			t.Errorf("detail = %q, want it to name both binaries", got.Detail)
		}
	})

	t.Run("nothing on PATH at all", func(t *testing.T) {
		env := fakeEnv(t)
		got := run(t, env, checkCLIOnPath)
		wantStatus(t, got, StatusFail)
	})
}

// ---------------------------------------------------------------- config ---

func TestConfigParses(t *testing.T) {
	t.Run("no file is a valid state", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkConfigParses), StatusOK)
	})

	t.Run("a broken file fails with a position and a caret", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon: [broken")
		got := run(t, env, checkConfigParses)
		wantStatus(t, got, StatusFail)
		if !got.Fixable {
			t.Error("a broken config is the fix --fix exists for")
		}
		if !strings.Contains(got.Detail, env.ConfigPath+":1:9") {
			t.Errorf("detail = %q, want it to carry <path>:1:7", got.Detail)
		}
		if !strings.Contains(got.Detail, "^") {
			t.Errorf("detail = %q, want a caret under the column", got.Detail)
		}
	})

	t.Run("a valid file parses", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon:\n  log_level: info\n")
		wantStatus(t, run(t, env, checkConfigParses), StatusOK)
	})

	t.Run("a well-formed file with the wrong types only warns", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon: 3\n")
		wantStatus(t, run(t, env, checkConfigParses), StatusWarn)
	})
}

func TestConfigDirWritable(t *testing.T) {
	t.Run("a writable directory", func(t *testing.T) {
		env := fakeEnv(t)
		got := run(t, env, checkConfigDirWritable)
		wantStatus(t, got, StatusOK)
		if _, err := os.Stat(env.ConfigDir); err != nil {
			t.Errorf("the check should have created %s: %v", env.ConfigDir, err)
		}
	})

	t.Run("a read-only directory fails", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Getuid() == 0 {
			t.Skip("file modes do not stop this user from writing")
		}
		env := fakeEnv(t)
		if err := os.MkdirAll(env.ConfigDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(env.ConfigDir, 0o500); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(env.ConfigDir, 0o700) })
		wantStatus(t, run(t, env, checkConfigDirWritable), StatusFail)
	})
}

func TestProjectConfig(t *testing.T) {
	t.Run("absent warns and points at oberth init", func(t *testing.T) {
		env := fakeEnv(t)
		got := run(t, env, checkProjectConfig)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Fix, "oberth init") {
			t.Errorf("fix = %q", got.Fix)
		}
	})

	t.Run("present and valid", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName),
			"name: demo\nservices:\n  - name: api\n    port: 3000\n")
		got := run(t, env, checkProjectConfig)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Summary, "demo") || !strings.Contains(got.Summary, "1 service") {
			t.Errorf("summary = %q, want the group name and the service count", got.Summary)
		}
	})

	t.Run("present and invalid fails", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName), "name: demo\nservices: [broken")
		wantStatus(t, run(t, env, checkProjectConfig), StatusFail)
	})

	t.Run("the old dotfile name still loads, warns and is fixable", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.LegacyConfigName), "name: demo\n")
		got := run(t, env, checkProjectConfig)
		wantStatus(t, got, StatusWarn)
		if !got.Fixable || !strings.Contains(got.Fix, "git mv "+groups.LegacyConfigName+" "+groups.ConfigName) {
			t.Errorf("fix = %q (fixable %v), want the git mv to the new name", got.Fix, got.Fixable)
		}
		if !strings.Contains(got.Summary, "demo") {
			t.Errorf("summary = %q, want the group it still loads", got.Summary)
		}
	})

	t.Run("a shadowed file warns and is not fixable", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName), "name: new\n")
		write(t, filepath.Join(env.Project, groups.LegacyConfigName), "name: old\n")
		got := run(t, env, checkProjectConfig)
		wantStatus(t, got, StatusWarn)
		if got.Fixable {
			t.Error("two files need a person to reconcile them; the check must not be fixable")
		}
		if !strings.Contains(got.Summary, "new") || !strings.Contains(got.Summary, groups.LegacyConfigName) {
			t.Errorf("summary = %q, want the file in use and the ignored one", got.Summary)
		}
	})
}

// ---------------------------------------------------------------- daemon ---

func aliveDaemon() DaemonInfo {
	return DaemonInfo{
		Reachable:       true,
		Version:         "v1.2.3",
		ProtocolVersion: rpc.ProtocolVersion,
		Socket:          "/tmp/option-berth.sock",
		PID:             4242,
	}
}

func TestDaemonReachable(t *testing.T) {
	t.Run("no daemon fails and is fixable", func(t *testing.T) {
		got := run(t, fakeEnv(t), checkDaemonReachable)
		wantStatus(t, got, StatusFail)
		if !got.Fixable {
			t.Error("a stopped daemon is exactly what --fix restarts")
		}
	})

	t.Run("a listening daemon", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		got := run(t, env, checkDaemonReachable)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "4242") {
			t.Errorf("detail = %q, want the pid", got.Detail)
		}
	})

	t.Run("answered from inside the daemon", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			d := aliveDaemon()
			d.Local = true
			return d
		}
		wantStatus(t, run(t, env, checkDaemonReachable), StatusOK)
	})
}

func TestDaemonVersionMatches(t *testing.T) {
	t.Run("no daemon skips", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkDaemonVersionMatches), StatusSkip)
	})

	t.Run("same version", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		wantStatus(t, run(t, env, checkDaemonVersionMatches), StatusOK)
	})

	t.Run("a stale daemon warns and names the restart", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			d := aliveDaemon()
			d.Version = "v0.9.0"
			return d
		}
		got := run(t, env, checkDaemonVersionMatches)
		wantStatus(t, got, StatusWarn)
		if !strings.Contains(got.Summary, "v0.9.0") || !strings.Contains(got.Summary, "v1.2.3") {
			t.Errorf("summary = %q, want both versions", got.Summary)
		}
		if got.Fix != "oberth daemon restart" {
			t.Errorf("fix = %q", got.Fix)
		}
	})
}

func TestDaemonProtocol(t *testing.T) {
	t.Run("matching majors", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		wantStatus(t, run(t, env, checkDaemonProtocol), StatusOK)
	})

	t.Run("a newer minor still matches", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			d := aliveDaemon()
			major, _ := protocolMajor(rpc.ProtocolVersion)
			d.ProtocolVersion = fmt.Sprintf("%d.99.0", major)
			return d
		}
		wantStatus(t, run(t, env, checkDaemonProtocol), StatusOK)
	})

	t.Run("a different major fails", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			d := aliveDaemon()
			major, _ := protocolMajor(rpc.ProtocolVersion)
			d.ProtocolVersion = fmt.Sprintf("%d.0.0", major+1)
			return d
		}
		got := run(t, env, checkDaemonProtocol)
		wantStatus(t, got, StatusFail)
		if got.Fix != "oberth daemon restart" {
			t.Errorf("fix = %q", got.Fix)
		}
	})
}

func TestSocketPermissions(t *testing.T) {
	t.Run("windows is a skip, not a failure", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "windows"
		env.SocketPath = `\\.\pipe\option-berth`
		wantStatus(t, run(t, env, checkSocketPermissions), StatusSkip)
	})

	if runtime.GOOS == "windows" {
		return
	}

	t.Run("no socket and no daemon skips", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkSocketPermissions), StatusSkip)
	})

	t.Run("a daemon whose socket vanished fails", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		wantStatus(t, run(t, env, checkSocketPermissions), StatusFail)
	})

	t.Run("0600 is fine, 0666 is not", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.SocketPath, "")
		if err := os.Chmod(env.SocketPath, 0o600); err != nil {
			t.Fatal(err)
		}
		wantStatus(t, run(t, env, checkSocketPermissions), StatusOK)

		if err := os.Chmod(env.SocketPath, 0o666); err != nil {
			t.Fatal(err)
		}
		got := run(t, env, checkSocketPermissions)
		wantStatus(t, got, StatusFail)
		if !strings.Contains(got.Detail, "0666") {
			t.Errorf("detail = %q, want the mode it found", got.Detail)
		}
	})
}

func TestDBOK(t *testing.T) {
	t.Run("no database yet warns", func(t *testing.T) {
		got := run(t, fakeEnv(t), checkDBOK)
		wantStatus(t, got, StatusWarn)
	})

	t.Run("a real database reports its schema version", func(t *testing.T) {
		env := fakeEnv(t)
		db, err := store.Open(env.DBPath)
		if err != nil {
			t.Fatal(err)
		}
		db.Close()

		got := run(t, env, checkDBOK)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Summary, fmt.Sprintf("v%d", store.LatestVersion())) {
			t.Errorf("summary = %q, want schema v%d", got.Summary, store.LatestVersion())
		}
	})
}

// ------------------------------------------------------------ agent tools ---

func TestDocker(t *testing.T) {
	t.Run("not installed skips", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkDocker), StatusSkip)
	})

	t.Run("installed but not responding warns", func(t *testing.T) {
		env := fakeEnv(t)
		env.LookPath = func(name string) (string, error) {
			if name == "docker" {
				return "/usr/local/bin/docker", nil
			}
			return "", exec.ErrNotFound
		}
		wantStatus(t, run(t, env, checkDocker), StatusWarn)
	})

	t.Run("hanging is reported as not answering, not as not installed", func(t *testing.T) {
		env := fakeEnv(t)
		env.LookPath = func(string) (string, error) { return "/usr/local/bin/docker", nil }
		env.Docker = func(context.Context) (string, error) {
			return "", fmt.Errorf("%w: `docker info` did not answer within 2s", ErrDockerNotAnswering)
		}
		got := run(t, env, checkDocker)
		wantStatus(t, got, StatusWarn)
		if got.Summary != "docker CLI not answering" {
			t.Errorf("summary = %q, want %q", got.Summary, "docker CLI not answering")
		}
	})

	t.Run("responding is ok", func(t *testing.T) {
		env := fakeEnv(t)
		env.LookPath = func(string) (string, error) { return "/usr/local/bin/docker", nil }
		env.Docker = func(context.Context) (string, error) { return "27.0.1", nil }
		got := run(t, env, checkDocker)
		wantStatus(t, got, StatusOK)
		if !strings.Contains(got.Detail, "27.0.1") {
			t.Errorf("detail = %q", got.Detail)
		}
	})
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{{0, "0 B"}, {999, "999 B"}, {2048, "2.0 KiB"}, {5 << 20, "5.0 MiB"}} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
