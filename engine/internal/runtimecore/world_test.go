package runtimecore

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func testGroup(services ...state.Service) state.Group {
	return state.Group{
		Name:     "shop",
		Identity: "wt_0123456789ab",
		Branch:   "main",
		Services: services,
	}
}

func strPointer(value string) *string { return &value }
func intPointer(value int) *int       { return &value }
func boolPointer(value bool) *bool    { return &value }

func TestReduceWorldCollapsesADependencyCascadeIntoOneRootCause(t *testing.T) {
	port := 3000
	group := testGroup(
		state.Service{
			Name: "auth", Port: intPointer(port), PortAuto: false,
			LastExit: &state.ServiceExit{
				Code: 1, Reason: "port_occupied", RunID: "auth-1", PortHint: port,
				PortHolder: &state.PortHolder{
					Port: port, PID: 4242, Command: "node server.js",
					Kind: state.PortHolderUnknown,
				},
			},
		},
		state.Service{
			Name: "api", DependsOn: []string{"auth"},
			LastExit: &state.ServiceExit{Code: 1, Reason: "dependency_not_ready", RunID: "api-1"},
		},
		state.Service{
			Name: "gateway", DependsOn: []string{"api"},
			LastExit: &state.ServiceExit{Code: 1, Reason: "dependency_not_ready", RunID: "gateway-1"},
		},
	)
	world := ReduceWorld(WorldInput{
		Seq: 7, ObservedAt: "2026-07-01T12:00:00Z", Group: group,
		Observation: &state.Observation{State: state.ObservationObservable},
	})

	if world.Health != WorldBroken {
		t.Fatalf("health = %q, want broken", world.Health)
	}
	if len(world.Diagnosis.RootCauses) != 1 {
		t.Fatalf("root causes = %+v, want exactly the port conflict", world.Diagnosis.RootCauses)
	}
	root := world.Diagnosis.RootCauses[0]
	if root.Service != "auth" || root.Kind != DriftPortOccupied || root.Severity != SeverityCritical {
		t.Fatalf("root cause = %+v, want auth/port_occupied/critical", root)
	}
	if len(root.Impacted) != 2 || root.Impacted[0] != "api" || root.Impacted[1] != "gateway" {
		t.Fatalf("impacted = %v, want [api gateway]", root.Impacted)
	}
	for _, service := range world.Services {
		switch service.Name {
		case "api", "gateway":
			if service.State != ServiceBlockedByDep {
				t.Errorf("%s state = %q, want blocked_by_dep", service.Name, service.State)
			}
			if len(service.BlockedBy) != 1 || service.BlockedBy[0] != "auth" {
				t.Errorf("%s blocked_by = %v, want [auth]", service.Name, service.BlockedBy)
			}
		}
	}
	if len(world.Diagnosis.NextActions) == 0 {
		t.Fatal("no next action for a broken world")
	}
	first := world.Diagnosis.NextActions[0]
	if first.Safety != SafetyHumanDecision || first.Command != "oberth status --json" {
		t.Fatalf("first action = %+v, want a read-only human decision on a foreign holder", first)
	}
}

func TestReduceWorldReportsStaleCodeBinaryAndManifestAsDrifts(t *testing.T) {
	group := testGroup(state.Service{
		Name: "api", Running: true, RunID: strPointer("run-1"), PID: intPointer(999),
		CodeChangedSinceStart:       boolPointer(true),
		ExecutableChangedSinceStart: boolPointer(true),
		ManifestRuntimeMismatch:     true,
	})
	world := ReduceWorld(WorldInput{
		Group: group, Observation: &state.Observation{State: state.ObservationObservable},
	})

	if world.Health != WorldDrifted {
		t.Fatalf("health = %q, want drifted", world.Health)
	}
	if world.Services[0].State != ServiceReady {
		t.Fatalf("state = %q, want ready", world.Services[0].State)
	}
	if len(world.Diagnosis.Drifts) != 3 {
		t.Fatalf("drifts = %+v, want code, binary and manifest", world.Diagnosis.Drifts)
	}
	var restarts int
	for _, action := range world.Diagnosis.NextActions {
		if action.Command == "oberth restart --only api" {
			restarts++
		}
	}
	if restarts != 1 {
		t.Fatalf("restart actions = %d, want one deduplicated action: %+v", restarts, world.Diagnosis.NextActions)
	}
	if world.Diagnosis.Healthy {
		t.Fatal("a drifted world must not report the diagnosis as healthy")
	}
}

func TestReduceWorldTreatsAPortlessWorkerAsHealthy(t *testing.T) {
	group := testGroup(state.Service{
		Name: "worker", Running: true, RunID: strPointer("run-1"), PID: intPointer(4242),
	})
	world := ReduceWorld(WorldInput{
		Group: group, Observation: &state.Observation{State: state.ObservationObservable},
	})

	if world.Health != WorldHealthy {
		t.Fatalf("health = %q, want healthy", world.Health)
	}
	if !world.Diagnosis.Healthy {
		t.Fatalf("diagnosis = %+v, want healthy", world.Diagnosis)
	}
	if len(world.Diagnosis.RootCauses) != 0 || len(world.Diagnosis.Drifts) != 0 {
		t.Fatalf("diagnosis = %+v, want no faults", world.Diagnosis)
	}
}

