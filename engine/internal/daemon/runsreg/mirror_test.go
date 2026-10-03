package runsreg

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/runs"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func mirroredRegistry(t *testing.T) (*Registry, Record) {
	t.Helper()
	t.Setenv("BERTH_HOME", t.TempDir())
	// Our PID is live for disk readers, but all Prune judgments are injected.
	// No process is started, killed, or mistaken for a synthetic PID.
	pid := os.Getpid()
	r := testRegistry(pid)
	r.Mirror = true
	rec := r.Register(Record{PID: pid, ID: "old", Group: "project", Name: "worker",
		StartedAt: time.Now().UTC(), Cmd: "worker", Cwd: t.TempDir()})
	return r, rec
}

func readMirror(t *testing.T) map[int]runs.Entry {
	t.Helper()
	data, err := os.ReadFile(runs.Path())
	if err != nil {
		t.Fatal(err)
	}
	var registry runs.Registry
	if err := json.Unmarshal(data, &registry); err != nil {
		t.Fatal(err)
	}
	return registry.Runs
}

func requireMirroredRun(t *testing.T, r *Registry, want Record) {
	t.Helper()
	if got, ok := r.Lookup(want.PID); !ok || got.ID != want.ID || !got.StartedAt.Equal(want.StartedAt) {
		t.Fatalf("memory = (%+v, %v), want %+v", got, ok, want)
	}
	got, ok := readMirror(t)[want.PID]
	if !ok || got.ID != want.ID || got.Group != want.Group || got.Name != want.Name {
		t.Fatalf("mirror = (%+v, %v), want %+v", got, ok, want)
	}
	// Use the legacy reader's parser as a compatibility check too.
	started, err := time.Parse(time.RFC3339, got.StartedAt)
	if err != nil || !started.Equal(want.StartedAt) {
		t.Fatalf("mirror rounded generation time: %q, want %v (%v)", got.StartedAt, want.StartedAt, err)
	}
}

func awaitMirrorStep(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("mirror operation did not finish")
	}
}

// Pause the actual Prune between its snapshot and identity decision. The new
// registration and its disk write finish before the old Prune is resumed.
func TestPrunePreservesReRegistrationDuringJudgment(t *testing.T) {
	for _, mode := range []string{"new start", "same time new ID", "same ID subsecond start"} {
		t.Run(mode, func(t *testing.T) {
			r, old := mirroredRegistry(t)
			entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			resume := func() { once.Do(func() { close(release) }) }
			r.Alive = func(int) bool {
				close(entered)
				<-release
				return false
			}
			go func() { defer close(done); r.Prune() }()
			t.Cleanup(func() { resume(); awaitMirrorStep(t, done) })
			awaitMirrorStep(t, entered)
			newRun := old
			newRun.ID = "new"
			if mode == "new start" {
				newRun.StartedAt = old.StartedAt.Add(time.Second)
			} else if mode == "same ID subsecond start" {
				newRun.ID = old.ID
				newRun.StartedAt = old.StartedAt.Add(time.Nanosecond)
			}
			r.Register(newRun)
			resume()
			awaitMirrorStep(t, done)
			requireMirroredRun(t, r, newRun)
			requireMirrorRecovery(t, newRun)
		})
	}
}

// Replaying a delayed effect is deterministic and does not rely on goroutine
// scheduling to put the stale write after the replacement's completed write.
func TestDelayedMirrorAddDoesNotOverwriteReplacement(t *testing.T) {
	r, old := mirroredRegistry(t)
	next := old
	next.ID, next.Name = "new", "replacement"
	next.StartedAt = old.StartedAt.Add(time.Second)
	r.Register(next)
	r.mirrorAdd(old)
	requireMirroredRun(t, r, next)
}

func TestDelayedMirrorAddDoesNotResurrectUnregisteredRun(t *testing.T) {
	r, old := mirroredRegistry(t)
	if !r.Unregister(old.PID) {
		t.Fatal("run was not registered")
	}
	r.mirrorAdd(old)
	if got, ok := readMirror(t)[old.PID]; ok {
		t.Fatalf("late write resurrected %+v", got)
	}
}

