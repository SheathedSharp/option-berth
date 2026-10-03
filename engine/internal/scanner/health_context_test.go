package scanner

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestConfiguredHealthContextCancelledRoundIsAtomic(t *testing.T) {
	const n = maxHealthProbes * 3
	previous := &state.Health{Status: state.HealthOK, Configured: true, ObservedAt: "previous"}
	rows := make([]state.Port, n)
	gg := []state.Group{{Services: make([]state.Service, n)}}
	path := "/healthz"
	for i := range rows {
		port := 18000 + i
		rows[i] = state.Port{Port: port, BindAddress: "127.0.0.1", Health: previous}
		gg[0].Services[i] = state.Service{Name: fmt.Sprint(i), PortActual: &port, Health: &path, HealthStatus: previous}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan struct{}, n)
	var calls, active atomic.Int32
	probe := func(ctx context.Context, host string, port int, path string, timeout time.Duration) ports.HealthResult {
		calls.Add(1)
		active.Add(1)
		defer active.Add(-1)
		entered <- struct{}{}
		<-ctx.Done()
		return ports.HealthResult{Status: "unhealthy", StatusCode: 503}
	}
	done := make(chan error, 1)
	go func() { done <- ProbeConfiguredContext(ctx, rows, gg, probe, 0) }()
	for range maxHealthProbes {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("first wave did not start")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancel result=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probes did not join")
	}
	if active.Load() != 0 || calls.Load() != maxHealthProbes {
		t.Fatalf("active=%d calls=%d", active.Load(), calls.Load())
	}
	for i := range rows {
		if rows[i].Health != previous || gg[0].Services[i].HealthStatus != previous {
			t.Fatalf("cancelled round changed row %d", i)
		}
	}
}

func TestConfiguredHealthContextDropsCompletedPartialRound(t *testing.T) {
	previous := &state.Health{Status: state.HealthOK, Configured: true, ObservedAt: "previous"}
	p1, p2, path := 18001, 18002, "/healthz"
	rows := []state.Port{{Port: p1, Health: previous}, {Port: p2, Health: previous}}
	gg := []state.Group{{Services: []state.Service{{Name: "first", PortActual: &p1, Health: &path, HealthStatus: previous}, {Name: "second", PortActual: &p2, Health: &path, HealthStatus: previous}}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan struct{})
	probe := func(_ context.Context, _ string, port int, _ string, _ time.Duration) ports.HealthResult {
		if port == p1 {
			close(first)
			return ports.HealthResult{Status: "unhealthy", StatusCode: 500}
		}
		<-first
		cancel()
		return ports.HealthResult{Status: "healthy", StatusCode: 200}
	}
	if err := ProbeConfiguredContext(ctx, rows, gg, probe, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	for i := range rows {
		if rows[i].Health != previous || gg[0].Services[i].HealthStatus != previous {
			t.Fatalf("partial result leaked at %d", i)
		}
	}
}

func TestConfiguredHealthContextPreCancelledDoesNotJoinRows(t *testing.T) {
	previous := &state.Health{Status: state.HealthOK, Configured: true}
	gg := []state.Group{{Services: []state.Service{{Name: "no-port", HealthStatus: previous}}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := ProbeConfiguredContext(ctx, nil, gg, nil, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel=%v", err)
	}
	if gg[0].Services[0].HealthStatus != previous {
		t.Fatal("pre-cancelled round synchronized rows")
	}
}
