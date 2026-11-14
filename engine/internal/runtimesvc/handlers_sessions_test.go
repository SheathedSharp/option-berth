package runtimesvc

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// fakeRuns is a run registry that knows sessions, standing in for
// internal/runtimesvc/runsreg, which this package is not allowed to import.
type fakeRuns struct {
	live []sessions.Live
}

func (s *fakeRuns) find(pid int) (sessions.Live, bool) {
	for _, live := range s.live {
		if live.PID == pid {
			return live, true
		}
	}
	return sessions.Live{}, false
}

func runOf(live sessions.Live) state.Run {
	return state.Run{ID: live.RunID, Group: live.Group, Name: live.Name, RootPID: live.PID}
}

func (s *fakeRuns) Run(port state.Port) (state.Run, bool) {
	live, found := s.find(port.PID)
	if !found {
		return state.Run{}, false
	}
	return runOf(live), true
}

func (s *fakeRuns) Session(port state.Port) (state.Session, bool) {
	live, found := s.find(port.PID)
	if !found {
		return state.Session{}, false
	}
	return live.Session, true
}

func (s *fakeRuns) Prune()         {}
func (s *fakeRuns) Stopping([]int) {}
func (s *fakeRuns) RunStart(int) (time.Time, bool) {
	return time.Time{}, false
}

func (s *fakeRuns) GroupPIDs(group string) []int {
	var pids []int
	for _, live := range s.live {
		if live.Group == group {
			pids = append(pids, live.PID)
		}
	}
	return pids
}

func (s *fakeRuns) GroupRuns(group string) []state.Run {
	var runs []state.Run
	for _, live := range s.live {
		if live.Group == group {
			runs = append(runs, runOf(live))
		}
	}
	return runs
}

func (s *fakeRuns) SessionRuns() []sessions.Live { return s.live }

func sampleSession(id string) state.Session {
	return state.Session{
		ID: id, Tool: sessions.ToolClaudeCode, Label: "ship it",
		Worktree: "feature-x", Branch: "feature/x", Detected: true,
	}
}

// sessionHarnessWithOnePort builds a runtime service whose scan sees one listener owned
// by one agent session, with the registry and the store both wired.
func sessionHarnessWithOnePort(t *testing.T, ctx context.Context) (*testHarness, *fakeRuns) {
	t.Helper()
	h := newHarness(t, ctx)
	registry := &fakeRuns{live: []sessions.Live{{
		RunID: "run1", PID: 4242, Group: "shop", Name: "web",
		Cmd: "python3 -m http.server", Cwd: "/home/me/code/shop",
		StartedAt: time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC),
		Session:   sampleSession("claude-code:abc"),
	}}}
	h.srv.runtime.SetRuns(registry)
	h.publishListener(t, ports.ListeningPort{
		Port: 3000, BindAddress: "127.0.0.1", PID: 4242, Process: "python3",
		Command: "python3 -m http.server", IPVersion: "IPv4",
	})
	return h, registry
}

// publishListener makes the harness's scan see one listener immediately,
// instead of waiting for the next tick.
func (h *testHarness) publishListener(t *testing.T, row ports.ListeningPort) {
	t.Helper()
	h.setRows(row)
	h.loop.Invalidate()
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the snapshot: %v", err)
	}
}

func sessionsIn(t *testing.T, table store.SessionStore) []store.SessionRow {
	t.Helper()
	rows, err := table.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return rows
}

func attachSessionStore(t *testing.T, h *testHarness, dir string) {
	t.Helper()
	h.withStore(filepath.Join(dir, "option-berth.db"))
}

func TestSessionsKillRejectsAMissingID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarnessWithOnePort(t, ctx)
	client := h.dial(ctx)

	if err := client.call("sessions.kill", rpc.SessionsKillParams{}, nil); err == nil {
		t.Fatal("want invalid_params for a missing id")
	}
}

func TestSessionsKillDryRunPlansTheSessionsPorts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarnessWithOnePort(t, ctx)
	client := h.dial(ctx)

	var envelope rpc.KillEnvelope
	if err := client.call("sessions.kill",
		rpc.SessionsKillParams{ID: "claude-code:abc", DryRun: true}, &envelope); err != nil {
		t.Fatalf("sessions.kill --dry-run: %v", err)
	}
	if len(envelope.Results) != 1 || envelope.Results[0].Port != 3000 {
		t.Fatalf("planned targets = %+v, want port 3000", envelope.Results)
	}
	if envelope.Affected == nil {
		t.Error("affected is null; the kill envelope promises an array")
	}
}

func TestSessionTargetsCoverPortsThenRunsWithoutOne(t *testing.T) {
	owner := sampleSession("s1")
	snapshot := state.Snapshot{Ports: []state.Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Session: &owner,
			Run: &state.Run{ID: "run1", RootPID: 100}},
		{Port: 5432, BindAddress: "127.0.0.1", PID: 999},
	}}
	live := []sessions.Live{
		{RunID: "run1", PID: 100, Session: owner},
		{RunID: "run2", PID: 200, Session: owner}, // started, not listening yet
	}
	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

	targets := sessionTargets(snapshot, live, "s1", func(pid int) (time.Time, bool) {
		return started, pid == 200
	})
	if len(targets) != 2 {
		t.Fatalf("targets = %+v, want the port and the silent run", targets)
	}
	if targets[0] != (killer.Target{Port: 3000, BindAddress: "127.0.0.1"}) {
		t.Errorf("first target = %+v", targets[0])
	}
	if targets[1] != (killer.Target{PID: 200, StartedAt: started}) {
		t.Errorf("second target = %+v, want the silent run's pid and start", targets[1])
	}
}

func TestSweepTouchesLiveSessionsAndPrunesTheGoneOnes(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarnessWithOnePort(t, ctx)
	attachSessionStore(t, h, dir)

	table := h.srv.runtime.Store.Sessions()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)
	for _, id := range []string{"claude-code:abc", "codex:gone"} {
		if err := table.Upsert(store.SessionRow{
			ID: id, FirstSeen: old, LastSeen: old,
		}); err != nil {
			t.Fatal(err)
		}
	}

	SweepSessions(h.srv.runtime, now)

	rows := sessionsIn(t, table)
	if len(rows) != 1 || rows[0].ID != "claude-code:abc" {
		t.Fatalf("after the sweep: %+v", rows)
	}
	if rows[0].LastSeen.Sub(now) != 0 {
		t.Errorf("the live session was not touched: %v", rows[0].LastSeen)
	}
}

func TestSessionsCapabilityIsAdvertised(t *testing.T) {
	for _, capability := range Capabilities() {
		if capability == "sessions" {
			return
		}
	}
	t.Errorf("capabilities = %v, want sessions", Capabilities())
}
