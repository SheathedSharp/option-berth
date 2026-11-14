//go:build !runtime_events

package runtimecheck

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"time"
)

func measurementSubscribe(ctx context.Context, read func(context.Context, string, string) (runner.Status, error), _ <-chan state.StateChanged) (runner.SubscriptionStats, error) {
	return runner.SubscribeWithStats(ctx, "fake", "/synthetic/a", 5*time.Millisecond, model.Client{}, model.TaskContext{}, read, "measure", nil, func(any) error { return nil })
}
