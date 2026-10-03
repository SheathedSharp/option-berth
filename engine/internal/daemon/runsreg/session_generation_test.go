package runsreg

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestSessionDoesNotAttachReplacementToCapturedRun(t *testing.T) {
	r := testRegistry(100)
	r.Register(Record{ID: "old", PID: 100, Group: "demo", Name: "api", Session: state.Session{ID: "old-session"}})
	run, ok := r.Run(state.Port{PID: 100})
	if !ok {
		t.Fatal("run fixture missing")
	}
	p := state.Port{PID: 100, Run: &run}
	if session, ok := r.Session(p); !ok || session.ID != "old-session" {
		t.Fatal("matching run lost its session")
	}
	r.Register(Record{ID: "new", PID: 100, Group: "demo", Name: "api", Session: state.Session{ID: "new-session"}})
	if session, ok := r.Session(p); ok {
		t.Fatalf("captured old run inherited replacement session: %+v", session)
	}
}
