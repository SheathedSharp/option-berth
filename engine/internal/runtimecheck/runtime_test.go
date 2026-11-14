// These tests import production packages, not their TestMain fixtures. They
// never Serve/Run a daemon, signal a PID or invoke an OS/process adapter.
package runtimecheck

import (
	"context"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*scanner.Loop, *runsreg.Registry, *atomic.Bool, chan struct{}, chan struct{}) {
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
	loop := scanner.New(scanner.Options{Runs: func() groups.Registry { return reg }, ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
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
