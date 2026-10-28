package cmd

import (
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/workplace"
)

// A recorder with no worker behind it: the queue is the whole observable
// behaviour of Observe, and reading it is how these tests see what a bridge
// event turned into.
func quietRecorder() *workplaceRecorder {
	return &workplaceRecorder{
		root:   "/home/dev/code/demo",
		prefix: "bridge1",
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

func TestBridgeEventsBecomeOneBlockPerTurn(t *testing.T) {
	r := quietRecorder()
	observe := r.bridgeEvents("codex")

	// Events that are not a turn's start or end are not work items.
	observe(map[string]any{"type": "session.started", "provider": "codex"})
	observe(map[string]any{"type": "turn.diff", "turn": "t1"})
	observe(map[string]any{"type": "turn.started"})
	if got := r.drain(); len(got) != 0 {
		t.Fatalf("non-turn events produced blocks: %+v", got)
	}

	observe(map[string]any{"type": "turn.started", "turn": "t1", "input": "fix the build"})
	observe(map[string]any{"type": "turn.completed", "turn": "t1", "outcome": "completed"})
	got := r.drain()
	if len(got) != 2 {
		t.Fatalf("one turn produced %d observations, want a start and an end", len(got))
	}
	start, end := got[0], got[1]
	if start.ID != end.ID {
		t.Fatalf("the two ends of one turn have different ids: %q and %q", start.ID, end.ID)
	}
	if start.Kind != workplace.KindAgent || start.Source != workplace.SourceEngine {
		t.Fatalf("a bridge turn was recorded as %s/%s", start.Kind, start.Source)
	}
	if start.Status != workplace.StatusRunning || start.Input != "fix the build" || start.Provider != "codex" {
		t.Fatalf("the start observation = %+v", start)
	}
	if start.StartedAt.IsZero() {
		t.Fatal("the start observation has no time")
	}
	if end.Status != workplace.StatusCompleted || end.EndedAt.IsZero() {
		t.Fatalf("the end observation = %+v", end)
	}
	// The end does not repeat the prompt: the bridge knows it, but re-sending
	// it would be a second copy of the same text to keep in step. It does name
	// the provider, so an end that arrives on its own still says which agent
	// ran the turn.
	if end.Input != "" {
		t.Fatalf("the end observation repeats the prompt: %+v", end)
	}
	if end.Provider != "codex" {
		t.Fatalf("the end observation lost the provider: %+v", end)
	}
}

func TestBridgeEventsMapOutcomesAndKeepTurnsApart(t *testing.T) {
	r := quietRecorder()
	observe := r.bridgeEvents("claude")

	observe(map[string]any{"type": "turn.started", "turn": "t1", "input": "one"})
	observe(map[string]any{"type": "turn.completed", "turn": "t1", "outcome": "interrupted"})
	observe(map[string]any{"type": "turn.started", "turn": "t2", "input": "two"})
	observe(map[string]any{"type": "turn.completed", "turn": "t2", "outcome": "lost"})

	got := r.drain()
	if len(got) != 4 {
		t.Fatalf("two turns produced %d observations", len(got))
	}
	if got[0].ID == got[2].ID {
		t.Fatal("two turns of one bridge share a block id")
	}
	if got[1].Status != workplace.StatusInterrupted {
		t.Fatalf("an interrupted turn = %q", got[1].Status)
	}
	if got[3].Status != workplace.StatusFailed {
		t.Fatalf("a lost turn = %q", got[3].Status)
	}
	// The id is scoped to this bridge process: resuming the same provider
	// session in a new bridge must not merge its turns into the old block.
	if !strings.HasPrefix(got[0].ID, "bridge1:") {
		t.Fatalf("block id %q is not scoped to the bridge that made it", got[0].ID)
	}
}

func TestRecorderObserveDropsRatherThanBlocks(t *testing.T) {
	r := quietRecorder()
	observe := r.bridgeEvents("codex")
	// Fill the queue past its depth: a recorder that blocked here would stall
	// the bridge, which is the thing it must never do.
	for i := 0; i < recordQueueDepth*2; i++ {
		observe(map[string]any{"type": "turn.started", "turn": "t" + strings.Repeat("x", i%3+1), "input": "many"})
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
	if r.bridgeEvents("codex") != nil {
		t.Fatal("a nil recorder handed out an observer that would drop every event")
	}
}
