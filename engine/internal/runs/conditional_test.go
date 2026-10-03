package runs

import (
	"os"
	"testing"
	"time"
)

func TestRemoveIfMatchComparesGenerationNotLabels(t *testing.T) {
	expected := Entry{PID: os.Getpid(), ID: "run", StartedAt: "2026-10-02T08:00:00.123456789Z"}
	for _, tt := range []struct {
		name    string
		id      string
		started string
		remove  bool
	}{
		{"same generation", expected.ID, expected.StartedAt, true},
		{"same time different ID", "replacement", expected.StartedAt, false},
		{"same ID one nanosecond later", expected.ID, "2026-10-02T08:00:00.123456790Z", false},
		{"equivalent timezone", expected.ID, "2026-10-02T17:00:00.123456789+09:00", true},
		{"truncated time is not equal", expected.ID, "2026-10-02T08:00:00Z", false},
		{"unknown time", expected.ID, "", false},
		{"malformed time", expected.ID, "not a time", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BERTH_HOME", t.TempDir())
			current := expected
			current.ID, current.StartedAt = tt.id, tt.started
			current.Group, current.Name = "renamed", "worker"
			// Save exactly the fixture, without Add supplying an omitted time.
			if err := save(&Registry{Runs: map[int]Entry{current.PID: current}}); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(Path())
			if err != nil {
				t.Fatal(err)
			}
			removed, err := RemoveIfMatch(expected)
			if err != nil || removed != tt.remove {
				t.Fatalf("RemoveIfMatch = (%v, %v), want %v", removed, err, tt.remove)
			}
			got, present := load().Runs[current.PID]
			if present == tt.remove || (present && got != current) {
				t.Fatalf("mirror changed incorrectly: (%+v, %v)", got, present)
			}
			if !tt.remove {
				after, err := os.Stat(Path())
				if err != nil || !os.SameFile(before, after) {
					t.Fatalf("no-op rewrote registry: %v", err)
				}
			}
		})
	}
}

func TestRemoveIfMatchLegacyAndUnknownIdentity(t *testing.T) {
	for _, tt := range []struct {
		name, id, started string
		remove            bool
	}{
		{"legacy time without ID", "", "2026-10-02T08:00:00Z", true},
		{"legacy ID without time", "legacy", "", true},
		{"no identity is not proof", "", "", false},
		{"invalid time without ID", "", "bad time", false},
		{"zero time without ID", "", "0001-01-01T00:00:00Z", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("BERTH_HOME", t.TempDir())
			entry := Entry{PID: os.Getpid(), ID: tt.id, StartedAt: tt.started}
			if err := save(&Registry{Runs: map[int]Entry{entry.PID: entry}}); err != nil {
				t.Fatal(err)
			}
			if removed, err := RemoveIfMatch(entry); err != nil || removed != tt.remove {
				t.Fatalf("RemoveIfMatch = (%v, %v), want %v", removed, err, tt.remove)
			}
		})
	}
}

func TestRemoveIfMatchMissingAndInvalidAreNoOps(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	for _, pid := range []int{0, -1, 42} {
		removed, err := RemoveIfMatch(Entry{PID: pid, ID: "missing"})
		if removed || ((err != nil) != (pid <= 0)) {
			t.Fatalf("pid=%d: (%v, %v)", pid, removed, err)
		}
		if _, err := os.Stat(Path()); !os.IsNotExist(err) {
			t.Fatalf("no-op created runs.json: %v", err)
		}
	}
}

func TestRemoveIfMatchSeesReplacementInsideFileLock(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	old := Entry{PID: os.Getpid(), ID: "old", StartedAt: "2026-10-02T08:00:00Z"}
	if err := Add(old); err != nil {
		t.Fatal(err)
	}
	// Publish a replacement while holding the real file transaction. The
	// delete may start before or after save, but cannot inspect a stale value
	// outside the transaction and then delete blindly after acquiring it.
	type result struct {
		removed bool
		err     error
	}
	begun := make(chan struct{})
	done := make(chan result, 1)
	next := old
	next.ID = "new"
	launched := false
	err := withLock(func() error {
		launched = true
		go func() {
			close(begun)
			removed, err := RemoveIfMatch(old)
			done <- result{removed, err}
		}()
		<-begun
		return save(&Registry{Runs: map[int]Entry{next.PID: next}})
	})
	// Wait for the worker even if the writer failed, before t.Setenv cleans up.
	if !launched {
		t.Fatalf("writer did not acquire file lock: %v", err)
	}
	var out result
	select {
	case out = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("conditional deletion did not finish")
	}
	if err != nil || out.err != nil || out.removed {
		t.Fatalf("writer=%v deletion=%+v", err, out)
	}
	if got := load().Runs[next.PID]; got != next {
		t.Fatalf("replacement lost: %+v", got)
	}
}
