package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func stringPtr(s string) *string { return &s }

func TestAnnotateCodeFactsReportsChangedCode(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	app := filepath.Join(root, "app.py")
	if err := os.WriteFile(app, []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := codeid.Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if err := codeid.Save("run-1", snap); err != nil {
		t.Fatal(err)
	}
	group := &state.Group{Services: []state.Service{{
		Name: "api", Running: true, RunID: stringPtr("run-1"),
	}}}

	annotateCodeFacts(context.Background(), group, nil)
	svc := group.Services[0]
	if svc.CodeChangedSinceStart == nil || *svc.CodeChangedSinceStart {
		t.Fatalf("unchanged code = %+v, want an explicit false", svc)
	}
	if svc.CodeChangedFiles != nil || svc.CodeHint != nil {
		t.Fatalf("unchanged code carried a count or hint: %+v", svc)
	}

	if err := os.WriteFile(app, []byte("print('v2')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	annotateCodeFacts(context.Background(), group, nil)
	svc = group.Services[0]
	if svc.CodeChangedSinceStart == nil || !*svc.CodeChangedSinceStart {
		t.Fatalf("edited code = %+v, want changed", svc)
	}
	if svc.CodeChangedFiles == nil || *svc.CodeChangedFiles != 1 {
		t.Fatalf("changed files = %v, want 1", svc.CodeChangedFiles)
	}
	if svc.CodeHint == nil || *svc.CodeHint != "oberth restart --only api" {
		t.Fatalf("hint = %v", svc.CodeHint)
	}
}

func TestPrintStatusServicesShowsChangedCodeAndNextStep(t *testing.T) {
	changed := true
	files := 2
	hint := "oberth restart --only api"
	doc := statusDocument{Worktree: state.Group{Services: []state.Service{{
		Name: "api", Running: true,
		CodeChangedSinceStart: &changed, CodeChangedFiles: &files, CodeHint: &hint,
	}}}}
	out := captureStdout(t, func() { printStatus(doc) })
	for _, want := range []string{"code changed since start (2 files)", hint} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}

func TestAnnotateCodeFactsHonoursTheSelection(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := codeid.Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	for _, runID := range []string{"run-api", "run-worker"} {
		if err := codeid.Save(runID, snap); err != nil {
			t.Fatal(err)
		}
	}
	apiRun, workerRun := "run-api", "run-worker"
	group := &state.Group{Services: []state.Service{
		{Name: "api", Running: true, RunID: &apiRun},
		{Name: "worker", Running: true, RunID: &workerRun},
	}}
	annotateCodeFacts(context.Background(), group, map[string]bool{"api": true})
	if group.Services[0].CodeChangedSinceStart == nil {
		t.Fatal("selected service was not checked")
	}
	if group.Services[1].CodeChangedSinceStart != nil {
		t.Fatal("unselected service was hashed")
	}
}

func TestAnnotateCodeFactsLeavesUnknownCodeUnknown(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stopped service is not compared.
	stopped := &state.Group{Services: []state.Service{{
		Name: "api", RunID: stringPtr("run-1"),
	}}}
	annotateCodeFacts(context.Background(), stopped, nil)
	if stopped.Services[0].CodeChangedSinceStart != nil {
		t.Fatal("a stopped service got a code verdict")
	}

	// A live run with no snapshot (an older run, a failed write, or sources:
	// []) is unknown, never "unchanged".
	missing := &state.Group{Services: []state.Service{{
		Name: "api", Running: true, RunID: stringPtr("run-2"),
	}}}
	annotateCodeFacts(context.Background(), missing, nil)
	if missing.Services[0].CodeChangedSinceStart != nil {
		t.Fatal("a missing snapshot was reported as unchanged")
	}

	// The manifest changed the source set after the run started. The old
	// snapshot cannot answer the new question, so the field stays unknown
	// until a restart records a new baseline.
	snap, err := codeid.Take(context.Background(), root, []string{"."})
	if err != nil {
		t.Fatal(err)
	}
	if err := codeid.Save("run-3", snap); err != nil {
		t.Fatal(err)
	}
	runID := "run-3"
	redefined := &state.Group{Services: []state.Service{{
		Name: "api", Running: true, RunID: &runID, Sources: []string{"src"},
	}}}
	annotateCodeFacts(context.Background(), redefined, nil)
	if redefined.Services[0].CodeChangedSinceStart != nil {
		t.Fatal("a changed source declaration was compared against the old baseline")
	}
}
