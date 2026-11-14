package doctor

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// mustMkdir and mustChmod keep the fixtures' setup failures out of the
// assertions they precede.
func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

// --------------------------------------------------------------- the CLI ---

func TestCLIOnPath(t *testing.T) {
	t.Run("the running binary is the one PATH finds", func(t *testing.T) {
		env := fakeEnv(t)
		self := write(t, filepath.Join(t.TempDir(), "option-berth"), "#!/bin/sh\n")
		env.Executable = func() (string, error) { return self, nil }
		env.LookPath = func(string) (string, error) { return self, nil }
		wantStatus(t, run(t, env, checkCLIOnPath), StatusOK)
	})

	t.Run("a second install shadows this one", func(t *testing.T) {
		env := fakeEnv(t)
		root := t.TempDir()
		self := write(t, filepath.Join(root, "mine", "option-berth"), "#!/bin/sh\n")
		other := write(t, filepath.Join(root, "theirs", "option-berth"), "#!/bin/sh\n")
		env.Executable = func() (string, error) { return self, nil }
		env.LookPath = func(string) (string, error) { return other, nil }

		row := run(t, env, checkCLIOnPath)
		wantStatus(t, row, StatusWarn)
		for _, binary := range []string{self, other} {
			if !strings.Contains(row.Detail, binary) {
				t.Errorf("detail = %q, want it to name %s", row.Detail, binary)
			}
		}
	})

	t.Run("PATH has no oberth", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkCLIOnPath), StatusFail)
	})
}

// ---------------------------------------------------------------- config ---

func TestConfigParses(t *testing.T) {
	t.Run("a missing file is a valid state", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkConfigParses), StatusOK)
	})

	t.Run("an unreadable path fails", func(t *testing.T) {
		env := fakeEnv(t)
		mustMkdir(t, env.ConfigPath, 0o700)
		wantStatus(t, run(t, env, checkConfigParses), StatusFail)
	})

	t.Run("broken YAML fails with a position and a caret", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon: [broken")
		row := run(t, env, checkConfigParses)
		wantStatus(t, row, StatusFail)
		if !row.Fixable {
			t.Error("a broken config is exactly what --fix exists for")
		}
		if !strings.Contains(row.Detail, env.ConfigPath+":1:9") {
			t.Errorf("detail = %q, want it to carry %s:1:9", row.Detail, env.ConfigPath)
		}
		if !strings.Contains(row.Detail, "^") {
			t.Errorf("detail = %q, want a caret under the column", row.Detail)
		}
	})

	t.Run("valid YAML parses", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon:\n  log_level: info\n")
		wantStatus(t, run(t, env, checkConfigParses), StatusOK)
	})

	t.Run("well-formed YAML of the wrong shape only warns", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, env.ConfigPath, "daemon: 3\n")
		wantStatus(t, run(t, env, checkConfigParses), StatusWarn)
	})
}

func TestConfigDirWritable(t *testing.T) {
	t.Run("a writable directory", func(t *testing.T) {
		env := fakeEnv(t)
		row := run(t, env, checkConfigDirWritable)
		wantStatus(t, row, StatusOK)
		if info, err := os.Stat(env.ConfigDir); err != nil || !info.IsDir() {
			t.Errorf("the check should have created %s as a directory: %v", env.ConfigDir, err)
		}
	})

	t.Run("a read-only directory fails", func(t *testing.T) {
		if runtime.GOOS == "windows" || os.Getuid() == 0 {
			t.Skip("file modes do not stop this user from writing")
		}
		env := fakeEnv(t)
		mustMkdir(t, env.ConfigDir, 0o700)
		mustChmod(t, env.ConfigDir, 0o500)
		t.Cleanup(func() { mustChmod(t, env.ConfigDir, 0o700) })
		wantStatus(t, run(t, env, checkConfigDirWritable), StatusFail)
	})
}

