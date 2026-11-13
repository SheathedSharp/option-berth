package codefacts

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/codeid"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func stringPtr(s string) *string { return &s }

func TestAnnotateReportsChangedCode(t *testing.T) {
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

	Annotate(context.Background(), group, nil)
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
	Annotate(context.Background(), group, nil)
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

func TestAnnotateHonoursTheSelection(t *testing.T) {
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
	Annotate(context.Background(), group, map[string]bool{"api": true})
	if group.Services[0].CodeChangedSinceStart == nil {
		t.Fatal("selected service was not checked")
	}
	if group.Services[1].CodeChangedSinceStart != nil {
		t.Fatal("unselected service was hashed")
	}
}

func TestAnnotateLeavesUnknownCodeUnknown(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "app.py"), []byte("print('v1')\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A stopped service is not compared.
	stopped := &state.Group{Services: []state.Service{{
		Name: "api", RunID: stringPtr("run-1"),
	}}}
	Annotate(context.Background(), stopped, nil)
	if stopped.Services[0].CodeChangedSinceStart != nil {
		t.Fatal("a stopped service got a code verdict")
	}

	// A live run with no snapshot (an older run, a failed write, or sources:
	// []) is unknown, never "unchanged".
	missing := &state.Group{Services: []state.Service{{
		Name: "api", Running: true, RunID: stringPtr("run-2"),
	}}}
	Annotate(context.Background(), missing, nil)
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
	Annotate(context.Background(), redefined, nil)
	if redefined.Services[0].CodeChangedSinceStart != nil {
		t.Fatal("a changed source declaration was compared against the old baseline")
	}
}

// A directly-run binary rebuilt after the run started is the artifact
// question `sources:` cannot answer: build output is excluded from the source
// fingerprint. The executable facts compare the program's current mtime with
// the run's start time, with one second of tolerance for the published
// timestamp's precision.
func TestAnnotateReportsARebuiltExecutable(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "api")
	if err := os.WriteFile(exe, []byte("v1"), 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(exe, start, start); err != nil {
		t.Fatal(err)
	}
	group := &state.Group{Services: []state.Service{{
		Name: "api", Running: true,
		StartedAt:      stringPtr(start.Format(time.RFC3339)),
		ExecutablePath: stringPtr(exe),
	}}}

	Annotate(context.Background(), group, nil)
	svc := group.Services[0]
	if svc.ExecutableChangedSinceStart == nil || *svc.ExecutableChangedSinceStart {
		t.Fatalf("fresh executable = %+v, want an explicit false", svc)
	}
	if svc.ExecutableModifiedAt == nil {
		t.Fatal("the executable's mtime was not published")
	}

	rebuilt := start.Add(2 * time.Second)
	if err := os.Chtimes(exe, rebuilt, rebuilt); err != nil {
		t.Fatal(err)
	}
	Annotate(context.Background(), group, nil)
	svc = group.Services[0]
	if svc.ExecutableChangedSinceStart == nil || !*svc.ExecutableChangedSinceStart {
		t.Fatalf("rebuilt executable = %+v, want changed", svc)
	}
}

// A path that no longer exists or a run without a start time leaves the
// executable facts absent: unknown, not "unchanged".
func TestAnnotateLeavesExecutableUnknownWhenItCannotRead(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone")
	group := &state.Group{Services: []state.Service{{
		Name: "api", Running: true,
		StartedAt:      stringPtr("2026-11-13T10:00:00Z"),
		ExecutablePath: stringPtr(missing),
	}}}
	Annotate(context.Background(), group, nil)
	if svc := group.Services[0]; svc.ExecutableChangedSinceStart != nil || svc.ExecutableModifiedAt != nil {
		t.Fatalf("missing executable = %+v, want unknown", svc)
	}
}
