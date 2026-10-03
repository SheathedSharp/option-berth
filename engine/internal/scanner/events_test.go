package scanner

import (
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestDeriveEventsIncludesReadyTimeout(t *testing.T) {
	root := "/code/example-worker"
	before := state.Group{
		Name: "example-worker", Repo: "example-worker", RootDir: &root,
		Services: []state.Service{{Name: "api", Running: true}},
	}
	after := before
	after.Services = []state.Service{{Name: "api", LastExit: &state.ServiceExit{
		Code: 1, Reason: "ready_timeout", At: "now", RunID: "run-1",
	}}}
	events := deriveEvents(
		state.Snapshot{Seq: 1, Groups: []state.Group{before}},
		state.Snapshot{Seq: 2, At: "now", Groups: []state.Group{after}},
		"now",
	)
	if len(events) != 1 || events[0].Kind != "ready_timeout" {
		t.Fatalf("events = %+v, want one ready_timeout event", events)
	}
	if events[0].Source == nil || events[0].Group == nil || *events[0].Group != "example-worker" {
		t.Fatalf("event identity = %+v, want group source", events[0])
	}
	if events[0].Data["service"] != "api" {
		t.Fatalf("event data = %+v, want service api", events[0].Data)
	}

	repeated := deriveEvents(
		state.Snapshot{Seq: 2, Groups: []state.Group{after}},
		state.Snapshot{Seq: 3, At: "later", Groups: []state.Group{after}},
		"later",
	)
	if len(repeated) != 0 {
		t.Fatalf("repeated events = %+v, want none", repeated)
	}
}
