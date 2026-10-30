package ports

import (
	"context"
	"errors"
	"net"
	"os/exec"
	"strconv"
	"time"
)

// ProbeTCPContext opens one TCP connection to host:port. It is the `health:
// {tcp: ...}` check: success means the socket accepts a connection, not that
// the protocol behind it is ready. Owner cancellation returns no observation;
// the probe's own timeout is a `timeout` verdict.
func ProbeTCPContext(ctx context.Context, host string, port int, timeout time.Duration) HealthResult {
	if ctx.Err() != nil {
		return HealthResult{}
	}
	host = HostForBind(host)
	start := time.Now()
	observedAt := start.UTC()
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	latency := time.Since(start)
	if ctx.Err() != nil {
		return HealthResult{}
	}
	if err == nil {
		_ = conn.Close()
		return HealthResult{Status: "healthy", Latency: latency, ObservedAt: observedAt}
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return HealthResult{Status: "timeout", Latency: latency, ObservedAt: observedAt}
	}
	if isConnectionRefused(err) {
		return HealthResult{Status: "refused", Latency: latency, ObservedAt: observedAt}
	}
	return HealthResult{Status: "tcp-error", Latency: latency, ObservedAt: observedAt}
}

// ProbeCommandContext runs argv to completion in dir and treats exit status 0
// as healthy. It is the `health: {command: ...}` check, for readiness that a
// listener alone cannot prove (a database's port opens before it answers
// queries). The command is bounded by timeout and is never run through a
// shell: callers split its manifest line the same way cmd and prepare are
// split, so a check that needs shell syntax says `sh -c ...` explicitly.
//
// Owner cancellation returns no observation. A timeout or a non-zero exit is a
// failure with the reason and exit code preserved.
func ProbeCommandContext(ctx context.Context, argv []string, dir string, env []string, timeout time.Duration) HealthResult {
	if ctx.Err() != nil {
		return HealthResult{}
	}
	start := time.Now()
	observedAt := start.UTC()
	if len(argv) == 0 {
		return HealthResult{Status: "command-empty", ObservedAt: observedAt}
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = env
	cmd.WaitDelay = commandWaitDelay
	err := cmd.Run()
	latency := time.Since(start)
	if ctx.Err() != nil {
		return HealthResult{}
	}
	if err == nil {
		return HealthResult{Status: "healthy", Latency: latency, ObservedAt: observedAt}
	}
	if errors.Is(cctx.Err(), context.DeadlineExceeded) {
		return HealthResult{Status: "timeout", Latency: latency, ObservedAt: observedAt}
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return HealthResult{Status: "command-failed", StatusCode: exitErr.ExitCode(), Latency: latency, ObservedAt: observedAt}
	}
	return HealthResult{Status: "command-error", Latency: latency, ObservedAt: observedAt}
}
