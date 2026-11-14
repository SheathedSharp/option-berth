package runstate

import (
	"github.com/sheathedsharp/option-berth/engine/internal/servicefacts"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"testing"
)

func ptr[T any](v T) *T { return &v }
func TestRuntimeProjectionGenerationAndIsolation(t *testing.T) {
	old := state.Snapshot{Groups: []state.Group{{Name: "a", Services: []state.Service{{Name: "worker", Running: true, RunID: ptr("r1"), PID: ptr(101), HealthStatus: &state.Health{Status: "ok"}}}}, {Name: "b", Services: []state.Service{{Name: "worker"}}}}}
	facts := servicefacts.Capture([]servicefacts.RunRecord{{Key: servicefacts.Key{Group: "a", Service: "worker"}, Run: state.ServiceRun{ID: "r2", PID: 102, StartedAt: "2026-11-14T00:00:00Z"}}}, nil)
	got := Project(old, facts, true)
	a, b := got.Groups[0].Services[0], got.Groups[1].Services[0]
	if !a.Running || a.RunID == nil || *a.RunID != "r2" || a.HealthStatus != nil || b.Running {
		t.Fatalf("bad generation/isolation: %+v %+v", a, b)
	}
	if *old.Groups[0].Services[0].RunID != "r1" {
		t.Fatal("mutated published snapshot")
	}
	exit := servicefacts.Capture(nil, []servicefacts.ExitRecord{{Key: servicefacts.Key{Group: "a", Service: "worker"}, Exit: state.ServiceExit{RunID: "r2", Reason: "crashed"}}})
	stopped := Project(got, exit, true).Groups[0].Services[0]
	if stopped.Running || stopped.RunID != nil || stopped.LastExit == nil {
		t.Fatalf("exit: %+v", stopped)
	}
}
func TestStaleListenersAreUnknownNotReady(t *testing.T) {
	old := state.Snapshot{Groups: []state.Group{{Name: "a", Services: []state.Service{{Name: "api", Port: ptr(8000), Running: true, PortActual: ptr(8000)}}}}, Ports: []state.Port{{Port: 8000, Group: ptr("a")}}}
	got := Project(old, nil, false)
	if got.Groups[0].Services[0].PortActual != nil || len(got.Ports) != 0 || got.Observation == nil {
		t.Fatalf("stale observation promoted: %+v", got)
	}
}

func TestLegacyRunPreservesHealthOnlyWithMatchingBirthEvidence(t *testing.T) {
	stamp := "2026-11-14T00:00:00Z"
	old := state.Snapshot{Groups: []state.Group{{Name: "a", Services: []state.Service{{Name: "worker", Running: true, PID: ptr(42), StartedAt: &stamp, HealthStatus: &state.Health{Status: state.HealthOK}}}}}}
	makeFacts := func(at string) servicefacts.Snapshot {
		return servicefacts.Capture([]servicefacts.RunRecord{{Key: servicefacts.Key{Group: "a", Service: "worker"}, Run: state.ServiceRun{PID: 42, StartedAt: at}}}, nil)
	}
	if got := Project(old, makeFacts(stamp), true).Groups[0].Services[0]; got.HealthStatus == nil {
		t.Fatal("legacy matching birth lost computed health")
	}
	for _, at := range []string{"2026-11-14T00:01:00Z", "0001-01-01T00:00:00Z"} {
		if got := Project(old, makeFacts(at), true).Groups[0].Services[0]; got.HealthStatus != nil {
			t.Fatal("changed or unknown legacy birth inherited health")
		}
	}
}
