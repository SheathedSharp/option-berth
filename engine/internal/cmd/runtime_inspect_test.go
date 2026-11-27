package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestPrintRuntimeInspectShowsRootCausesAndImpactedServices(t *testing.T) {
	port := 3000
	world := runtimecore.ReduceWorld(runtimecore.WorldInput{
		ObservedAt: "2026-07-01T12:00:00Z",
		Group: state.Group{Name: "shop", Services: []state.Service{
			{
				Name: "auth", Port: &port,
				LastExit: &state.ServiceExit{
					Code: 1, Reason: "port_occupied", RunID: "auth-1", PortHint: port,
					PortHolder: &state.PortHolder{Port: port, PID: 4242, Command: "node server.js", Kind: state.PortHolderUnknown},
				},
			},
			{Name: "api", DependsOn: []string{"auth"}, LastExit: &state.ServiceExit{Code: 1, Reason: "dependency_not_ready"}},
		}},
		Observation: &state.Observation{State: state.ObservationObservable},
	})
	out := captureStdout(t, func() { printRuntimeInspect(world) })
	for _, want := range []string{"health", "broken", "root causes", "auth", "impacted: api", "4242", "next:"} {
		if !strings.Contains(out, want) {
			t.Errorf("runtime inspect output = %q, want %q", out, want)
		}
	}
}

func TestRuntimeInspectWorldReducesStatusFactsOffline(t *testing.T) {
	demoManifest(t)
	prev := noRuntimeServiceFlag
	noRuntimeServiceFlag = true
	t.Cleanup(func() { noRuntimeServiceFlag = prev })
	blindObservation(t)

	world, err := runtimeInspectWorld(context.Background(), ".")
	if err != nil {
		t.Fatalf("runtimeInspectWorld: %v", err)
	}
	if world.Worktree.Group != "demo" {
		t.Fatalf("group = %q, want demo", world.Worktree.Group)
	}
	if world.Coverage != runtimecore.CoverageBlind {
		t.Fatalf("coverage = %q, want blind", world.Coverage)
	}
	if len(world.Services) != 1 || world.Services[0].State != runtimecore.ServiceUnobserved {
		t.Fatalf("services = %+v, want the declared service unobserved", world.Services)
	}
}
