package groupstart

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

// Like the existing dependency tests, these never run in parallel: the
// production test seam is package-wide. Each change is restored in cleanup.
func setDependencyBudget(t *testing.T, budget time.Duration) {
	t.Helper()
	old := dependencyTimeout
	dependencyTimeout = budget
	t.Cleanup(func() { dependencyTimeout = old })
}

func budgetWait(ctx context.Context, l *scanner.Loop) error {
	return waitFor(ctx, &daemon.Runtime{Scanner: l}, "test",
		[]groups.Service{{Name: "api", Port: 18000}},
		&addressBook{ports: map[string]int{"api": 18000}})
}

func requireDependencyTimeout(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "timed out after") || !strings.Contains(err.Error(), "api") {
		t.Fatalf("lost dependency timeout/name: %v", err)
	}
	if startFailureReason(err.Error()) != "dependency_timeout" {
		t.Fatalf("dependency failure classification changed: %v", err)
	}
}

func TestDependencyBudgetReachesObservation(t *testing.T) {
	setDependencyBudget(t, 20*time.Millisecond)
	// A longer parent deadline bounds a broken implementation too. The
	// observer must receive the dependency's deadline, not this guard.
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var child context.Context
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		child = ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	err := budgetWait(parent, l)
	requireDependencyTimeout(t, err)
	if parent.Err() != nil || child == nil || !errors.Is(child.Err(), context.DeadlineExceeded) {
		t.Fatalf("wrong deadline owner: parent=%v child=%v", parent.Err(), child)
	}
	if l.Status().Seq != 0 || l.Status().LastError != nil {
		t.Fatalf("deadline polluted scanner state: %+v", l.Status())
	}
}

func TestDependencyBudgetRejectsLateReady(t *testing.T) {
	setDependencyBudget(t, 10*time.Millisecond)
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		// A legacy synchronous callback is joined, not detached. Its ready
		// result is too late even though the parent itself is still live.
		time.Sleep(25 * time.Millisecond)
		return []ports.ListeningPort{{Port: 18000, PID: 123, Tag: "api", RunGroup: "test", RunID: "budget-fixture", RunRootPID: 123}}, nil
	}})
	err := budgetWait(context.Background(), l)
	requireDependencyTimeout(t, err)
	if l.Status().Seq != 0 || l.Status().LastError != nil {
		t.Fatalf("late ready observation was committed: %+v", l.Status())
	}
}

func TestDependencyBudgetEarlierParentDeadline(t *testing.T) {
	setDependencyBudget(t, time.Second)
	parent, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	want, _ := parent.Deadline()
	var got time.Time
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		got, _ = ctx.Deadline()
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	err := budgetWait(parent, l)
	if !errors.Is(err, context.DeadlineExceeded) || !got.Equal(want) {
		t.Fatalf("parent deadline replaced: error=%v deadline=%v want=%v", err, got, want)
	}
}

func TestDependencyBudgetReleasesContextOnReady(t *testing.T) {
	setDependencyBudget(t, time.Second)
	var child context.Context
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		child = ctx
		return []ports.ListeningPort{{Port: 18000, PID: 123, Tag: "api", RunGroup: "test", RunID: "budget-fixture", RunRootPID: 123}}, nil
	}})
	if err := budgetWait(context.Background(), l); err != nil {
		t.Fatal(err)
	}
	if child == nil || !errors.Is(child.Err(), context.Canceled) {
		t.Fatalf("wait did not own and release a child context: %v", child)
	}
	if l.Status().Seq != 1 {
		t.Fatalf("ready result was not committed: %+v", l.Status())
	}
}

func TestDependencyBudgetZeroSkipsObservation(t *testing.T) {
	setDependencyBudget(t, 0)
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		t.Error("expired budget started a collector")
		return nil, nil
	}})
	requireDependencyTimeout(t, budgetWait(context.Background(), l))
}

func TestDependencyBudgetDoesNotPoisonNextWait(t *testing.T) {
	setDependencyBudget(t, 20*time.Millisecond)
	var calls atomic.Int32
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []ports.ListeningPort{{Port: 18000, PID: 123, Tag: "api", RunGroup: "test", RunID: "budget-fixture", RunRootPID: 123}}, nil
	}})
	// Guard the old implementation without changing the production budget.
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	requireDependencyTimeout(t, budgetWait(parent, l))
	if err := budgetWait(context.Background(), l); err != nil {
		t.Fatalf("independent next wait inherited timeout: %v", err)
	}
	if calls.Load() != 2 || l.Status().Seq != 1 || l.Status().LastError != nil {
		t.Fatalf("calls=%d status=%+v", calls.Load(), l.Status())
	}
}

func TestDependencyBudgetEmptyDependenciesRemainImmediate(t *testing.T) {
	setDependencyBudget(t, 0)
	if err := waitFor(context.Background(), nil, "test", nil, nil); err != nil {
		t.Fatalf("empty wait used runtime or budget: %v", err)
	}
}

func TestDependencyBudgetQueuedWaitLeavesOwnerRunning(t *testing.T) {
	setDependencyBudget(t, 20*time.Millisecond)
	entered, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return []ports.ListeningPort{{Port: 18000, PID: 123, Tag: "api", RunGroup: "test", RunID: "budget-fixture", RunRootPID: 123}}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}})
	owner, stopOwner := context.WithCancel(context.Background())
	ownerDone := make(chan error, 1)
	go func() { _, err := l.RescanContext(owner, scanner.Include{}); ownerDone <- err }()
	joined := false
	defer func() {
		stopOwner()
		if !joined {
			select {
			case <-ownerDone:
			case <-time.After(2 * time.Second):
				t.Error("owner did not join on cleanup")
			}
		}
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("owner did not enter scanner")
	}
	parent, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	requireDependencyTimeout(t, budgetWait(parent, l))
	if owner.Err() != nil || calls.Load() != 1 {
		t.Fatalf("waiter interfered with owner: error=%v calls=%d", owner.Err(), calls.Load())
	}
	select {
	case err := <-ownerDone:
		joined = true
		t.Fatalf("unrelated owner stopped early: %v", err)
	default:
	}
	close(release)
	select {
	case err := <-ownerDone:
		joined = true
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("released owner did not finish")
	}
	if err := budgetWait(context.Background(), l); err != nil || calls.Load() != 1 {
		t.Fatalf("next wait did not reuse owner's valid observation: error=%v calls=%d", err, calls.Load())
	}
}

func TestDependencyBudgetDeadlineIsNotRenewedBetweenPolls(t *testing.T) {
	setDependencyBudget(t, 2*time.Second)
	parent, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var l *scanner.Loop
	var deadlines []time.Time
	var child context.Context
	l = scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Error("observation has no budget")
		}
		deadlines = append(deadlines, deadline)
		child = ctx
		if len(deadlines) == 1 {
			l.Invalidate() // Keep the next poll from reusing this missing snapshot.
			return nil, nil
		}
		return []ports.ListeningPort{{Port: 18000, PID: 123, Tag: "api", RunGroup: "test", RunID: "budget-fixture", RunRootPID: 123}}, nil
	}})
	if err := budgetWait(parent, l); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 2 || !deadlines[0].Equal(deadlines[1]) {
		t.Fatalf("dependency deadline renewed per poll: %v", deadlines)
	}
	parentDeadline, _ := parent.Deadline()
	if !deadlines[0].Before(parentDeadline) || !errors.Is(child.Err(), context.Canceled) || parent.Err() != nil {
		t.Fatalf("wrong budget lifetime: deadlines=%v child=%v parent=%v", deadlines, child.Err(), parent.Err())
	}
}
