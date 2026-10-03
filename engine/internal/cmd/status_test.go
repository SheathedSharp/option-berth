package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/attention"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestPublishAttentionWritesFactArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	code := 1
	root := t.TempDir()
	doc := statusDocument{
		Scope: display.Scope{Root: root},
		Worktree: state.Group{Services: []state.Service{{
			Name: "api", LastExit: &state.ServiceExit{Code: code, Reason: "start_failed"},
		}}},
	}
	path := publishAttention(&doc)
	if path == "" {
		t.Fatal("publishAttention returned no path")
	}
	artifact, err := attention.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if doc.AttentionRevision != artifact.StateRevision {
		t.Fatalf("attention_revision = %q, artifact state_revision = %q", doc.AttentionRevision, artifact.StateRevision)
	}
	if artifact.Event.Kind != attention.EventServiceFailed || artifact.Assessment != nil {
		t.Fatalf("artifact = %+v, want fact-only service failure", artifact)
	}
	if err := attention.Clear(home, root); err != nil {
		t.Fatal(err)
	}
	if _, err := attention.Read(path); err == nil {
		t.Fatal("cleared attention artifact still exists")
	}
}

func TestPublishAttentionPreservesAssessmentOnStableRefresh(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := t.TempDir()
	doc := statusDocument{
		Scope: display.Scope{Root: root},
		Worktree: state.Group{Services: []state.Service{{
			Name: "api", LastExit: &state.ServiceExit{Code: 1, Reason: "start_failed", RunID: "run-1"},
		}}},
	}
	path := publishAttention(&doc)
	if path == "" {
		t.Fatal("publishAttention returned no path")
	}
	needsHuman := 0.9
	if err := attention.UpdateAssessment(path, attention.Assessment{NeedsHuman: &needsHuman, NextOption: "restart_service"}); err != nil {
		t.Fatal(err)
	}
	if got := publishAttention(&doc); got != path {
		t.Fatalf("path changed across refresh: %q -> %q", path, got)
	}
	artifact, err := attention.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Assessment == nil || artifact.Assessment.NextOption != "restart_service" {
		t.Fatalf("assessment lost across status refresh: %+v", artifact.Assessment)
	}
}

func TestPublishAttentionCarriesScopedPortsAndHistory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := t.TempDir()
	declared, actual := 3000, 3001
	projectRoot := root
	doc := statusDocument{
		Scope: display.Scope{Root: root},
		Worktree: state.Group{Services: []state.Service{{
			Name: "api", Running: true, Port: &declared, PortActual: &actual,
		}}},
		Ports: []state.Port{
			{Port: 9000, Cwd: root, ProjectRoot: &projectRoot},
			{Port: 9001, Cwd: t.TempDir()},
		},
		Exits: []rpc.RunRecord{{Name: "api", ID: "run-1", Reason: "crashed", ExitedAt: "2026-09-27T00:59:00Z"}},
	}
	path := publishAttention(&doc)
	if path == "" {
		t.Fatal("publishAttention returned no path")
	}
	artifact, err := attention.Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Events) != 2 {
		t.Fatalf("events = %+v, want conflict and scoped undeclared listener", artifact.Events)
	}
	if artifact.Events[0].Kind != attention.EventManifestConflict || artifact.Events[1].Kind != attention.EventUndeclared {
		t.Fatalf("events = %+v", artifact.Events)
	}
}

func TestPublishAttentionClearsAfterRecovery(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := t.TempDir()
	failed := statusDocument{
		Scope: display.Scope{Root: root},
		Worktree: state.Group{Services: []state.Service{{
			Name: "api", LastExit: &state.ServiceExit{Reason: "crashed"},
		}}},
	}
	path := publishAttention(&failed)
	if path == "" {
		t.Fatal("publishAttention returned no path")
	}
	recovered := failed
	recovered.Worktree.Services = []state.Service{{Name: "api", Running: true}}
	if got := publishAttention(&recovered); got != "" {
		t.Fatalf("recovery returned attention path %q", got)
	}
	if recovered.AttentionPath != "" || recovered.AttentionRevision != "" {
		t.Fatalf("recovery left status handoff fields: path=%q revision=%q", recovered.AttentionPath, recovered.AttentionRevision)
	}
	if _, err := attention.Read(path); err == nil {
		t.Fatal("recovery left stale attention artifact")
	}
}

