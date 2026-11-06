package killer

import (
	"context"
	"net"
	"os/exec"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
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

// pause is the poll between escalation probes. It reports whether the wait
// completed, so a withdrawn request stops the loop instead of sleeping out the
// rest of its grace period.
func (e *engine) pause(ctx context.Context, d time.Duration) bool {
	return e.clock.Wait(ctx, d)
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
