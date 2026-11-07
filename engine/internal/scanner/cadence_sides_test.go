package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestASpentHealthBudgetDoesNotProbe(t *testing.T) {
	calls := 0
	round := &healthRound{results: make([]state.Health, 1), probed: make([]bool, 1)}
	recordHealthProbe(context.Background(), func(context.Context, string, int, string, time.Duration) ports.HealthResult {
		calls++
		return ports.HealthResult{Status: "healthy"}
	}, healthTarget{kind: groups.HealthHTTP, host: "127.0.0.1", port: 9}, time.Now().Add(-time.Second), round, 0)
	if calls != 0 || round.probed[0] {
		t.Fatalf("calls=%d probed=%v, a probe with no budget left ran", calls, round.probed[0])
	}
}

func TestAnUnprobedCommandVerdictIsNotWritten(t *testing.T) {
	gg := []state.Group{{Services: []state.Service{{Name: "api"}}}}
	round := &healthRound{probed: []bool{false}, results: []state.Health{{Status: state.HealthOK}}}
	applyCommandHealth(gg, []healthTarget{{kind: groups.HealthCommand, group: 0, service: 0}}, round)
	if gg[0].Services[0].HealthStatus != nil {
		t.Fatal("a command that was not probed was written onto the service")
	}
}

func TestAScanOneNanosecondEarlyWaitsInsteadOfRunning(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	scans := 0
	l := New(Options{
		Now:          func() time.Time { return frozen.Add(MinScanInterval - time.Nanosecond) },
		ScanInterval: MinScanInterval,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return []ports.ListeningPort{{Port: 9, PID: 1}}, nil
		},
	})
	l.mu.Lock()
	l.lastAttemptAt = frozen
	l.lastScanAt = frozen
	l.interval = MinScanInterval
	l.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	stop := l.waitTick(ctx, timer, Include{})
	if stop || scans != 0 {
		t.Fatalf("stop=%v scans=%d, a scan one nanosecond early ran or waited out the whole cadence", stop, scans)
	}
}

func TestAScanThatFindsNewWorkWaitsOutTheCadence(t *testing.T) {
	frozen := time.Date(2026, 11, 7, 12, 0, 0, 0, time.UTC)
	scans := 0
	l := New(Options{
		Now:          func() time.Time { return frozen },
		ScanInterval: MinScanInterval,
		Scan:         func(Include) ([]ports.ListeningPort, error) { return nil, nil },
	})
	l.opts.Scan = func(Include) ([]ports.ListeningPort, error) {
		scans++
		l.mu.Lock()
		l.refreshRequested++
		l.mu.Unlock()
		return []ports.ListeningPort{{Port: 9, PID: 1}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	timer := time.NewTimer(time.Hour)
	defer timer.Stop()
	stop := l.waitTick(ctx, timer, Include{})
	if !stop || scans != 1 {
		t.Fatalf("stop=%v scans=%d, a scan that is still due did not wait out the cadence", stop, scans)
	}
}

func TestRearmingAFiredTimerDropsTheOldTick(t *testing.T) {
	// Go 1.23 sync timers do not keep a fired value. The buffered channel
	// restored by this setting is the case the drain exists for.
	t.Setenv("GODEBUG", "asynctimerchan=1")
	timer := time.NewTimer(0)
	deadline := time.Now().Add(time.Second)
	for len(timer.C) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("a zero timer did not fire")
		}
	}
	armTimer(timer, time.Hour)
	select {
	case <-timer.C:
		t.Fatal("rearming a fired timer kept the old tick")
	case <-time.After(50 * time.Millisecond):
	}
	timer.Stop()
}
