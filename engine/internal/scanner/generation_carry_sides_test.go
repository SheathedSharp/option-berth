package scanner

import (
	"errors"
	"log/slog"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestTheSameGenerationFailureIsRecordedAndACurrentOneIsNews(t *testing.T) {
	failed := errors.New("collector down")
	l := New(Options{
		Scan:   func(Include) ([]ports.ListeningPort, error) { return nil, failed },
		Logger: slog.New(slog.DiscardHandler),
	})
	l.mu.Lock()
	l.outcomeSeq = 1
	l.haveSnap = true
	l.snap = state.Snapshot{Seq: 4, Ports: []state.Port{{Port: 9, PID: 1}}}
	l.mu.Unlock()
	_, _, changed, err := l.scanLocked(Include{}, Include{}, true)
	if !errors.Is(err, failed) || changed || l.lastErr == nil {
		t.Fatalf("changed=%v err=%v lastErr=%v, a failure of the current generation was dropped", changed, err, l.lastErr)
	}
}

func TestAnOlderFailureIsNotAChange(t *testing.T) {
	obsolete := errors.New("obsolete")
	published := 0
	l := New(Options{
		Scan:   func(Include) ([]ports.ListeningPort, error) { return nil, obsolete },
		Logger: slog.New(slog.DiscardHandler),
		Publish: func(state.Snapshot, state.Snapshot, []state.Event) {
			published++
		},
	})
	l.mu.Lock()
	l.outcomeSeq = 100
	l.haveSnap = true
	l.snap = state.Snapshot{Seq: 4, Ports: []state.Port{{Port: 9, PID: 1}}}
	l.lastErr = nil
	l.mu.Unlock()
	_, _, changed, err := l.scanLocked(Include{}, Include{}, true)
	if err != nil || changed || l.lastErr != nil || published != 0 {
		t.Fatalf("changed=%v err=%v lastErr=%v published=%d, an older failure was published as news", changed, err, l.lastErr, published)
	}

	newer := errors.New("newer")
	l.mu.Lock()
	l.lastErr = newer
	l.mu.Unlock()
	_, _, changed, err = l.scanLocked(Include{}, Include{}, true)
	if !errors.Is(err, obsolete) || changed || l.lastErr != newer || published != 0 {
		t.Fatalf("changed=%v err=%v lastErr=%v published=%d, an older failure hid a newer one or was announced", changed, err, l.lastErr, published)
	}
}

func TestAnEqualGenerationSuccessClearsTheFailure(t *testing.T) {
	l := New(Options{Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"})})
	l.mu.Lock()
	l.outcomeSeq = 1
	l.lastErr = errors.New("previous")
	l.mu.Unlock()
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	if l.lastErr != nil {
		t.Fatal("a success of the same generation left the previous failure in place")
	}

	l = New(Options{Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"})})
	kept := errors.New("newer")
	l.mu.Lock()
	l.outcomeSeq = 5
	l.lastErr = kept
	l.mu.Unlock()
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	if l.lastErr != kept {
		t.Fatal("an older success cleared a newer failure")
	}
}

func TestAPartialScanDoesNotWarnWhenTheCallerDidNotAsk(t *testing.T) {
	warns := &warnCount{}
	l := New(Options{
		Logger: slog.New(warns),
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return nil, &ports.ObservationError{
				Partial: true, Code: "blind", Detail: "half", Hint: "retry",
				Rows: []ports.ListeningPort{{Port: 9, PID: 1, Process: "app"}},
			}
		},
	})
	snap, _, _, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || warns.n != 0 || snap.Observation == nil || snap.Observation.State != state.ObservationPartial {
		t.Fatalf("err=%v warns=%d obs=%+v, a partial scan the caller did not ask to report was warned or dropped", err, warns.n, snap.Observation)
	}
}

func TestUnrequestedHealthAndStatsStayOffTheNextSnapshot(t *testing.T) {
	prevHealth := &state.Health{Status: state.HealthOK}
	prev := state.Snapshot{Ports: []state.Port{{
		Port: 9, PID: 1, Health: prevHealth, Stats: &state.Stats{MemoryRSS: 10},
	}}}
	rows := []state.Port{{Port: 9, PID: 1}}
	carryScanFields(prev, rows, nil, commit{})
	if rows[0].Health != nil || rows[0].Stats != nil {
		t.Fatalf("row=%+v, health or stats nobody asked for were copied forward", rows[0])
	}

	fresh := []state.Port{{Port: 9, PID: 1, Stats: &state.Stats{MemoryRSS: 4}}}
	carryStats([]state.Port{{Port: 9, PID: 1, Stats: &state.Stats{MemoryRSS: 10}}}, fresh)
	if fresh[0].Stats.MemoryRSS != 4 {
		t.Fatalf("stats=%d, a fresh sample was replaced by the previous one", fresh[0].Stats.MemoryRSS)
	}
}

func TestAStatsOnlyDifferenceStillPublishes(t *testing.T) {
	l := New(Options{Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"})})
	snap, _, _, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || len(snap.Ports) != 1 {
		t.Fatal(err)
	}
	rows := append([]state.Port(nil), snap.Ports...)
	rows[0].Stats = &state.Stats{MemoryRSS: 99}
	_, _, changed := l.commitScan(commit{
		seq:      l.scanGeneration() + 1,
		collect:  Include{Stats: true},
		rows:     rows,
		groups:   snap.Groups,
		sessions: snap.Sessions,
	})
	if !changed || l.Cached().Ports[0].Stats == nil || l.Cached().Ports[0].Stats.MemoryRSS != 99 {
		t.Fatalf("changed=%v stats=%+v, a stats-only scan was not published", changed, l.Cached().Ports[0].Stats)
	}
}
