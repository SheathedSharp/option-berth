package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

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
