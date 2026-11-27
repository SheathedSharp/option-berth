package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestStatusDiagnosisCollapsesADependencyCascade(t *testing.T) {
	port := 3000
	doc := statusDocument{
		Scope: display.Scope{Root: "/tmp/shop"},
		Worktree: state.Group{Name: "shop", Services: []state.Service{
			{
				Name: "auth", Port: &port,
				LastExit: &state.ServiceExit{
					Code: 1, Reason: "port_occupied", RunID: "auth-1", PortHint: port,
					PortHolder: &state.PortHolder{Port: port, PID: 4242, Kind: state.PortHolderUnknown},
				},
			},
			{Name: "api", DependsOn: []string{"auth"},
				LastExit: &state.ServiceExit{Code: 1, Reason: "dependency_not_ready", RunID: "api-1"}},
		}},
		Observation: &state.Observation{State: state.ObservationObservable},
	}
	diagnosis := statusDiagnosis(doc)
	if diagnosis == nil {
		t.Fatal("a dependency cascade produced no diagnosis")
	}
	if len(diagnosis.RootCauses) != 1 || diagnosis.RootCauses[0].Service != "auth" {
		t.Fatalf("root causes = %+v, want one on auth", diagnosis.RootCauses)
	}
	if got := diagnosis.RootCauses[0].Impacted; len(got) != 1 || got[0] != "api" {
		t.Fatalf("impacted = %v, want [api]", got)
	}
}

func TestStatusDiagnosisStaysAbsentForACleanRunningWorktree(t *testing.T) {
	port := 3000
	doc := statusDocument{
		Worktree: state.Group{Name: "shop", Services: []state.Service{{
			Name: "api", Running: true, RunID: stringPointer("run-1"), Port: &port, PortActual: &port,
		}}},
		Observation: &state.Observation{State: state.ObservationObservable},
	}
	if diagnosis := statusDiagnosis(doc); diagnosis != nil {
		t.Fatalf("diagnosis = %+v, want nil for a clean world", diagnosis)
	}
}

func TestPrintStatusDiagnosisShowsImpactedServices(t *testing.T) {
	doc := statusDocument{Diagnosis: &runtimecore.CausalDiagnosis{
		RootCauses: []runtimecore.RootCause{{
			Service:  "auth",
			Kind:     runtimecore.DriftPortOccupied,
			Severity: runtimecore.SeverityCritical,
			Summary:  "port 3000 is held by pid 4242",
			Impacted: []string{"api", "gateway"},
			Command:  "oberth status --json",
		}},
	}}
	out := captureStdout(t, func() { printStatusDiagnosis(doc) })
	for _, want := range []string{"auth", "port_occupied", "impacted", "api, gateway", "oberth status --json"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnosis output = %q, want %q", out, want)
		}
	}
}

func TestStatusDiagnosisIncludesFirstErrorFromRunLog(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "auth.log")
	prev := "ERROR: stale run error\n"
	curr := "listening failed\nFATAL: missing DATABASE_URL\n"
	if err := os.WriteFile(logPath, []byte(prev+curr), 0o600); err != nil {
		t.Fatal(err)
	}
	offset := int64(len(prev))
	code := 1
	doc := statusDocument{
		Scope: display.Scope{Root: "/tmp/shop"},
		Worktree: state.Group{Name: "shop", Services: []state.Service{
			{
				Name:     "auth",
				LastExit: &state.ServiceExit{Code: 1, Reason: "crashed", RunID: "auth-2"},
			},
		}},
		Exits: []rpc.RunRecord{{
			ID:        "auth-2",
			Name:      "auth",
			Reason:    "crashed",
			ExitCode:  &code,
			LogPath:   logPath,
			LogOffset: &offset,
		}},
		Observation: &state.Observation{State: state.ObservationObservable},
	}
	diagnosis := statusDiagnosis(doc)
	if diagnosis == nil || len(diagnosis.RootCauses) != 1 {
		t.Fatalf("diagnosis = %+v, want one root cause", diagnosis)
	}
	if diagnosis.RootCauses[0].FirstError != "FATAL: missing DATABASE_URL" {
		t.Fatalf("first_error = %q, want %q", diagnosis.RootCauses[0].FirstError, "FATAL: missing DATABASE_URL")
	}
	doc.Diagnosis = diagnosis
	out := captureStdout(t, func() { printStatusDiagnosis(doc) })
	if !strings.Contains(out, "FATAL: missing DATABASE_URL") {
		t.Fatalf("human status output omitted first error:\n%s", out)
	}
}

func stringPointer(value string) *string { return &value }
