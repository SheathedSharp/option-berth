package runtimesvc

import (
	"os"
	"path/filepath"
	"strings"
)

// A Go test binary must never turn into a oberth runtime service.
//
// `go test` links each package into an executable named <pkg>.test. Passing
// that executable `runtime service run --detach` does not start a runtime_service: Go's flag package
// stops parsing at the first non-flag argument, so `runtime service run` and `--detach` are
// ignored and the binary re-runs the whole test suite in the background
// against whatever HOME it inherited. Any test reaching the autostart path
// thus forks a copy of itself on every run, and any test in that copy reaching
// the same path forks again — one afternoon of `go test` left 2,150
// `cmd.test serve --detach` processes behind and exhausted swap that way.
//
// Both ends need the guard, which is why it lives here: the client that would
// spawn the runtime service (internal/runtimesvc/client.Autostart) and the `oberth runtime service run`
// that would become one (internal/cmd).

// AllowTestRuntimeServiceEnv opts a test binary back in to acting as a runtime.service. It
// exists so the guard itself can be exercised, and so a future harness that
// really wants a test binary as its runtime service can say so out loud. Nothing in the
// tree sets it.
const AllowTestRuntimeServiceEnv = "BERTH_RUNTIME_SERVICE_ALLOW_TEST_BINARY"

// IsTestBinary reports whether path names a Go test binary. `go test` always
// names one <pkg>.test (<pkg>.test.exe on Windows), whether it runs it out of
// the build cache or `go test -c` writes it to disk.
func IsTestBinary(path string) bool {
	name := filepath.Base(path)
	name = strings.ToLower(name)
	name = strings.TrimSuffix(name, ".exe")
	return strings.HasSuffix(name, ".test")
}

// TestRuntimeServiceAllowed reports whether AllowTestRuntimeServiceEnv opts this process in.
func TestRuntimeServiceAllowed() bool {
	return EnvEnabled(os.Getenv(AllowTestRuntimeServiceEnv))
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
