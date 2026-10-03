package daemon

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// fakeRuns is a run registry carrying sessions, standing in for
// internal/daemon/runsreg — which this package must not import.
type fakeRuns struct {
	live []sessions.Live
}

func (f *fakeRuns) Run(p state.Port) (state.Run, bool) {
	for _, l := range f.live {
		if l.PID == p.PID {
			return state.Run{ID: l.RunID, Group: l.Group, Name: l.Name, RootPID: l.PID}, true
		}
	}
	return state.Run{}, false
}

func (f *fakeRuns) Prune() {}

func (f *fakeRuns) Stopping([]int) {}

func (f *fakeRuns) RunStart(int) (time.Time, bool) { return time.Time{}, false }

func (f *fakeRuns) GroupPIDs(group string) []int {
	var out []int
	for _, l := range f.live {
		if l.Group == group {
			out = append(out, l.PID)
		}
	}
	return out
}

func (f *fakeRuns) GroupRuns(group string) []state.Run {
	var out []state.Run
	for _, l := range f.live {
		if l.Group == group {
			out = append(out, state.Run{ID: l.RunID, Group: l.Group, Name: l.Name, RootPID: l.PID})
		}
	}
	return out
}

func (f *fakeRuns) Session(p state.Port) (state.Session, bool) {
	for _, l := range f.live {
		if l.PID == p.PID {
			return l.Session, true
		}
	}
	return state.Session{}, false
}

func (f *fakeRuns) SessionRuns() []sessions.Live { return f.live }

func testSession(id string) state.Session {
	return state.Session{
		ID: id, Tool: sessions.ToolClaudeCode, Label: "ship it",
		Worktree: "feature-x", Branch: "feature/x", Detected: true,
	}
}

// sessionHarness is a daemon whose fake scan shows one listener owned by one
// agent session, with the run registry and the store both wired up.
func sessionHarness(t *testing.T, ctx context.Context) (*testHarness, *fakeRuns) {
	t.Helper()
	h := newHarness(t, ctx)
	runs := &fakeRuns{live: []sessions.Live{{
		RunID: "run1", PID: 4242, Group: "shop", Name: "web",
		Cmd: "python3 -m http.server", Cwd: "/home/me/code/shop",
		StartedAt: time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC),
		Session:   testSession("claude-code:abc"),
	}}}
	h.srv.runtime.SetRuns(runs)
	h.setRows(ports.ListeningPort{
		Port: 3000, BindAddress: "127.0.0.1", PID: 4242, Process: "python3",
		Command: "python3 -m http.server", IPVersion: "IPv4",
	})
	h.loop.Invalidate()
	if _, err := h.loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("priming the snapshot: %v", err)
	}
	return h, runs
}

// openSessionStore gives a harness a temp database with the sessions table in
// it. The directory is the caller's, claimed before the harness exists so the
// store is closed before it is removed.
func openSessionStore(t *testing.T, h *testHarness, dir string) {
	t.Helper()
	h.withStore(filepath.Join(dir, "option-berth.db"))
}

func TestSessionsKillRequiresAnID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarness(t, ctx)
	c := h.dial(ctx)

	if e := c.call("sessions.kill", rpc.SessionsKillParams{}, nil); e == nil {
		t.Fatal("want invalid_params for a missing id")
	}
}

// A dry run reports the plan and changes nothing, in the same envelope
// groups.kill returns (contract §3).
func TestSessionsKillDryRunReturnsTheKillEnvelope(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarness(t, ctx)
	c := h.dial(ctx)

	var env rpc.KillEnvelope
	if e := c.call("sessions.kill", rpc.SessionsKillParams{ID: "claude-code:abc", DryRun: true}, &env); e != nil {
		t.Fatalf("sessions.kill --dry-run: %v", e)
	}
	if len(env.Results) != 1 {
		t.Fatalf("results = %+v, want the session's one port", env.Results)
	}
	if env.Results[0].Port != 3000 {
		t.Errorf("planned target = %+v", env.Results[0])
	}
	if env.Affected == nil {
		t.Error("affected is null; contract §3 wants an array")
	}
}

func TestSessionTargetsCoversPortsThenPidlessRuns(t *testing.T) {
	s := testSession("s1")
	snap := state.Snapshot{Ports: []state.Port{
		{Port: 3000, BindAddress: "127.0.0.1", PID: 100, Session: &s,
			Run: &state.Run{ID: "run1", RootPID: 100}},
		{Port: 5432, BindAddress: "127.0.0.1", PID: 999},
	}}
	live := []sessions.Live{
		{RunID: "run1", PID: 100, Session: s},
		{RunID: "run2", PID: 200, Session: s}, // started, nothing listening yet
	}

	started := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	got := sessionTargets(snap, live, "s1", func(pid int) (time.Time, bool) {
		if pid == 200 {
			return started, true
		}
		return time.Time{}, false
	})
	if len(got) != 2 {
		t.Fatalf("targets = %+v, want the port and the silent run", got)
	}
	if got[0] != (killer.Target{Port: 3000, BindAddress: "127.0.0.1"}) {
		t.Errorf("first target = %+v", got[0])
	}
	if got[1] != (killer.Target{PID: 200, StartedAt: started}) {
		t.Errorf("second target = %+v, want the pid of the run with no port and its identity", got[1])
	}
}

func TestSweepSessionsTouchesLiveAndPrunesOld(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h, _ := sessionHarness(t, ctx)
	openSessionStore(t, h, dir)

	table := h.srv.runtime.Store.Sessions()
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	long := now.Add(-30 * 24 * time.Hour)

	// The live session, recorded long ago, and a stale one nothing runs.
	if err := table.Upsert(store.SessionRow{ID: "claude-code:abc", FirstSeen: long, LastSeen: long}); err != nil {
		t.Fatal(err)
	}
	if err := table.Upsert(store.SessionRow{ID: "codex:gone", FirstSeen: long, LastSeen: long}); err != nil {
		t.Fatal(err)
	}

	SweepSessions(h.srv.runtime, now)

	rows, err := table.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 1 || rows[0].ID != "claude-code:abc" {
		t.Fatalf("after the sweep: %+v", rows)
	}
	if !rows[0].LastSeen.Equal(now.UTC()) {
		t.Errorf("the live session was not touched: %v", rows[0].LastSeen)
	}
}

func TestSessionsCapabilityIsAnnounced(t *testing.T) {
	for _, c := range Capabilities() {
		if c == "sessions" {
			return
		}
	}
	t.Errorf("daemon.hello capabilities = %v, want sessions", Capabilities())
}
