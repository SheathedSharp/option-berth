//go:build integration && !windows

package scenario

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// This is a real CLI, native OS collector and fixture process, not a fake
// scanner or a scan_error-only transport smoke. Run only in an isolated runner.
func TestSystemScanAndScopedCursorReplay(t *testing.T) {
	e := newEnv(t)
	e.serve()
	b, err := RecoveryService(filepath.Join(e.home, "cursor-replay"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.cleanupBuilt(b)
		_, _ = e.run("", "daemon", "stop", "--json")
	})
	var up struct {
		Errors []string `json:"errors"`
	}
	if err := e.runJSON(b.Root, &up, "up", "--json"); err != nil {
		t.Fatal(err)
	}
	if len(up.Errors) != 0 {
		t.Fatalf("up errors: %v", up.Errors)
	}
	e.waitStatus(b.Root, func(s statusDoc) bool { return s.serviceRunning("api", b.Port, true) })

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	dial := func() *client.Client {
		t.Helper()
		c, err := client.Dial(ctx, client.ClientInfo{Name: "cursor-scenario", Socket: e.socket, Keepalive: true})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	c := dial()
	live, before := waitNativeListener(t, ctx, c, b.Port, true, 0)
	scope := &state.Scope{Worktrees: []string{b.Root}}
	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	base := sub.StreamSnapshot
	if base.Seq == 0 || base.Seq < live.Seq || len(base.Worktrees) != 1 {
		t.Fatalf("missing real scoped baseline: %+v", base)
	}
	source := base.Worktrees[0].Source.WorktreeID
	if source == "" || !scopedAPIIsRunning(base) {
		t.Fatalf("baseline lost running api: %+v", base)
	}
	// A genuinely disconnected subscriber must recover changes published while
	// it was away, rather than just opening a fresh snapshot at the latest head.
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(b.Root, "down", "--force"); err != nil {
		t.Fatal(err)
	}
	monitor := dial()
	stopped, after := waitNativeListener(t, ctx, monitor, b.Port, false, before.Scans)
	if stopped.Seq <= base.Seq {
		t.Fatalf("no published stop transition: %d <= %d", stopped.Seq, base.Seq)
	}

	resumed := dial()
	replay, err := resumed.Subscribe(ctx, client.SubscribeOptions{Events: true, Scope: scope, AfterSeq: base.Seq})
	if err != nil {
		t.Fatal(err)
	}
	if replay.StreamSnapshot.Seq != base.Seq || !scopedAPIIsRunning(replay.StreamSnapshot) {
		t.Fatalf("expected historical baseline, not fresh stopped snapshot: %+v", replay.StreamSnapshot)
	}
	change := waitReplayedServiceChange(t, ctx, replay, base.Seq, source)
	if replay.Dropped() {
		t.Fatal("client dropped replay notifications")
	}
	if err := replay.Unsubscribe(ctx); err != nil {
		t.Fatal(err)
	}
	var status rpc.DaemonStatusResult
	if err := monitor.Call(ctx, "daemon.status", rpc.Empty{}, &status); err != nil {
		t.Fatal(err)
	}
	if status.Subscribers != 0 {
		t.Fatalf("subscription leak: %d", status.Subscribers)
	}
	t.Logf("REAL_OS_SCAN_AND_CURSOR_REPLAY platform=%s scans=%d->%d base_seq=%d stopped_seq=%d replay_seq=%d source=%s subscribers=0", runtime.GOOS, before.Scans, after.Scans, base.Seq, stopped.Seq, change.Seq, source)
}

func scopedAPIIsRunning(s state.StreamSnapshot) bool {
	for _, w := range s.Worktrees {
		for _, service := range w.Services {
			if service.Name == "api" && service.Running {
				return true
			}
		}
	}
	return false
}

func waitNativeListener(t *testing.T, ctx context.Context, c *client.Client, port int, present bool, afterScans int64) (rpc.StateSnapshotResult, rpc.DaemonStatusResult) {
	t.Helper()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		var status rpc.DaemonStatusResult
		if err := c.Call(ctx, "daemon.status", rpc.Empty{}, &status); err != nil {
			t.Fatal(err)
		}
		var snap rpc.StateSnapshotResult
		if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, p := range snap.Ports {
			if p.Port == port && p.PID > 0 {
				found = true
			}
		}
		if found == present && snap.Seq > 0 && status.Scans > afterScans && status.Observation != nil && status.Observation.State == "observable" {
			return snap, status
		}
		select {
		case <-ctx.Done():
			t.Fatalf("native collector did not observe listener present=%t port=%d scans=%d seq=%d: %v", present, port, status.Scans, snap.Seq, ctx.Err())
			return snap, status
		case <-ticker.C:
		}
	}
}

func waitReplayedServiceChange(t *testing.T, ctx context.Context, sub *client.Subscription, after uint64, source string) state.StateChanged {
	t.Helper()
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("no real service transition replay after %d: %v", after, ctx.Err())
			return state.StateChanged{}
		case change, ok := <-sub.Changes:
			if !ok {
				t.Fatal("replay stream closed")
				return state.StateChanged{}
			}
			if slices.Contains(change.Changed, "scan_error") {
				t.Fatalf("scan failure is not success: %+v", change)
			}
			if change.Seq > after && change.Source != nil && change.Source.WorktreeID == source && slices.Contains(change.Changed, "service_changed") {
				return change
			}
		}
	}
}
