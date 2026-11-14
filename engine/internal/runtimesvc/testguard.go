package runtimesvc

import (
	"os"
	"path/filepath"
	"strings"
)

// A Go test binary must never turn into a oberth daemon.
//
// `go test` links each package into an executable named <pkg>.test. Passing
// that executable `serve --detach` does not start a daemon: Go's flag package
// stops parsing at the first non-flag argument, so `serve` and `--detach` are
// ignored and the binary re-runs the whole test suite in the background
// against whatever HOME it inherited. Any test reaching the autostart path
// thus forks a copy of itself on every run, and any test in that copy reaching
// the same path forks again — one afternoon of `go test` left 2,150
// `cmd.test serve --detach` processes behind and exhausted swap that way.
//
// Both ends need the guard, which is why it lives here: the client that would
// spawn the daemon (internal/runtimesvc/client.Autostart) and the `oberth serve`
// that would become one (internal/cmd).

// AllowTestDaemonEnv opts a test binary back in to acting as a daemon. It
// exists so the guard itself can be exercised, and so a future harness that
// really wants a test binary as its daemon can say so out loud. Nothing in the
// tree sets it.
const AllowTestDaemonEnv = "BERTH_ALLOW_TEST_DAEMON"

// IsTestBinary reports whether path names a Go test binary. `go test` always
// names one <pkg>.test (<pkg>.test.exe on Windows), whether it runs it out of
// the build cache or `go test -c` writes it to disk.
func IsTestBinary(path string) bool {
	name := filepath.Base(path)
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".exe")
	return strings.HasSuffix(name, ".test")
}

// TestDaemonAllowed reports whether AllowTestDaemonEnv opts this process in.
func TestDaemonAllowed() bool {
	return EnvEnabled(os.Getenv(AllowTestDaemonEnv))
}

// EnvEnabled reads a boolean environment variable the way a shell does: unset,
// empty, "0", "false" and "no" are off; everything else is on. option-berth's
// environment switches are all opt-in flags, so "set at all" has to mean on.
func EnvEnabled(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "0", "no", "", "false":
		return false
	}
	return true
}
