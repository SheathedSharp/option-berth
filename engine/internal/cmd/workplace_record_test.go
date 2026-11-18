package cmd

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/workplace"
)

// A recorder with no worker behind it: the queue is the whole observable
// behaviour of Observe, and reading it is how these tests see what a bridge
// event turned into.
func quietRecorder() *workplaceRecorder {
	return &workplaceRecorder{
		root:   "/home/dev/code/demo",
		events: make(chan workplace.Record, recordQueueDepth),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
	}
}

func (r *workplaceRecorder) drain() []workplace.Record {
	var out []workplace.Record
	for {
		select {
		case rec := <-r.events:
			out = append(out, rec)
		default:
			return out
		}
	}
}

func TestRecorderObserveDropsRatherThanBlocks(t *testing.T) {
	r := quietRecorder()
	// Fill the queue past its depth: a recorder that blocked here would stall
	// the bridge, which is the thing it must never do.
	for i := 0; i < recordQueueDepth*2; i++ {
		r.Observe(workplace.Record{Kind: workplace.KindShell, Input: "many"})
	}
	if n := len(r.drain()); n > recordQueueDepth {
		t.Fatalf("the queue grew past its depth: %d", n)
	}
}

func TestShellRecordsCarryTheRunAndItsOutcome(t *testing.T) {
	r := quietRecorder()
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)
	id := r.startShell([]string{"npm", "test"}, "run-1", 42, "/tmp/api.log", started)
	got := r.drain()
	if len(got) != 1 {
		t.Fatalf("startShell queued %d observations", len(got))
	}
	rec := got[0]
	if rec.ID != id || rec.Kind != workplace.KindShell || rec.Status != workplace.StatusRunning {
		t.Fatalf("the start observation = %+v", rec)
	}
	if rec.Input != "npm test" || rec.RunID != "run-1" || rec.PID != 42 || rec.LogPath != "/tmp/api.log" {
		t.Fatalf("the start observation lost the run evidence: %+v", rec)
	}
	if !rec.StartedAt.Equal(started) {
		t.Fatalf("started_at = %v, want the run's own start %v", rec.StartedAt, started)
	}

	r.endShell(id, 2, false)
	got = r.drain()
	if len(got) != 1 {
		t.Fatalf("endShell queued %d observations", len(got))
	}
	end := got[0]
	if end.ID != id || end.Status != workplace.StatusFailed {
		t.Fatalf("the end observation = %+v", end)
	}
	if end.ExitCode == nil || *end.ExitCode != 2 || end.EndedAt.IsZero() {
		t.Fatalf("the end observation lost the outcome: %+v", end)
	}

	// A command the user stopped is interrupted whatever the signal left.
	r.endShell(id, 130, true)
	if got = r.drain(); got[0].Status != workplace.StatusInterrupted {
		t.Fatalf("an interrupted command = %q", got[0].Status)
	}
}

func TestNilRecorderSwallowsEverything(t *testing.T) {
	// A command run outside any worktree has no recorder, and every call on it
	// has to be safe: the commands that record are the commands that must work
	// anywhere.
	var r *workplaceRecorder
	r.Observe(workplace.Record{Kind: workplace.KindShell})
	if id := r.startShell([]string{"ls"}, "", 0, "", time.Time{}); id != "" {
		t.Fatalf("a nil recorder returned a block id %q", id)
	}
	r.endShell("b1", 0, false)
	r.Close()
}
