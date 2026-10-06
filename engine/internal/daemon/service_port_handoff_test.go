package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
)

type queuedReleaseFixture struct {
	*releaseGuardFixture
	wait func(context.Context, func() (int, error)) (int, error)
}

func (g *queuedReleaseFixture) WithNoGroupRunsWait(ctx context.Context, group, path string, mutate func() (int, error)) (int, error) {
	return g.wait(ctx, mutate)
}

func TestStopReleaseQueuesForMirrorHandoffWithinExistingBudget(t *testing.T) {
	rt, plan, guard := releaseFixture(t)
	guard.err = busyReleaseFixture{}
	scans, waited := 0, false
	rt.Scanner = scanner.New(scanner.Options{Runs: rt.RunRegistry, ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
		scans++
		if !claimsMu.TryLock() {
			t.Fatal("fresh observation held claims lock")
		}
		claimsMu.Unlock()
		return nil, nil
	}})
	queued := &queuedReleaseFixture{releaseGuardFixture: guard}
	queued.wait = func(ctx context.Context, mutate func() (int, error)) (int, error) {
		waited = true
		deadline, bounded := ctx.Deadline()
		if !bounded || time.Until(deadline) > 250*time.Millisecond {
			t.Fatal("handoff enlarged the existing release budget")
		}
		if scans != 1 {
			t.Fatalf("handoff lacks one fresh observation: %d", scans)
		}
		return mutate()
	}
	rt.SetRuns(queued)
	value, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, []state.KillResult{{OK: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !waited || guard.calls != 1 {
		t.Fatalf("release still races TryLock instead of queuing: waited=%v tries=%d", waited, guard.calls)
	}
	if result := value.(rpc.KillEnvelope); !result.OK || result.Released != 1 {
		t.Fatal(result)
	}
}

func TestStopReleaseHandoffRetainsChangedReservations(t *testing.T) {
	rt, plan, guard := releaseFixture(t)
	guard.err = busyReleaseFixture{}
	rt.Scanner = scanner.New(scanner.Options{Runs: rt.RunRegistry, ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) { return nil, nil }})
	queued := &queuedReleaseFixture{releaseGuardFixture: guard}
	queued.wait = func(ctx context.Context, mutate func() (int, error)) (int, error) {
		changed := plan.observed.Rows[0]
		changed.ExpiresAt = changed.ExpiresAt.Add(time.Second)
		if err := rt.Store.Claims().Put(changed); err != nil {
			t.Fatal(err)
		}
		return mutate()
	}
	rt.SetRuns(queued)
	if _, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, []state.KillResult{{OK: true}}); err == nil {
		t.Fatal("changed reservation was deleted")
	}
	rows, err := rt.Store.Claims().List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("reservation lost: %v %v", rows, err)
	}
}