func TestProjectConfig(t *testing.T) {
	t.Run("no project directory skips", func(t *testing.T) {
		env := fakeEnv(t)
		env.Project = ""
		// No resolve(): that would fill Project from the working directory.
		wantStatus(t, checkProjectConfig(context.Background(), env), StatusSkip)
	})

	t.Run("absent warns and points at oberth init", func(t *testing.T) {
		row := run(t, fakeEnv(t), checkProjectConfig)
		wantStatus(t, row, StatusWarn)
		if !strings.Contains(row.Fix, "oberth init") {
			t.Errorf("fix = %q", row.Fix)
		}
	})

	t.Run("present and valid", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName),
			"name: demo\nservices:\n  - name: api\n    port: 3000\n")
		row := run(t, env, checkProjectConfig)
		wantStatus(t, row, StatusOK)
		if !strings.Contains(row.Summary, "demo") || !strings.Contains(row.Summary, "1 service") {
			t.Errorf("summary = %q, want the group name and the service count", row.Summary)
		}
	})

	t.Run("present and invalid fails", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName), "name: demo\nservices: [broken")
		wantStatus(t, run(t, env, checkProjectConfig), StatusFail)
	})

	t.Run("the old dotfile name loads, warns and is fixable", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.LegacyConfigName), "name: demo\n")
		row := run(t, env, checkProjectConfig)
		wantStatus(t, row, StatusWarn)
		if !row.Fixable || !strings.Contains(row.Fix, "oberth doctor --fix --only project_config") {
			t.Errorf("fix = %q (fixable %v), want an explicit filesystem-only repair", row.Fix, row.Fixable)
		}
		if !strings.Contains(row.Summary, "demo") {
			t.Errorf("summary = %q, want the group it still loads", row.Summary)
		}
	})

	t.Run("a shadowed spelling warns and is not fixable", func(t *testing.T) {
		env := fakeEnv(t)
		write(t, filepath.Join(env.Project, groups.ConfigName), "name: new\n")
		write(t, filepath.Join(env.Project, groups.LegacyConfigName), "name: old\n")
		row := run(t, env, checkProjectConfig)
		wantStatus(t, row, StatusWarn)
		if row.Fixable {
			t.Error("two files need a person to reconcile them; the check must not be fixable")
		}
		if !strings.Contains(row.Summary, "new") || !strings.Contains(row.Summary, groups.LegacyConfigName) {
			t.Errorf("summary = %q, want the file in use and the ignored one", row.Summary)
		}
	})
}

func TestProjectConfigFilesFindsNestedManifest(t *testing.T) {
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
	write(t, filepath.Join(root, "apps", "api", groups.ConfigName), "name: nested\n")
	nested := filepath.Join(root, "apps", "api", "src")
	mustMkdir(t, nested, 0o755)

	files := ProjectConfigFiles(nested)
	if len(files) == 0 || filepath.Base(files[0]) != groups.ConfigName {
		t.Fatalf("ProjectConfigFiles(%s) = %v, want the nested manifest", nested, files)
	}
	if !strings.Contains(files[0], filepath.Join("apps", "api")) {
		t.Fatalf("ProjectConfigFiles(%s) = %v, want the manifest under apps/api", nested, files)
	}
}

// ---------------------------------------------------------------- daemon ---

func aliveDaemon() DaemonInfo {
	info := DaemonInfo{Reachable: true, PID: 4242, Socket: "/tmp/option-berth.sock"}
	info.Version = "v1.2.3"
	info.ProtocolVersion = rpc.ProtocolVersion
	return info
}

func TestDaemonReachable(t *testing.T) {
	t.Run("a stopped daemon fails and is fixable", func(t *testing.T) {
		row := run(t, fakeEnv(t), checkDaemonReachable)
		wantStatus(t, row, StatusFail)
		if !row.Fixable {
			t.Error("a stopped daemon is exactly what --fix restarts")
		}
	})

	t.Run("a listening daemon", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		row := run(t, env, checkDaemonReachable)
		wantStatus(t, row, StatusOK)
		if !strings.Contains(row.Detail, "4242") {
			t.Errorf("detail = %q, want the pid", row.Detail)
		}
	})

	t.Run("answered from inside the daemon", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			info := aliveDaemon()
			info.Local = true
			return info
		}
		wantStatus(t, run(t, env, checkDaemonReachable), StatusOK)
	})
}

