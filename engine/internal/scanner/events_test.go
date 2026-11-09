package scanner

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
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

// An unchanged group cannot produce a new readiness fact. With one group
// moving and another standing still, only the moving group is joined: the
// still one keeps its previous exit verbatim and emits nothing.
func TestAnUnchangedGroupEmitsNoReadyTimeout(t *testing.T) {
	rootA := "/code/a"
	rootB := "/code/b"
	stopped := state.Service{Name: "worker", LastExit: &state.ServiceExit{
		Code: 1, Reason: "ready_timeout", At: "earlier", RunID: "run-b",
	}}
	beforeA := state.Group{Name: "a", Repo: "a", RootDir: &rootA, Services: []state.Service{{Name: "api", Running: true}}}
	beforeB := state.Group{Name: "b", Repo: "b", RootDir: &rootB, Services: []state.Service{stopped}}
	afterA := beforeA
	afterA.Services = []state.Service{{Name: "api", Running: true}, {Name: "extra", Running: true}}
	afterB := beforeB

	events := deriveEvents(
		state.Snapshot{Seq: 1, Groups: []state.Group{beforeA, beforeB}},
		state.Snapshot{Seq: 2, At: "now", Groups: []state.Group{afterA, afterB}},
		"now",
	)
	for _, ev := range events {
		if ev.Kind == "ready_timeout" {
			t.Fatalf("an unchanged group re-emitted its timeout: %+v", ev)
		}
	}
}