func TestDelayedMirrorAddKeepsRenamedMetadata(t *testing.T) {
	r, old := mirroredRegistry(t)
	if r.RenameGroups(map[string]string{old.Group: "renamed"}) != 1 {
		t.Fatal("run was not renamed")
	}
	r.mirrorAdd(old)
	want := old
	want.Group = "renamed"
	requireMirroredRun(t, r, want)
}

func requireMirrorRecovery(t *testing.T, want Record) {
	t.Helper()
	// Exercise actual disk readers, not a hand-built DirectRuns projection.
	// The PID is this test process; no synthetic OS identity is assumed.
	direct, ok := groups.NewDirectRuns().Run(state.Port{PID: want.PID})
	if !ok || direct.ID != want.ID || direct.Group != want.Group || direct.Name != want.Name {
		t.Fatalf("DirectRuns = (%+v, %v), want %+v", direct, ok, want)
	}
	restarted := New()
	if got, err := restarted.ImportLegacy(); err != nil || got != 1 {
		t.Fatalf("restart imported %d records, error=%v, want 1/nil", got, err)
	}
	requireMirroredRun(t, restarted, want)
}

// Replay the deferred delete after the memory half was already removed and a
// new registration completed. The mutation gate now prevents this ordering
// within one registry; the conditional disk deletion also guards external
// writers, which do not share that gate.
func TestDelayedMirrorRemovePreservesReplacement(t *testing.T) {
	for _, mode := range []string{"new ID same time", "same ID one nanosecond later"} {
		t.Run(mode, func(t *testing.T) {
			r, old := mirroredRegistry(t)
			r.Unregister(old.PID)
			next := old
			if mode == "new ID same time" {
				next.ID = "new"
			} else {
				next.StartedAt = old.StartedAt.Add(time.Nanosecond)
			}
			r.Register(next)
			r.mirrorMu.Lock()
			r.mirrorRemoveLocked(old)
			r.mirrorMu.Unlock()
			requireMirroredRun(t, r, next)
			requireMirrorRecovery(t, next)
		})
	}
}

func TestDelayedMirrorRemovePreservesExternalReplacement(t *testing.T) {
	r, old := mirroredRegistry(t)
	r.Unregister(old.PID)
	next := old
	next.ID = "external"
	if err := runs.Add(mirrorEntry(next)); err != nil {
		t.Fatal(err)
	}
	r.mirrorMu.Lock()
	r.mirrorRemoveLocked(old)
	r.mirrorMu.Unlock()
	requireMirrorRecovery(t, next)
}

func TestMirrorGateLeavesSnapshotReadsAvailable(t *testing.T) {
	r, rec := mirroredRegistry(t)
	r.mirrorMu.Lock()
	defer r.mirrorMu.Unlock()
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Lookup(rec.PID)
		r.ServiceFacts()
	}()
	awaitMirrorStep(t, done)
}

// Concurrent replacement, unregister and rename must leave the final disk
// entry equal to the final memory entry (or absent in both). In particular,
// skipping an intermediate add must not leave its predecessor on disk after
// a newer generation has already been unregistered.
func TestConcurrentMirrorMutationsConverge(t *testing.T) {
	r, first := mirroredRegistry(t)
	begin := make(chan struct{})
	var workers sync.WaitGroup
	for worker := 0; worker < 6; worker++ {
		workers.Add(1)
		go func(worker int) {
			defer workers.Done()
			<-begin
			for step := 0; step < 12; step++ {
				rec := first
				rec.ID = fmt.Sprintf("%d/%d", worker, step)
				rec.StartedAt = first.StartedAt.Add(time.Duration(worker*12+step+1) * time.Nanosecond)
				r.Register(rec)
				if step%2 == 0 {
					r.Unregister(rec.PID)
				} else {
					r.RenameGroups(map[string]string{first.Group: "renamed"})
				}
			}
		}(worker)
	}
	close(begin)
	workers.Wait()
	got, present := r.Lookup(first.PID)
	if present {
		requireMirroredRun(t, r, got)
	} else if disk := readMirror(t); len(disk) != 0 {
		t.Fatalf("unregistered generation left behind: %+v", disk)
	}
}
