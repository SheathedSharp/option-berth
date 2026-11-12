package groups

import (
	"reflect"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// sessionRunSet is a Registry that also implements SessionRegistry: the shape
// the daemon's run registry takes once a run carries a session.
type sessionRunSet struct {
	runSet
	session state.Session
}

func (s sessionRunSet) Session(p state.Port) (state.Session, bool) {
	if p.Port == s.port {
		return s.session, true
	}
	return state.Session{}, false
}

func requireResolvedPort(t *testing.T, got, want state.Port) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Errorf("port %d = %#v, want %#v", want.Port, got, want)
	}
}

func TestAttributeWithStampsTheSession(t *testing.T) {
	want := state.Session{
		ID: "claude-code:def", Tool: "claude-code",
		Worktree: "feature-y", Branch: "feature/y", Detected: true,
	}
	pp := []ports.ListeningPort{
		{Port: 3000, PID: 11, Process: "node", Command: "node server.js"},
		{Port: 4000, PID: 12, Process: "python3", Command: "python3 -m http.server"},
	}

	resolved, index := AttributeWith(pp,
		sessionRunSet{runSet: runSet{port: 4000, id: "r1", group: "itest", name: "web"}, session: want},
		nil)
	if index == nil {
		t.Fatal("AttributeWith returned a nil index")
	}
	if len(index.Configs()) != 0 {
		t.Errorf("index picked up %d configs, want none", len(index.Configs()))
	}
	if len(resolved) != 2 {
		t.Fatalf("resolved %d ports, want 2", len(resolved))
	}

	group := "itest"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	requireResolvedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 3000, URL: "http://localhost:3000",
		PID: 11, Process: "node", DisplayName: "server.js",
		Command: "node server.js", Type: state.TypeUser,
	})
	requireResolvedPort(t, resolved[1], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "python3", DisplayName: "web",
		Command: "python3 -m http.server", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run, Session: &want,
	})
	if pp[0].Group != "" || pp[0].GroupSource != "" || pp[0].RunID != "" || pp[0].RunGroup != "" || pp[0].Tag != "" || pp[0].RunRootPID != 0 {
		t.Errorf("unattributed scanner row was stamped: %+v", pp[0])
	}
	if pp[1].Group != "itest" || pp[1].GroupSource != "start" || pp[1].RunID != "r1" || pp[1].RunGroup != "itest" || pp[1].Tag != "web" || pp[1].RunRootPID != 12 {
		t.Errorf("scanner row = group %q source %q run %q/%q/%q pid %d, want itest/start/r1/itest/web/12",
			pp[1].Group, pp[1].GroupSource, pp[1].RunID, pp[1].RunGroup, pp[1].Tag, pp[1].RunRootPID)
	}
}

// A registry that knows nothing about sessions publishes null sessions, which
// is the direct-scan path: runs.json has no session column.
func TestAttributeWithoutASessionRegistryLeavesSessionsNull(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "python3"}}
	resolved, _ := AttributeWith(pp,
		runSet{port: 4000, id: "r1", group: "itest", name: "web"}, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	group := "itest"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "itest", Name: "web", RootPID: 12}
	requireResolvedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "python3", DisplayName: "web", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run,
	})
}

// An empty session id is not a session: a registry that answers ok with a zero
// value must not put an empty badge on the row.
func TestAttributeWithIgnoresAnEmptySessionID(t *testing.T) {
	pp := []ports.ListeningPort{{Port: 4000, PID: 12, Process: "python3"}}
	resolved, _ := AttributeWith(pp,
		sessionRunSet{runSet: runSet{port: 4000, id: "r1", group: "g", name: "n"}}, nil)
	if len(resolved) != 1 {
		t.Fatalf("resolved %d ports, want 1", len(resolved))
	}
	group := "g"
	source := state.SourceStart
	run := state.Run{ID: "r1", Group: "g", Name: "n", RootPID: 12}
	requireResolvedPort(t, resolved[0], state.Port{
		Host: "localhost", Port: 4000, URL: "http://localhost:4000",
		PID: 12, Process: "python3", DisplayName: "n", Type: state.TypeUser,
		Group: &group, GroupSource: &source, Run: &run,
	})
}
