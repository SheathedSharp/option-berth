package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// The two budgets are the contract value the loop promises: an observation and
// a gate wait each get fifteen seconds, no more and no less.
func TestBudgetsAreExactlyFifteenSeconds(t *testing.T) {
	if ObservationBudget != 15*time.Second {
		t.Fatalf("ObservationBudget = %s, want 15s", ObservationBudget)
	}
	if ScanLockBudget != 15*time.Second {
		t.Fatalf("ScanLockBudget = %s, want 15s", ScanLockBudget)
	}
}

// A parent deadline exactly ObservationBudget away is reused; one nanosecond
// further out gets this stage its own bounded context. The clock is supplied so
// both sides are decided by the same comparison rather than by how much
// time.Now moved between constructing the deadline and reading it.
func TestObservationBudgetBoundaryAtExactlyTheBudget(t *testing.T) {
	// Anchored in the future on purpose: the contexts are real, and a parent
	// whose deadline has already passed would answer for the wrong reason.
	now := time.Now().Add(time.Hour)
	frozen := func() time.Time { return now }

	exact, stopExact := context.WithDeadline(context.Background(), now.Add(ObservationBudget))
	defer stopExact()
	reused, cancelReused := observationContextAt(frozen, exact)
	if reused != exact {
		t.Fatal("a parent with exactly the budget left was replaced by a new timer")
	}
	cancelReused()

	longer, stopLonger := context.WithDeadline(context.Background(), now.Add(ObservationBudget+time.Nanosecond))
	defer stopLonger()
	ctx, cancel := observationContextAt(frozen, longer)
	if ctx == longer {
		t.Fatal("a parent with more than the budget left kept its longer deadline")
	}
	cancel()
	if longer.Err() != nil {
		t.Fatal("the stage's own budget cancelled its parent's ownership")
	}
}

func TestObservationBudgetReusesEarlierParent(t *testing.T) {
	parent, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	ctx, cancel := observationContext(parent)
	if ctx != parent {
		t.Fatal("earlier deadline replaced")
	}
	cancel()
	if parent.Err() != nil {
		t.Fatal("stage cancelled its parent's ownership")
	}
}

func TestObservationBudgetIsNotRenewedByNestedStages(t *testing.T) {
	ctx, cancel := observationContext(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > ObservationBudget {
		t.Fatal("missing overall budget")
	}
	inner, done := observationContext(ctx)
	defer done()
	if inner != ctx {
		t.Fatal("nested stage created a fresh budget")
	}
}

func TestForcedObservationOwnsAndReleasesItsBudget(t *testing.T) {
	parent, stop := context.WithCancel(context.Background())
	defer stop()
	var seen context.Context
	l := New(Options{ScanContext: func(ctx context.Context, _ Include) ([]ports.ListeningPort, error) {
		seen = ctx
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > ObservationBudget {
			t.Fatal("collector not bounded")
		}
		return nil, nil
	}})
	if _, err := l.RescanContext(parent, Include{}); err != nil {
		t.Fatal(err)
	}
	if seen == nil {
		t.Fatal("collector not called")
	}
	select {
	case <-seen.Done():
	default:
		t.Fatal("observation timer not released")
	}
	if parent.Err() != nil {
		t.Fatal("observation cancelled caller")
	}
}
