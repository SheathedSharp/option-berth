package runsreg

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// fixtureSession is one attributed coding-agent session, filled in on every
// field so that one dropped on the way through the registry shows up as a
// mismatch rather than as an equal zero value.
func fixtureSession() state.Session {
	return state.Session{
		ID: "claude-code:zz9", Tool: sessions.ToolClaudeCode,
		Label: "port the runsreg tests", Worktree: "feature-y", Branch: "feature/y",
		Detected: true,
	}
}

// A session registered with a run comes back on the run, on the ports the run
// owns, and in the list the runtime service's sessions handlers read.
func TestSessionRoundTrip(t *testing.T) {
	reg := testRegistry(500)
	startedAt := time.Date(2026, 4, 17, 14, 30, 0, 0, time.UTC)
	reg.Register(Record{
		ID: "run-500", PID: 500, Group: "storefront", Name: "vite",
		Cmd: "npm run dev", Cwd: "/home/dev/code/storefront",
		StartedAt: startedAt, Session: fixtureSession(),
	})

	rec, ok := reg.Lookup(500)
	if !ok {
		t.Fatal("the run was not registered")
	}
	if rec.Session.ID != fixtureSession().ID {
		t.Fatalf("Lookup session = %+v, want %+v", rec.Session, fixtureSession())
	}

	byPID, ok := reg.Session(state.Port{PID: 500})
	if !ok {
		t.Fatal("Session did not attribute the run's own pid")
	}
	if byPID != fixtureSession() {
		t.Errorf("Session = %+v, want %+v", byPID, fixtureSession())
	}

	live := reg.SessionRuns()
	if len(live) != 1 {
		t.Fatalf("SessionRuns = %+v, want one entry", live)
	}
	want := sessions.Live{
		RunID: "run-500", PID: 500, Group: "storefront", Name: "vite",
		Cmd: "npm run dev", Cwd: "/home/dev/code/storefront",
		StartedAt: startedAt, Session: fixtureSession(),
	}
	if live[0] != want {
		t.Errorf("SessionRuns = %+v, want the run projected with its session as %+v", live, want)
	}
}

// A grandchild of the run carries the same session as the run itself: the PPID
// walk that decides attribution is the one that answers this, so `npm run dev`
// -> vite -> esbuild stays a single session.
func TestSessionFollowsThePPIDWalk(t *testing.T) {
	reg := testRegistry(700)
	reg.Parents = func() map[int]int { return map[int]int{900: 800, 800: 700, 700: 1} }
	reg.Register(Record{ID: "run-700", PID: 700, Group: "storefront", Name: "vite", Session: fixtureSession()})

	got, ok := reg.Session(state.Port{PID: 900, PPID: 800})
	if !ok {
		t.Fatal("a grandchild's port was attributed to no run")
	}
	if got.ID != fixtureSession().ID {
		t.Errorf("a grandchild's port got session %+v, want %+v", got, fixtureSession())
	}
}

// A run nobody attributed to a session has none, and neither do its ports:
// a plain shell must not invent a session for every dev server on the machine.
func TestRunsWithoutASessionReportNone(t *testing.T) {
	reg := testRegistry(700)
	reg.Register(Record{ID: "run-700", PID: 700, Group: "storefront", Name: "vite"})

	if got, ok := reg.Session(state.Port{PID: 700}); ok {
		t.Errorf("a run with no session reported %+v", got)
	}
	live := reg.SessionRuns()
	if len(live) != 0 {
		t.Errorf("SessionRuns = %+v, want empty", live)
	}
	if live == nil {
		t.Error("SessionRuns returned nil rather than an empty slice")
	}
}

// A dead run leaves the session list with it, so "active" means what it says.
func TestSessionRunsDropsDeadRuns(t *testing.T) {
	reg := testRegistry()
	reg.Register(Record{ID: "run-701", PID: 701, Session: fixtureSession()})
	if live := reg.SessionRuns(); len(live) != 0 {
		t.Errorf("SessionRuns kept a dead run: %+v", live)
	}
}
