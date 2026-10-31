package groupstart

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// waitingScanner is a collector that never sees the port and stays until the
// dependency budget cancels it.
func waitingScanner() *scanner.Loop {
	return scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}})
}

// A dependency that declares ready_timeout is waited for on its own budget,
// not on the package default. The seam is deliberately larger here, so a
// missing per-service budget would time out a full second.
func TestDependencyBudgetUsesDependencyReadyTimeout(t *testing.T) {
	setDependencyBudget(t, time.Second)
	l := waitingScanner()
	start := time.Now()
	err := waitFor(context.Background(), &daemon.Runtime{Scanner: l}, "test",
		[]groups.Service{{Name: "api", Port: 18000, ReadyTimeout: "20ms"}},
		&addressBook{ports: map[string]int{"api": 18000}}, "")
	requireDependencyTimeout(t, err)
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("waited %s, want the dependency's 20ms budget", elapsed)
	}
	if !strings.Contains(err.Error(), "20ms") {
		t.Fatalf("message %q does not name the dependency's budget", err)
	}
}

// The dependency's own budget also wins when it is larger than the seam: it is
// the dependency's declaration that decides, not the waiter's default.
func TestDependencyBudgetExtendsPastPackageDefault(t *testing.T) {
	setDependencyBudget(t, 10*time.Millisecond)
	l := waitingScanner()
	start := time.Now()
	err := waitFor(context.Background(), &daemon.Runtime{Scanner: l}, "test",
		[]groups.Service{{Name: "api", Port: 18000, ReadyTimeout: "80ms"}},
		&addressBook{ports: map[string]int{"api": 18000}}, "")
	requireDependencyTimeout(t, err)
	if elapsed := time.Since(start); elapsed < 60*time.Millisecond {
		t.Fatalf("waited %s, want the dependency's 80ms budget", elapsed)
	}
	if !strings.Contains(err.Error(), "80ms") {
		t.Fatalf("message %q does not name the dependency's budget", err)
	}
}

// With several dependencies the wait gets the largest budget any of them
// declared, never the first one's and never the sum.
func TestDependencyBudgetTakesMaximumAcrossDependencies(t *testing.T) {
	setDependencyBudget(t, time.Second)
	l := waitingScanner()
	deps := []groups.Service{
		{Name: "db", Port: 18001, ReadyTimeout: "10ms"},
		{Name: "api", Port: 18002, ReadyTimeout: "70ms"},
	}
	err := waitFor(context.Background(), &daemon.Runtime{Scanner: l}, "test", deps,
		&addressBook{ports: map[string]int{"db": 18001, "api": 18002}}, "")
	requireDependencyTimeout(t, err)
	if !strings.Contains(err.Error(), "70ms") {
		t.Fatalf("message %q does not use the largest dependency budget", err)
	}
}
