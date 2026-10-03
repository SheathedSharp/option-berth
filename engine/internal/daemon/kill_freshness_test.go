package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

func TestPortsKillRejectsFailedFreshScanWithWarmCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var failed atomic.Bool
	h := newHarness(t, ctx, func(o *scanner.Options) {
		o.Scan = func(scanner.Include) ([]ports.ListeningPort, error) {
			if failed.Load() {
				return nil, errors.New("collector unavailable")
			}
			return []ports.ListeningPort{{Port: 4323, BindAddress: "127.0.0.1", PID: 424243, Process: "fixture"}}, nil
		}
	})
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatal(err)
	}
	failed.Store(true)
	c := h.dial(ctx)
	// DryRun makes a regression safe: even the broken implementation must
	// not send a signal to a real process on the test host.
	err := c.call("ports.kill", rpc.PortsKillParams{Targets: []rpc.Selector{{Port: ptr(4323)}}, DryRun: true}, nil)
	if err == nil || err.Code != rpc.CodeInternal {
		t.Fatalf("kill accepted stale selectors after a failed refresh: %v", err)
	}
}
