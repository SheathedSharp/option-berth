package paths

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// The layout is something other people write down: the macOS client hardcodes
// the socket, the docs quote the directory, an agent's hook exports the session
// variables, a shell script greps the log. So it is pinned here — move the
// directory and exactly one test goes red, instead of three downstream things
// quietly pointing at the old place.

func homeOf(dir string) func() (string, error) {
	return func() (string, error) { return dir, nil }
}

func envOf(pairs map[string]string) func(string) string {
	return func(key string) string { return pairs[key] }
}

func TestEverythingIsInTheOneDirectory(t *testing.T) {
	temp := t.TempDir()
	t.Setenv(HomeEnv, temp)
	// This tests the default layout, not overrides from the invoking shell.
	// The override precedence is covered independently below.
	for _, key := range []string{SocketEnv, DBEnv, LogDirEnv, "XDG_RUNTIME_DIR"} {
		t.Setenv(key, "")
	}

	// 一个目录装全部：配置、数据库、socket、锁、日志、profile、凭据、安装号。
	// 这正是这个包存在的理由 —— 它们以前被八个地方各拼一遍。
	for _, tc := range []struct {
		name string
		got  string
	}{
		{"config", ConfigPath()},
		{"Jev settings", JevConfigPath()},
		{"legacy Jev key", LegacyJevKeyPath()},
		{"database", DB()},
		{"socket", Socket()},
		{"log file", LogPath()},
		{"logs", Logs()},
		{"profiles", Profiles()},
		{"runs", Runs()},
		{"credentials", CredentialsPath()},
		{"install id", InstallIDPath()},
	} {
		if tc.name == "socket" && runtime.GOOS == "windows" {
			if tc.got != WindowsPipe {
				t.Errorf("socket = %q, want %q", tc.got, WindowsPipe)
			}
			continue // named pipes do not live inside a filesystem directory
		}
		if !strings.HasPrefix(tc.got, temp+string(filepath.Separator)) {
			t.Errorf("%s = %q, want it under %s", tc.name, tc.got, temp)
		}
	}
}

func TestDirPrefersTheEnvThenHome(t *testing.T) {
	if got := dirFrom(envOf(map[string]string{HomeEnv: "/custom/berth"}), homeOf("/home/dev")); got != "/custom/berth" {
		t.Errorf("with %s set: %q", HomeEnv, got)
	}
	if got := dirFrom(envOf(map[string]string{HomeEnv: "  "}), homeOf("/home/dev")); got != "/home/dev/"+DirName {
		t.Errorf("blank override should not win: %q", got)
	}
	if got := dirFrom(envOf(nil), homeOf("/home/dev")); got != "/home/dev/"+DirName {
		t.Errorf("default: %q", got)
	}
	// HOME unset: the daemon should start in the working directory, never at /.
	failing := func() (string, error) { return "", os.ErrNotExist }
	if got := dirFrom(envOf(nil), failing); got != DirName {
		t.Errorf("no home: %q, want %q", got, DirName)
	}
}

func TestSocketResolutionOrder(t *testing.T) {
	home := homeOf("/home/dev")

	if got := SocketFrom(envOf(map[string]string{SocketEnv: "/tmp/x.sock"}), home, "linux"); got != "/tmp/x.sock" {
		t.Errorf("%s should win: %q", SocketEnv, got)
	}
	if got := SocketFrom(envOf(nil), home, "windows"); got != WindowsPipe {
		t.Errorf("windows: %q", got)
	}
	// $XDG_RUNTIME_DIR is honoured for the socket alone: on Linux that is what it
	// is for, and it buys per-boot cleanup.
	runtimeEnv := envOf(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"})
	if got := SocketFrom(runtimeEnv, home, "linux"); got != "/run/user/1000/option-berth/"+SocketFile {
		t.Errorf("runtime dir: %q", got)
	}
	if got := SocketFrom(envOf(nil), home, "darwin"); got != "/home/dev/"+DirName+"/"+SocketFile {
		t.Errorf("default: %q", got)
	}
	// The socket override must not leak into the default lookup, or the
	// ownership rule below would call /tmp ours.
	withOverride := envOf(map[string]string{SocketEnv: "/tmp/x.sock"})
	if got := SocketFrom(without(withOverride, SocketEnv), home, "linux"); got != "/home/dev/"+DirName+"/"+SocketFile {
		t.Errorf("hidden override: %q", got)
	}
}

func TestTheTwoOverridesThatPointOneFileElsewhere(t *testing.T) {
	t.Setenv(HomeEnv, t.TempDir())

	t.Setenv(DBEnv, "/elsewhere/option-berth.db")
	if got := DB(); got != "/elsewhere/option-berth.db" {
		t.Errorf("%s: %q", DBEnv, got)
	}

	t.Setenv(LogDirEnv, "/elsewhere/logs")
	if got := Logs(); got != "/elsewhere/logs" {
		t.Errorf("%s: %q", LogDirEnv, got)
	}
}

func TestLockSitsBesideTheSocket(t *testing.T) {
	// $BERTH_HOME names the directory itself, the way $CARGO_HOME does — not the
	// user's home. So the expectation is written off that, not off a home.
	dir := filepath.Join("/home/dev", DirName)
	t.Setenv(HomeEnv, dir)

	if got := Lock(filepath.Join(dir, SocketFile)); got != filepath.Join(dir, LockFile) {
		t.Errorf("beside the socket: %q", got)
	}
	// A named pipe has no directory of its own, so its lock falls back to Dir.
	if got := Lock(WindowsPipe); got != filepath.Join(dir, LockFile) {
		t.Errorf("named pipe: %q", got)
	}
}

func TestOwnsSocketDirOnlyForOurOwnDirectory(t *testing.T) {
	home := homeOf("/home/dev")
	env := envOf(map[string]string{SocketEnv: "/tmp/x.sock"})

	if OwnsSocketDirFrom(env, home, "linux", "/tmp/x.sock") {
		t.Error("/tmp counted as a directory option-berth owns")
	}
	if !OwnsSocketDirFrom(env, home, "linux", "/home/dev/"+DirName+"/"+SocketFile) {
		t.Error("our own directory did not count as ours")
	}
	if !OwnsSocketDirFrom(envOf(map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}), home, "linux",
		"/run/user/1000/option-berth/"+SocketFile) {
		t.Error("the runtime directory did not count as ours")
	}
}
