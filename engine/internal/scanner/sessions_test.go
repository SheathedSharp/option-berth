package scanner

import (
	"context"
	"errors"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// sessionRegistry is a run registry that also knows the agent session, the
// shape internal/daemon/runsreg has once a run carries one.
type sessionRegistry struct {
	owner int
	agent state.Session
}

func (r sessionRegistry) Run(p state.Port) (state.Run, bool) {
	if p.PID != r.owner {
		return state.Run{}, false
	}
	return state.Run{ID: "run1", Group: "shop", Name: "web", RootPID: r.owner}, true
}

func (r sessionRegistry) Session(p state.Port) (state.Session, bool) {
	if p.PID != r.owner {
		return state.Session{}, false
	}
	return r.agent, true
}

// A scan tick stamps the owning run's session onto the port and publishes the
// sessions collection built from it.
func TestScanStampsPortSessionAndPublishesTheCollection(t *testing.T) {
	agent := state.Session{
		ID: "claude-code:sess-1", Tool: sessions.ToolClaudeCode,
		Branch: "feature/x", Worktree: "feature-x", Detected: true,
	}
	reg := sessionRegistry{owner: 21, agent: agent}

	loop := New(Options{
		Scan: func(Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{
				{Port: 3100, Process: "node", PID: 21, Command: "node server.js"},
				{Port: 5432, Process: "postgres", PID: 22},
			}, nil
		},
		Runs: func() groups.Registry { return reg },
	})
	loop.SetSessions(func(rows []state.Port) []state.SessionRecord {
		return sessions.Records(nil, []sessions.Live{{RunID: "run1", PID: 21, Group: "shop", Session: agent}}, rows)
	})

	snap, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	byPort := map[int]state.Port{}
	for _, row := range snap.Ports {
		byPort[row.Port] = row
	}
	stamped := byPort[3100].Session
	if stamped == nil {
		t.Fatal("the run's port was published with no session")
	}
	if *stamped != agent {
		t.Errorf("port session = %+v, want %+v", *stamped, agent)
	}
	if other := byPort[5432].Session; other != nil {
		t.Errorf("an unrelated port got session %+v", other)
	}

	if len(snap.Sessions) != 1 {
		t.Fatalf("sessions collection = %+v, want one record", snap.Sessions)
	}
	record := snap.Sessions[0]
	if record.ID != agent.ID || record.Runs != 1 || record.Ports != 1 || !record.Active {
		t.Errorf("session record = %+v", record)
	}
}

// With no provider the collection is an empty array rather than null: the wire
// shape is fixed whether or not anything in this build owns sessions.
func TestSnapshotSessionsAreNeverNull(t *testing.T) {
	loop := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) { return nil, nil }})
	snap, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Sessions == nil {
		t.Error("Snapshot.Sessions is nil")
	}
	if cached := loop.Cached(); cached.Sessions == nil {
		t.Error("Cached().Sessions is nil")
	}
}

// A session appearing is a change worth publishing even when no port moved:
// the desktop's session badge has to arrive without waiting for a port event.
func TestSessionChangeAlonePublishes(t *testing.T) {
	before := state.Snapshot{Ports: []state.Port{}, Groups: []state.Group{}}
	after := state.Snapshot{
		Ports:    []state.Port{},
		Groups:   []state.Group{},
		Sessions: []state.SessionRecord{{Session: state.Session{ID: "s1"}, Active: true}},
	}
	if !snapshotChanged(before, after, false) {
		t.Error("a new session did not count as a change")
	}
}

// TestASessionBuilderThatLeavesAbortsTheScan: the sessions builder is a seam
// the daemon installs after the loop exists. A caller withdrawn inside it ends
// the scan: nothing is attributed, nothing is published, and the error is the
// caller's own cancellation rather than a stale snapshot.
func TestASessionBuilderThatLeavesAbortsTheScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := New(Options{Scan: func(Include) ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{{Port: 8123, PID: 42, Process: "python3"}}, nil
	}})
	l.SetSessions(func([]state.Port) []state.SessionRecord {
		cancel()
		return nil
	})

	snap, err := l.SnapshotAllContext(ctx, Include{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if len(snap.Ports) != 0 || len(l.Cached().Ports) != 0 {
		t.Fatalf("published %+v, want no snapshot for a withdrawn scan", snap.Ports)
	}
}
