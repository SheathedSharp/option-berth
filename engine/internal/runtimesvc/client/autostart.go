package client

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
)

// pollInterval is how often WaitForSocket retries the dial.
const pollInterval = 25 * time.Millisecond

// NoAutostartEnv disables autostart for a whole process tree. The test harness
// and CI set it so a build never leaves a runtime service behind on the machine.
const NoAutostartEnv = "BERTH_RUNTIME_SERVICE_NO_AUTOSTART"

// Autostart spawns `oberth runtime service run --detach` and waits up to AutostartTimeout
// for the socket to accept connections. binary defaults to the running
// executable, so a client starts the runtime service it was built with.
//
// It refuses in two cases, both meaning "this is not an oberth install": when
// NoAutostartEnv is set, and when the executable is a Go test binary (spawning
// it would re-run the test suite in the background rather than start a
// runtime service).
func Autostart(ctx context.Context, binary, socket string) error {
	if reason := disabledReason(); reason != nil {
		return reason
	}
	executable, err := resolveBinary(binary)
	if err != nil {
		return err
	}
	if err := refuseTestBinary(executable); err != nil {
		return err
	}

	output, err := spawnDetached(executable, socket)
	if err != nil {
		return fmt.Errorf("%w: starting `%s runtime service run --detach` failed: %v: %s",
			ErrNotRunning, filepath.Base(executable), err, clip(output))
	}
	return WaitForSocket(ctx, socket, AutostartTimeout)
}

func disabledReason() error {
	value := os.Getenv(NoAutostartEnv)
	if !runtimesvc.EnvEnabled(value) {
		return nil
	}
	return fmt.Errorf("%w: autostart is disabled by %s=%s", ErrNotRunning, NoAutostartEnv, value)
}

func refuseTestBinary(executable string) error {
	if !runtimesvc.IsTestBinary(executable) || runtimesvc.TestRuntimeServiceAllowed() {
		return nil
	}
	return fmt.Errorf("%w: refusing to autostart %s: it is a Go test binary, and `%s runtime service run --detach` re-runs the whole test suite in the background instead of starting a runtime service\nhint: a test needs an in-process runtime service, or %s=1 to override",
		ErrNotRunning, filepath.Base(executable), filepath.Base(executable), runtimesvc.AllowTestRuntimeServiceEnv)
}

func spawnDetached(executable, socket string) ([]byte, error) {
	command := exec.Command(executable, "runtime", "service", "run", "--detach")
	command.Env = os.Environ()
	if socket != "" {
		command.Env = append(command.Env, "BERTH_RUNTIME_SERVICE_SOCKET="+socket)
	}
	// `runtime service run --detach` forks the real runtime service and exits, so this wait is short.
	return command.CombinedOutput()
}

// WaitForSocket blocks until the runtime service accepts connections at socket or the
// timeout expires.
func WaitForSocket(ctx context.Context, socket string, timeout time.Duration) error {
	if socket == "" {
		socket = runtimesvc.SocketPath()
	}
	limit := time.Now().Add(timeout)
	retry := time.NewTicker(pollInterval)
	defer retry.Stop()
	for !runtimesvc.SocketAlive(socket) {
		if time.Now().After(limit) {
			return fmt.Errorf("%w: socket %s did not accept connections within %s",
				ErrNotRunning, socket, timeout)
		}
		// Wait out one retry interval, or return early on cancellation.
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-retry.C:
		}
	}
	return nil
}

// resolveBinary chooses what to spawn: the override, then the running
// executable, then `oberth` on PATH.
func resolveBinary(binary string) (string, error) {
	if binary != "" {
		return binary, nil
	}
	if running, err := os.Executable(); err == nil && running != "" {
		return running, nil
	}
	found, err := exec.LookPath("oberth")
	if err != nil {
		return "", fmt.Errorf("%w: cannot find the oberth binary to start it", ErrNotRunning)
	}
	return found, nil
}

// clip bounds the command output quoted in an error.
func clip(output []byte) string {
	const limit = 400
	if len(output) > limit {
		output = output[:limit]
	}
	return string(output)
}
