package runsreg

import (
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/store"
)

func TestExitedRunRejectsLateCallbackBeforeIO(t *testing.T) {
	r, old := mirroredRegistry(t)
	next := old
	next.ID = "replacement"
	next.LogPath = "must not read replacement log"
	r.Register(next)
	if e, ok := r.exitedRun(old, 9, false, func(string, int64, int) []string {
		t.Fatal("stale callback read another generation's log")
		return nil
	}); ok {
		t.Fatalf("stale callback recorded %+v", e)
	}
	requireMirroredRun(t, r, next)
}

func TestExitedRunRechecksGenerationAfterLogRead(t *testing.T) {
	for _, mode := range []string{"ID", "timestamp"} {
		t.Run(mode, func(t *testing.T) {
			r, old := mirroredRegistry(t)
			old.LogPath = filepath.Join(t.TempDir(), "old.log")
			r.Register(old)
			db, err := store.Open(filepath.Join(t.TempDir(), "history.db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			r.SetHistoryStore(db)
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan struct{})
			var once sync.Once
			resume := func() { once.Do(func() { close(release) }) }
			var recorded bool
			go func() {
				defer close(done)
				_, recorded = r.exitedRun(old, 17, false, func(string, int64, int) []string {
					close(entered)
					<-release
					return []string{"old generation failure"}
				})
			}()
			t.Cleanup(func() { resume(); awaitMirrorStep(t, done) })
			awaitMirrorStep(t, entered)
			next := old
			if mode == "ID" {
				next.ID = "replacement"
			} else {
				next.StartedAt = old.StartedAt.Add(time.Nanosecond)
			}
			r.Register(next)
			resume()
			awaitMirrorStep(t, done)
			if recorded {
				t.Fatal("old exit was recorded for a replaced run")
			}
			requireMirroredRun(t, r, next)
			rows, err := db.RunExits(10)
			if err != nil || len(rows) != 0 {
				t.Fatalf("durable history = %+v, %v", rows, err)
			}
			if _, ok := r.ExitedRun(next, 0, false); !ok {
				t.Fatal("current run's exit was lost")
			}
			rows, err = db.RunExits(10)
			if err != nil || len(rows) != 1 || rows[0].ID != next.ID || !rows[0].StartedAt.Equal(next.StartedAt) {
				t.Fatalf("current durable exit = %+v, %v", rows, err)
			}
		})
	}
}

func TestExitUsesCurrentStopAndRenameMetadata(t *testing.T) {
	r, old := mirroredRegistry(t)
	old.LogPath = filepath.Join(t.TempDir(), "old.log")
	r.Register(old)
	e, ok := r.exitedRun(old, 1, false, func(string, int64, int) []string {
		r.StoppingWithReason([]int{old.PID}, ReasonReadyTimeout)
		r.RenameGroups(map[string]string{old.Group: "renamed"})
		return []string{"address already in use"}
	})
	if !ok || e.ID != old.ID || e.Group != "renamed" || e.Reason != ReasonReadyTimeout {
		t.Fatalf("exit rolled back current metadata: %+v/%v", e, ok)
	}
	if len(e.LastLines) != 1 || len(readMirror(t)) != 0 {
		t.Fatal("lost log evidence or live mirror")
	}
	if _, ok := r.ExitedRun(old, 1, false); ok {
		t.Fatal("duplicate callback recorded twice")
	}
}