func TestPrintStatusFailureShowsEveryRetainedRun(t *testing.T) {
	code1, code2 := 127, 7
	out := captureStdout(t, func() {
		printStatusFailure(statusDocument{Exits: []rpc.RunRecord{
			{Name: "missing", Reason: "start_failed", ExitCode: &code1, LastLines: []string{"exec: command not found"}},
			{Name: "crashed", Reason: "crashed", ExitCode: &code2, LastLines: []string{"worker exited"}},
		}})
	})
	if !strings.Contains(out, "missing") || !strings.Contains(out, "crashed") {
		t.Fatalf("failure output = %q, want both service failures", out)
	}
	if strings.Count(out, "failed") != 2 {
		t.Fatalf("failure output = %q, want two failure rows", out)
	}
	if strings.Count(out, "next") != 2 || !strings.Contains(out, "oberth logs missing --once") {
		t.Fatalf("failure output = %q, want one actionable next step per failure", out)
	}
}

func TestPrintStatusFailureSkipsRoutineStops(t *testing.T) {
	code := 137
	out := captureStdout(t, func() {
		printStatusFailure(statusDocument{Exits: []rpc.RunRecord{
			{Name: "forced", Reason: "stopped", ExitCode: &code, LastLines: []string{"last line before SIGKILL"}},
			{Name: "clean", Reason: "exited", ExitCode: &code, LastLines: []string{"finished"}},
		}})
	})
	if out != "" {
		t.Fatalf("routine stop output = %q, want no failure or next-step rows", out)
	}
}

func TestPrintStatusFailureStillSuggestsLogsWithoutTail(t *testing.T) {
	code := 1
	out := captureStdout(t, func() {
		printStatusFailure(statusDocument{Exits: []rpc.RunRecord{
			{Name: "silent", Reason: "start_failed", ExitCode: &code},
		}})
	})
	if !strings.Contains(out, "silent") || !strings.Contains(out, "no log lines kept") ||
		!strings.Contains(out, "oberth logs silent --once") {
		t.Fatalf("failure output = %q, want evidence placeholder and next step", out)
	}
}

func TestPrintStatusServicesUsesOneRowPerService(t *testing.T) {
	port := 18121
	autoPort := 22000
	crashCode := 17
	out := captureStdout(t, func() {
		printStatusServices(statusDocument{Worktree: state.Group{Services: []state.Service{
			{Name: "api", Running: true, PortActual: &port},
			{Name: "worker", Running: true, PID: intPtr(4242)},
			{Name: "gateway", PortAuto: true},
			{Name: "crashed", LastExit: &state.ServiceExit{Reason: "crashed", Code: crashCode}},
			{Name: "stopped", LastExit: &state.ServiceExit{Reason: "stopped", Code: 137}},
			{Name: "auto-running", Running: true, PortActual: &autoPort, PortAuto: true},
		}}})
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 6 {
		t.Fatalf("service output = %q, want one row for each service", out)
	}
	for _, want := range []string{
		"api · running · listening on 18121",
		"worker · running · no port · pid 4242",
		"gateway · not running · auto port unassigned",
		"crashed · failed · crashed · exit 17",
		"stopped · stopped",
		"auto-running · running · listening on 22000",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("service output = %q, want %q", out, want)
		}
	}
	if strings.Contains(out, "stopped · stopped · exit") {
		t.Errorf("service output = %q, stopped rows should not surface a signal code", out)
	}
}