func TestReduceWorldBlindObservationNeverClaimsHealthy(t *testing.T) {
	group := testGroup(state.Service{
		Name: "api", Port: intPointer(3000),
	})
	world := ReduceWorld(WorldInput{
		Group:       group,
		Observation: &state.Observation{State: state.ObservationBlind},
	})

	if world.Coverage != CoverageBlind {
		t.Fatalf("coverage = %q, want blind", world.Coverage)
	}
	if world.Health != WorldBlind {
		t.Fatalf("health = %q, want blind", world.Health)
	}
	if world.Services[0].State != ServiceUnobserved {
		t.Fatalf("state = %q, want unobserved rather than stopped", world.Services[0].State)
	}
	if world.Diagnosis.Healthy {
		t.Fatal("blind observation must not report a healthy diagnosis")
	}
}

func TestLoopTrackerDetectsCrashLoopAndRestartThrash(t *testing.T) {
	now := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	crash := NewLoopTracker()
	for i, runID := range []string{"run-1", "run-2", "run-3"} {
		crash.RecordExit("wt", "api", runID, "crashed", now.Add(-time.Duration(3-i)*time.Minute))
	}
	// A repeated notification for a run already seen is not another crash.
	crash.RecordExit("wt", "api", "run-3", "crashed", now.Add(-time.Minute))
	signals := crash.Signals("wt", now)
	if len(signals) != 1 || signals[0].Kind != LoopCrashLoop || signals[0].Count != 3 {
		t.Fatalf("crash signals = %+v, want one crash_loop of 3", signals)
	}

	thrash := NewLoopTracker()
	for i, runID := range []string{"run-1", "run-2", "run-3"} {
		thrash.RecordStart("wt", "api", runID, "spec-a", "head-a", now.Add(-time.Duration(3-i)*time.Minute))
	}
	signals = thrash.Signals("wt", now)
	if len(signals) != 1 || signals[0].Kind != LoopRestartThrash || signals[0].Count != 3 {
		t.Fatalf("thrash signals = %+v, want one restart_thrash of 3", signals)
	}

	changed := NewLoopTracker()
	changed.RecordStart("wt", "api", "run-1", "spec-a", "head-a", now.Add(-3*time.Minute))
	changed.RecordStart("wt", "api", "run-2", "spec-a", "head-a", now.Add(-2*time.Minute))
	changed.RecordStart("wt", "api", "run-3", "spec-b", "head-b", now.Add(-time.Minute))
	if signals := changed.Signals("wt", now); len(signals) != 0 {
		t.Fatalf("signals = %+v, want none after the spec and code changed", signals)
	}
}

func TestReduceWorldRevisionIgnoresReadTimeButNotCode(t *testing.T) {
	head := "head-a"
	group := testGroup(state.Service{
		Name: "api", Running: true, RunID: strPointer("run-1"), CodeHead: &head,
	})
	first := ReduceWorld(WorldInput{
		Seq: 1, ObservedAt: "2026-07-01T12:00:00Z", Group: group,
		Observation: &state.Observation{State: state.ObservationObservable},
	})
	second := ReduceWorld(WorldInput{
		Seq: 9, ObservedAt: "2026-07-01T12:05:00Z", Group: group,
		Observation: &state.Observation{State: state.ObservationObservable},
	})
	if first.Revision == "" || first.Revision != second.Revision {
		t.Fatalf("revision = %q and %q, want one stable value", first.Revision, second.Revision)
	}
	head = "head-b"
	third := ReduceWorld(WorldInput{
		Seq: 9, ObservedAt: "2026-07-01T12:05:00Z", Group: group,
		Observation: &state.Observation{State: state.ObservationObservable},
	})
	if third.Revision == first.Revision {
		t.Fatal("revision did not change when the run's code head changed")
	}
}

func TestReduceWorldDistinguishesSingleCrashFromCrashLoop(t *testing.T) {
	group := testGroup(state.Service{
		Name:     "api",
		LastExit: &state.ServiceExit{Code: 1, Reason: "crashed", RunID: "run-1"},
	})
	single := ReduceWorld(WorldInput{
		Group:       group,
		Observation: &state.Observation{State: state.ObservationObservable},
	})
	if len(single.Diagnosis.RootCauses) != 1 || single.Diagnosis.RootCauses[0].Kind != DriftServiceCrashed {
		t.Fatalf("single crash root causes = %+v, want service_crashed", single.Diagnosis.RootCauses)
	}

	looped := ReduceWorld(WorldInput{
		Group:       group,
		Observation: &state.Observation{State: state.ObservationObservable},
		Loops: []LoopSignal{{
			Kind: LoopCrashLoop, Service: "api", Count: 3, WindowSec: 600,
			Summary: "3 crashes in 10m", Recommendation: "oberth logs api --once",
		}},
	})
	if len(looped.Diagnosis.RootCauses) != 1 || looped.Diagnosis.RootCauses[0].Kind != DriftRepeatedCrash {
		t.Fatalf("crash loop root causes = %+v, want repeated_crash", looped.Diagnosis.RootCauses)
	}
}
