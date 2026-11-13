package scanner

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func serviceWithHealth(name, health string, running bool, port *int) state.Service {
	svc := state.Service{Name: name, Running: running, PortActual: port}
	if health != "" {
		svc.Health = strPtr(health)
	}
	return svc
}

// A declared check has no verdict until its first probe. The run is registered
// and `starting` is gone by then, so the only honest answer is unknown/pending:
// without it the row had no health at all, and a reader could not tell "not
// probed yet" from "probe unavailable".
func TestDeclaredHealthIsPendingBeforeTheFirstVerdict(t *testing.T) {
	port := 3000
	gg := []state.Group{{Name: "demo", Services: []state.Service{
		serviceWithHealth("http", "/healthz", true, &port),
		serviceWithHealth("worker", "command: pg_isready", true, nil),
		serviceWithHealth("stopped", "/healthz", false, &port),
		{Name: "unchecked", Running: true, PortActual: &port},
	}}}

	syncConfiguredHealth(nil, gg)

	httpSvc := gg[0].Services[0]
	if httpSvc.HealthStatus == nil || httpSvc.HealthStatus.Status != state.HealthUnknown ||
		httpSvc.HealthStatus.Reason != "pending" {
		t.Fatalf("http health = %+v, want unknown/pending", httpSvc.HealthStatus)
	}
	if httpSvc.HealthStatus.ObservedAt != "" {
		t.Fatalf("pending health claims an observation: %+v", httpSvc.HealthStatus)
	}
	if worker := gg[0].Services[1]; worker.HealthStatus == nil || worker.HealthStatus.Reason != "pending" {
		t.Fatalf("worker health = %+v, want pending", worker.HealthStatus)
	}
	if stopped := gg[0].Services[2]; stopped.HealthStatus != nil {
		t.Fatalf("stopped health = %+v, want none", stopped.HealthStatus)
	}
	if unchecked := gg[0].Services[3]; unchecked.HealthStatus != nil {
		t.Fatalf("unchecked health = %+v, want none", unchecked.HealthStatus)
	}
}

// A verdict already carried by the listener or written by a command probe
// replaces pending; pending only fills the absence of a verdict.
func TestConfiguredVerdictReplacesPending(t *testing.T) {
	port := 3000
	httpSvc := serviceWithHealth("http", "/healthz", true, &port)
	commandSvc := serviceWithHealth("worker", "command: true", true, nil)
	commandSvc.HealthStatus = &state.Health{Status: state.HealthFail, Reason: "command-failed"}
	gg := []state.Group{{Name: "demo", Services: []state.Service{httpSvc, commandSvc}}}
	rows := []state.Port{{
		Port: 3000, BindAddress: "127.0.0.1",
		Health: &state.Health{Status: state.HealthOK, Configured: true},
	}}

	syncConfiguredHealth(rows, gg)

	if h := gg[0].Services[0].HealthStatus; h == nil || h.Status != state.HealthOK {
		t.Fatalf("http health = %+v, want the listener verdict", h)
	}
	if h := gg[0].Services[1].HealthStatus; h == nil || h.Status != state.HealthFail {
		t.Fatalf("command health = %+v, want the carried verdict", h)
	}
}

// applyHealthRound runs the join before command verdicts are applied, so the
// pending placeholder written by the join cannot outlive a real probe result.
func TestCommandVerdictReplacesPendingInOneRound(t *testing.T) {
	gg := []state.Group{{Name: "demo", Services: []state.Service{
		serviceWithHealth("worker", "command: pg_isready", true, nil),
	}}}
	syncConfiguredHealth(nil, gg)
	if h := gg[0].Services[0].HealthStatus; h == nil || h.Reason != "pending" {
		t.Fatalf("preparation did not publish pending: %+v", h)
	}

	applyCommandHealth(gg, []healthTarget{{
		group: 0, service: 0, kind: groups.HealthCommand,
	}}, &healthRound{
		results: []state.Health{{Status: state.HealthFail, Reason: "command-failed"}},
		probed:  []bool{true},
	})

	if h := gg[0].Services[0].HealthStatus; h == nil || h.Status != state.HealthFail {
		t.Fatalf("health = %+v, want the probe verdict", h)
	}
}
