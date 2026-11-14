//go:build runtime_events

package runtimecheck

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"time"
)

func measurementSubscribe(ctx context.Context, read func(context.Context, string, string) (runner.Status, error), changes <-chan state.StateChanged) (runner.SubscriptionStats, error) {
	trigger, stop := runner.OpenChanges(ctx, "a", time.Millisecond, func(context.Context, uint64) (runner.ChangeStream, error) {
		return runner.ChangeStream{Changes: changes, Identity: "fake"}, nil
	})
	defer stop()
	trigger.Reconcile = 50 * time.Millisecond
	return runner.SubscribeWithStats(ctx, "fake", "/synthetic/a", 5*time.Millisecond, model.Client{}, model.TaskContext{}, read, "measure", nil, func(any) error { return nil }, trigger)
}
