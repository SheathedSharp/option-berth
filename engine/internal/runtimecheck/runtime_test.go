// These tests import production packages, not their TestMain fixtures. They
// never Serve/Run a daemon, signal a PID or invoke an OS/process adapter.
package runtimecheck

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*scanner.Loop, *runsreg.Registry, *atomic.Bool, chan struct{}, chan struct{}) {
	return fixtureWithRegistry(t, func(r *runsreg.Registry) groups.Registry { return r })
}
func fixtureWithRegistry(t *testing.T, wrap func(*runsreg.Registry) groups.Registry) (*scanner.Loop, *runsreg.Registry, *atomic.Bool, chan struct{}, chan struct{}) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "oberth.yaml"), []byte("name: "+name+"\nservices:\n  - name: worker\n    cmd: fake-worker\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	reg := runsreg.New()
	reg.Mirror = false
	reg.Alive = func(int) bool { return true }
	reg.Parents = func() map[int]int { return map[int]int{} }
	reg.StartTime = func(int) (time.Time, bool) { return time.Time{}, false }
	busy := &atomic.Bool{}
	entered, release := make(chan struct{}, 1), make(chan struct{})
	provider := wrap(reg)
	loop := scanner.New(scanner.Options{Runs: func() groups.Registry { return provider }, ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		if busy.Load() {
			select {
			case entered <- struct{}{}:
			default:
			}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		return nil, nil
	}, SampleStats: func([]int) map[int]ports.ProcSample { return nil }})
	for _, name := range []string{"a", "b", "c"} {
		if err := loop.LoadConfig(filepath.Join(root, name, "oberth.yaml")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := loop.SnapshotAllContext(context.Background(), scanner.Include{}); err != nil {
		t.Fatal(err)
	}
	return loop, reg, busy, entered, release
}
func TestRegisteredWorkerProgressWhileObserverBusy(t *testing.T) {
	loop, reg, busy, entered, release := fixture(t)
	busy.Store(true)
	defer close(release)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { defer close(done); _, _ = loop.RescanContext(ctx, scanner.Include{}) }()
	<-entered
	reg.Register(runsreg.Record{ID: "r2", PID: 90210, Group: "a", Name: "worker", StartedAt: time.Now()})
	loop.Invalidate()
	read, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	defer stop()
	snap, err := loop.SnapshotAllContext(read, scanner.Include{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, g := range snap.Groups {
		for _, s := range g.Services {
			if g.Name == "a" && s.Name == "worker" {
				found = s.Running && s.RunID != nil && *s.RunID == "r2"
			}
			if g.Name != "a" && s.Running {
				t.Fatal("cross-worktree run")
			}
		}
	}
	if !found {
		t.Fatal("registered worker waited for OS scan")
	}
	cancel()
	<-done
}

func TestUnchangedRuntimeRefreshDoesNotInventAnObservationFailure(t *testing.T) {
	loop, _, _, _, release := fixture(t)
	defer close(release)
	before := loop.Cached()
	loop.Invalidate()
	after, have, err := loop.RefreshRuntimeContext(context.Background())
	if err != nil || !have || after.Seq != before.Seq || after.Observation != nil {
		t.Fatalf("unchanged registry expired ordinary cache: seq=%d/%d obs=%+v err=%v", before.Seq, after.Seq, after.Observation, err)
	}
}

type stagedRegistry struct {
	*runsreg.Registry
	armed            atomic.Bool
	entered, release chan struct{}
}

func (r *stagedRegistry) ServiceFacts() servicefacts.Snapshot {
	captured := r.Registry.ServiceFacts()
	if r.armed.CompareAndSwap(true, false) {
		close(r.entered)
		<-r.release
	}
	return captured
}
func TestSlowScanCannotRepublishOldRunGeneration(t *testing.T) {
	var staged *stagedRegistry
	loop, reg, _, _, release := fixtureWithRegistry(t, func(r *runsreg.Registry) groups.Registry {
		staged = &stagedRegistry{Registry: r, entered: make(chan struct{}), release: make(chan struct{})}
		return staged
	})
	defer close(release)
	started := time.Now()
	reg.Register(runsreg.Record{ID: "r1", PID: 7010, Group: "a", Name: "worker", StartedAt: started})
	if _, _, err := loop.RefreshRuntimeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	staged.armed.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := loop.RescanContext(ctx, scanner.Include{}); done <- err }()
	select {
	case <-staged.entered:
	case <-ctx.Done():
		t.Fatal("scan never captured original run")
	}
	reg.Unregister(7010)
	reg.Register(runsreg.Record{ID: "r2", PID: 7011, Group: "a", Name: "worker", StartedAt: started.Add(time.Second)})

	read, stop := context.WithTimeout(ctx, 100*time.Millisecond)
	_, have, readErr := loop.RefreshRuntimeContext(read)
	stop()
	close(staged.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if readErr != nil || !have {
		t.Fatalf("runtime progress blocked behind attribution: %v", readErr)
	}

	for _, g := range loop.Cached().Groups {
		for _, row := range g.Services {
			if g.Name == "a" && (row.RunID == nil || *row.RunID != "r2") {
				t.Fatalf("old scan overwrote replacement: %+v", row)
			}
			if g.Name != "a" && row.Running {
				t.Fatal("replacement crossed worktree")
			}
		}
	}
	reg.Unregister(7011)
	stopped, _, err := loop.RefreshRuntimeContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range stopped.Groups {
		for _, row := range g.Services {
			if row.Running {
				t.Fatal("stopped generation stayed active")
			}
		}
	}
}
