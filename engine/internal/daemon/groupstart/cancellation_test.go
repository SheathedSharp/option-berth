package groupstart

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

func TestDependencyWaitCancellationReachesScanner(t *testing.T) {
	entered := make(chan struct{})
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- waitFor(ctx, &daemon.Runtime{Scanner: l}, "test", []groups.Service{{Name: "api", Port: 18000}}, &addressBook{ports: map[string]int{"api": 18000}})
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("dependency collector did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait result=%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("dependency wait did not join")
	}
	if l.Status().Seq != 0 || l.Status().LastError != nil {
		t.Fatalf("cancelled wait changed observer: %+v", l.Status())
	}
}

func TestDependencyWaitPreCancelledSkipsCollector(t *testing.T) {
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		t.Error("cancelled wait collected")
		return nil, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := waitFor(ctx, &daemon.Runtime{Scanner: l}, "test", []groups.Service{{Name: "api", Port: 18000}}, &addressBook{ports: map[string]int{"api": 18000}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("wait result=%v", err)
	}
}

// Uses the old public seams too: cancellation inside a synchronous legacy
// collector must not be mistaken for successful dependency readiness.
func TestDependencyWaitCancelledLegacyReadCannotReportReady(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		cancel()
		return []ports.ListeningPort{{Port: 18000, PID: 123}}, nil
	}})
	err := waitFor(ctx, &daemon.Runtime{Scanner: l}, "test", []groups.Service{{Name: "api", Port: 18000}}, &addressBook{ports: map[string]int{"api": 18000}})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled dependency reported ready: %v", err)
	}
}
