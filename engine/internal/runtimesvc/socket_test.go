package runtimesvc

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestSocketPathResolution walks the resolution order over the whole
// environment matrix, on one host, for both platform branches.
func TestSocketPathResolution(t *testing.T) {
	homeDir := func() (string, error) { return "/home/dev", nil }

	cases := []struct {
		label    string
		platform string
		vars     map[string]string
		want     string
	}{
		{
			label:    "BERTH_RUNTIME_SERVICE_SOCKET outranks everything else",
			platform: "darwin",
			vars:     map[string]string{"BERTH_RUNTIME_SERVICE_SOCKET": "/tmp/x.sock", "XDG_RUNTIME_DIR": "/run/user/1000"},
			want:     "/tmp/x.sock",
		},
		{
			label:    "BERTH_RUNTIME_SERVICE_SOCKET outranks everything on Windows too",
			platform: "windows",
			vars:     map[string]string{"BERTH_RUNTIME_SERVICE_SOCKET": `\\.\pipe\option-berth-test`},
			want:     `\\.\pipe\option-berth-test`,
		},
		{
			label:    "an empty BERTH_RUNTIME_SERVICE_SOCKET counts as unset",
			platform: "linux",
			vars:     map[string]string{"BERTH_RUNTIME_SERVICE_SOCKET": "", "XDG_RUNTIME_DIR": "/run/user/1000"},
			want:     filepath.Join("/run/user/1000", "option-berth", "runtime-service.sock"),
		},
		{
			label:    "Windows falls back to the named pipe",
			platform: "windows",
			vars:     map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want:     WindowsPipe,
		},
		{
			label:    "XDG_RUNTIME_DIR is used when it is set",
			platform: "linux",
			vars:     map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want:     filepath.Join("/run/user/1000", "option-berth", "runtime-service.sock"),
		},
		{
			label:    "otherwise the home config directory",
			platform: "darwin",
			vars:     map[string]string{},
			want:     filepath.Join("/home/dev", ".option-berth", "runtime-service.sock"),
		},
		{
			label:    "an empty XDG_RUNTIME_DIR falls through to home",
			platform: "linux",
			vars:     map[string]string{"XDG_RUNTIME_DIR": ""},
			want:     filepath.Join("/home/dev", ".option-berth", "runtime-service.sock"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			getenv := func(key string) string { return tc.vars[key] }
			got := socketPathFrom(getenv, homeDir, tc.platform)
			if got != tc.want {
				t.Errorf("socketPathFrom() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSocketPathHomeFailure covers the unhappy path, where HOME cannot be
// resolved: the runtime service must not fall back to the filesystem root.
func TestSocketPathHomeFailure(t *testing.T) {
	getenv := func(string) string { return "" }
	noHome := func() (string, error) { return "", errNoHome{} }
	want := filepath.Join(".", ".option-berth", "runtime-service.sock")
	if got := socketPathFrom(getenv, noHome, "linux"); got != want {
		t.Errorf("socketPathFrom() = %q, want %q", got, want)
	}
}

type errNoHome struct{}

func (errNoHome) Error() string { return "no home" }

// TestLockPathFollowsSocket checks that the lock sits beside the socket — so a
// socket on a per-boot tmpfs takes its lock along with it — and that a named
// pipe, which has no directory to sit in, falls back to the config directory.
func TestLockPathFollowsSocket(t *testing.T) {
	// The "beside the socket" branch only exists for unix sockets: on Windows
	// the address is always a named pipe (the default, or a BERTH_RUNTIME_SERVICE_SOCKET
	// pointing at another pipe), which takes the config-dir branch below.
	// Asserting a unix path there would assert nothing but filepath's slash
	// handling.
	if runtime.GOOS != "windows" {
		want := "/run/user/1000/option-berth/runtime-service.lock"
		if got := lockPathFor("/run/user/1000/option-berth/runtime-service.sock"); got != want {
			t.Errorf("lockPathFor(unix socket) = %q, want %q", got, want)
		}
	}
	if got, want := lockPathFor(WindowsPipe), filepath.Join(ConfigDir(), "runtime-service.lock"); got != want {
		t.Errorf("lockPathFor(named pipe) = %q, want %q", got, want)
	}
}
