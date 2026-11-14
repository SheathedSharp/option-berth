package cmd

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"time"
)

// attentionChanges reuses the existing Runtime Service stream. It only dials:
// absent/older daemons keep the existing bounded poll path, never autostart.
func attentionChanges(ctx context.Context, project string) (runner.Trigger, func()) {
	if noDaemonFlag {
		return runner.Trigger{}, func() {}
	}
	_, root, err := statusProject(project)
	if err != nil {
		return runner.Trigger{}, func() {}
	}
	root = groups.Canonical(root)
	return runner.OpenChanges(ctx, groups.WorktreeTokenFor(root), time.Second, func(ctx context.Context, after uint64) (runner.ChangeStream, error) {
		connectCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		c, err := dialDaemon(connectCtx)
		if err != nil {
			return runner.ChangeStream{}, err
		}
		if err = requireScopedState(c); err != nil {
			c.Close()
			return runner.ChangeStream{}, err
		}
		// Subscribe uses the budget only for its opening reply. The task owns
		// the socket afterwards; cancellation of that budget does not close it.
		sub, err := c.Subscribe(connectCtx, client.SubscribeOptions{Events: true, AfterSeq: after, Scope: &state.Scope{Worktrees: []string{root}}})
		if err != nil {
			c.Close()
			return runner.ChangeStream{}, err
		}
		return runner.ChangeStream{Changes: sub.Changes, Seq: sub.StreamSnapshot.Seq, Identity: fmt.Sprintf("%d/%s", c.Hello().PID, c.Hello().StartedAt), Close: func() { _ = c.Close() }, Dropped: sub.Dropped}, nil
	})
}