func TestDaemonVersionMatches(t *testing.T) {
	for _, tc := range []struct {
		name   string
		daemon func() DaemonInfo
		want   string
	}{
		{"no daemon", nil, StatusSkip},
		{"same version", aliveDaemon, StatusOK},
		{"a stale daemon", func() DaemonInfo {
			info := aliveDaemon()
			info.Version = "v0.9.0"
			return info
		}, StatusWarn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnv(t)
			if tc.daemon != nil {
				env.Daemon = func(context.Context) DaemonInfo { return tc.daemon() }
			}
			row := run(t, env, checkDaemonVersionMatches)
			wantStatus(t, row, tc.want)
			if tc.want != StatusWarn {
				return
			}
			if !strings.Contains(row.Summary, "v0.9.0") || !strings.Contains(row.Summary, "v1.2.3") {
				t.Errorf("summary = %q, want both versions", row.Summary)
			}
			if row.Fix != "oberth daemon restart" {
				t.Errorf("fix = %q", row.Fix)
			}
		})
	}
}

func TestDaemonProtocol(t *testing.T) {
	major, _ := protocolMajor(rpc.ProtocolVersion)
	for _, tc := range []struct {
		name     string
		protocol string
		want     string
	}{
		{"matching majors", rpc.ProtocolVersion, StatusOK},
		{"a newer minor still matches", fmt.Sprintf("%d.99.0", major), StatusOK},
		{"a different major fails", fmt.Sprintf("%d.0.0", major+1), StatusFail},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnv(t)
			env.Daemon = func(context.Context) DaemonInfo {
				info := aliveDaemon()
				info.ProtocolVersion = tc.protocol
				return info
			}
			row := run(t, env, checkDaemonProtocol)
			wantStatus(t, row, tc.want)
			if tc.want == StatusFail && row.Fix != "oberth daemon restart" {
				t.Errorf("fix = %q", row.Fix)
			}
		})
	}

	t.Run("an unreadable version fails", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo {
			info := aliveDaemon()
			info.ProtocolVersion = "not-a-version"
			return info
		}
		row := run(t, env, checkDaemonProtocol)
		wantStatus(t, row, StatusFail)
		if !strings.Contains(row.Summary, "unreadable protocol version") {
			t.Errorf("summary = %q, want it to name the unreadable version", row.Summary)
		}
	})
}

func TestSocketPermissions(t *testing.T) {
	t.Run("windows is a skip, not a failure", func(t *testing.T) {
		env := fakeEnv(t)
		env.GOOS = "windows"
		env.SocketPath = `\\.\pipe\option-berth`
		row := run(t, env, checkSocketPermissions)
		wantStatus(t, row, StatusSkip)
		if !strings.Contains(row.Summary, "named pipe") {
			t.Errorf("summary = %q, want it to say the socket is a named pipe", row.Summary)
		}
	})

	if runtime.GOOS == "windows" {
		return
	}

	t.Run("an absent socket is a skip when no daemon answers", func(t *testing.T) {
		row := run(t, fakeEnv(t), checkSocketPermissions)
		wantStatus(t, row, StatusSkip)
		if !strings.Contains(row.Summary, "no socket to check") {
			t.Errorf("summary = %q, want it to say there is no socket to check", row.Summary)
		}
	})

	t.Run("an absent socket fails when the daemon still answers", func(t *testing.T) {
		env := fakeEnv(t)
		env.Daemon = func(context.Context) DaemonInfo { return aliveDaemon() }
		row := run(t, env, checkSocketPermissions)
		wantStatus(t, row, StatusFail)
		if !strings.Contains(row.Summary, "socket is gone") {
			t.Errorf("summary = %q, want it to say the socket is gone", row.Summary)
		}
		if row.Fix != "oberth daemon restart" {
			t.Errorf("fix = %q, want a daemon restart", row.Fix)
		}
	})

	for _, tc := range []struct {
		name   string
		mode   os.FileMode
		want   string
		detail string
	}{
		{"0600 is fine", 0o600, StatusOK, ""},
		{"0666 is not", 0o666, StatusFail, "0666"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnv(t)
			write(t, env.SocketPath, "")
			mustChmod(t, env.SocketPath, tc.mode)
			row := run(t, env, checkSocketPermissions)
			wantStatus(t, row, tc.want)
			if tc.detail != "" && !strings.Contains(row.Detail, tc.detail) {
				t.Errorf("detail = %q, want the mode it found", row.Detail)
			}
		})
	}
}

