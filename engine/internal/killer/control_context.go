package killer

import (
	"context"
	"net"
	"os/exec"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

func controlCommand(ctx context.Context, name string, args ...string) (*exec.Cmd, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, ports.CommandTimeout)
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = time.Second
	return cmd, cancel
}

func probePortContext(ctx context.Context, port int, bind string) portState {
	return probeWith(func(network, address string, timeout time.Duration) (net.Conn, error) {
		d := net.Dialer{Timeout: timeout}
		return d.DialContext(ctx, network, address)
	}, port, bind)
}

func (realClock) SleepContext(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (e *engine) pause(ctx context.Context, d time.Duration) bool {
	if c, ok := e.clock.(interface {
		SleepContext(context.Context, time.Duration) bool
	}); ok {
		return c.SleepContext(ctx, d)
	}
	// Legacy injected clocks are synchronous and must return.
	e.clock.Sleep(d)
	return ctx.Err() == nil
}

// Saturate a caller's large grace rather than wrapping into an expired budget.
func controlBudget(opts Options) time.Duration {
	const work = 15 * time.Second
	const largest = time.Duration(1<<63 - 1)
	if !opts.escalating() {
		return work
	}
	grace := opts.grace()
	if grace > largest-work {
		return largest
	}
	return work + grace
}