func TestDBOK(t *testing.T) {
	t.Run("no database yet warns", func(t *testing.T) {
		wantStatus(t, run(t, fakeEnv(t), checkDBOK), StatusWarn)
	})

	t.Run("a real database reports its schema version", func(t *testing.T) {
		env := fakeEnv(t)
		db, openErr := store.Open(env.DBPath)
		if openErr != nil {
			t.Fatalf("open fixture database: %v", openErr)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("close fixture database: %v", err)
		}

		row := run(t, env, checkDBOK)
		wantStatus(t, row, StatusOK)
		if !strings.Contains(row.Summary, fmt.Sprintf("v%d", store.LatestVersion())) {
			t.Errorf("summary = %q, want schema v%d", row.Summary, store.LatestVersion())
		}
	})
}

// ------------------------------------------------------------ agent tools ---

func TestDocker(t *testing.T) {
	onPath := func(name string) (string, error) {
		if name == "docker" {
			return "/usr/local/bin/docker", nil
		}
		return "", exec.ErrNotFound
	}
	for _, tc := range []struct {
		name      string
		installed bool
		probe     func(context.Context) (string, error)
		want      string
		summary   string
		detail    string
	}{
		{"not installed", false, nil, StatusSkip, "", ""},
		{"installed but not responding", true, func(context.Context) (string, error) {
			return "", errors.New("daemon down")
		}, StatusWarn, "", ""},
		{"hanging", true, func(context.Context) (string, error) {
			return "", fmt.Errorf("%w: `docker info` did not answer within 2s", ErrDockerNotAnswering)
		}, StatusWarn, "docker CLI not answering", ""},
		{"responding", true, func(context.Context) (string, error) {
			return "27.0.1", nil
		}, StatusOK, "", "27.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := fakeEnv(t)
			if tc.installed {
				env.LookPath = onPath
				env.Docker = tc.probe
			}
			row := run(t, env, checkDocker)
			wantStatus(t, row, tc.want)
			if tc.summary != "" && row.Summary != tc.summary {
				t.Errorf("summary = %q, want %q", row.Summary, tc.summary)
			}
			if tc.detail != "" && !strings.Contains(row.Detail, tc.detail) {
				t.Errorf("detail = %q, want %s", row.Detail, tc.detail)
			}
		})
	}
}

func TestDockerProbeHelpers(t *testing.T) {
	if version, err := dockerServerVersion([]byte(" 27.0.1\n")); err != nil || version != "27.0.1" {
		t.Errorf("dockerServerVersion = %q, %v; want 27.0.1", version, err)
	}
	if _, err := dockerServerVersion(nil); err == nil {
		t.Error("an empty docker answer must be an error")
	}

	exit := &exec.ExitError{Stderr: []byte("Cannot connect to the Docker daemon\nsecond line\n")}
	if err := dockerCommandError(exit); err == nil || err.Error() != "Cannot connect to the Docker daemon" {
		t.Errorf("dockerCommandError = %v, want the first stderr line", err)
	}

	sentinel := errors.New("boom")
	if err := dockerCommandError(sentinel); !errors.Is(err, sentinel) {
		t.Errorf("dockerCommandError = %v, want the original error", err)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{
		{0, "0 B"},
		{999, "999 B"},
		{2048, "2.0 KiB"},
		{5 << 20, "5.0 MiB"},
	} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Errorf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
