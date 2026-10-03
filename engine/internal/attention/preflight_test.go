package attention

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeStatusFixture(t *testing.T, home, root string, drafts []SnapshotDraft) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "status.json")
	rootDir := root
	snapshot := StatusSnapshot{}
	snapshot.Scope.Root = root
	snapshot.Worktree.Name = "demo"
	snapshot.Worktree.Branch = "codex/test"
	snapshot.Worktree.RootDir = &rootDir
	snapshot.Worktree.Services = []SnapshotService{{Name: "api"}, {Name: "worker"}}
	snapshot.Drafts = drafts
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPreflightBindsActionToFreshStatusAndFixedTarget(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	statusPath := writeStatusFixture(t, home, root, nil)
	snapshot, err := ReadStatusSnapshot(statusPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request, err := DerivePreflight(snapshot, Intent{
		Action:         "restart_service",
		Service:        "api",
		UserPermission: "The user allowed this proposal to be shown; ask before restart",
	}, home, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if request.Schema != PreflightSchema || request.Target.Service != "api" || request.Target.Project != "demo" {
		t.Fatalf("request = %+v", request)
	}
	if request.RequestID == "" || request.StateRevision == "" || len(request.Options) < 3 {
		t.Fatalf("identity/options missing: %+v", request)
	}
	for _, option := range request.Options {
		if !KnownOptionID(option.ID) {
			t.Fatalf("unknown option in preflight: %q", option.ID)
		}
	}
	path, err := WritePreflight(home, request)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ReadPreflight(path)
	if err != nil || got.RequestID != request.RequestID {
		t.Fatalf("read preflight = %+v, err=%v", got, err)
	}
	if err := ValidatePreflight(got, snapshot, home, time.Now().UTC()); err != nil {
		t.Fatalf("fresh preflight rejected: %v", err)
	}
	if _, err := DerivePreflight(snapshot, Intent{Action: "restart_service", Service: "missing", UserPermission: "ask"}, home, time.Now().UTC()); err == nil {
		t.Fatal("undeclared service target was accepted")
	}
}

func TestPreflightAdoptRequiresCurrentDraftUnderBerthHome(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	draftDir := filepath.Join(home, "drafts")
	if err := os.MkdirAll(draftDir, 0o700); err != nil {
		t.Fatal(err)
	}
	draftPath := filepath.Join(draftDir, "demo.yaml")
	if err := os.WriteFile(draftPath, []byte("name: demo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusPath := writeStatusFixture(t, home, root, []SnapshotDraft{{Group: "demo", Path: draftPath, Root: root}})
	snapshot, err := ReadStatusSnapshot(statusPath, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request, err := DerivePreflight(snapshot, Intent{Action: "adopt_manifest", DraftGroup: "demo", UserPermission: "show the selected draft first"}, home, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if request.Target.DraftHash == "" || request.Target.DraftPath != draftPath || request.Target.DraftGroup != "demo" {
		t.Fatalf("draft target = %+v", request.Target)
	}
	if err := ValidatePreflight(request, snapshot, home, time.Now().UTC()); err != nil {
		t.Fatalf("draft preflight rejected: %v", err)
	}
	if err := os.WriteFile(draftPath, []byte("name: changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ValidatePreflight(request, snapshot, home, time.Now().UTC()); !errors.Is(err, ErrStale) {
		t.Fatalf("changed draft err = %v, want ErrStale", err)
	}
}

func TestPreflightRejectsUnsafeDraftAndProjectSelectors(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	snapshot, err := ReadStatusSnapshot(writeStatusFixture(t, home, root, nil), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DerivePreflight(snapshot, Intent{Action: "stop_service", Service: "api", UserPermission: "ask"}, home, time.Now().UTC()); err == nil {
		t.Fatal("service selector was accepted for project-wide stop")
	}
	if _, err := DerivePreflight(snapshot, Intent{Action: "adopt_manifest", DraftGroup: "demo", UserPermission: "ask"}, home, time.Now().UTC()); err == nil {
		t.Fatal("missing current draft was accepted")
	}
	if _, err := DerivePreflight(snapshot, Intent{Action: "run_shell", UserPermission: "ask"}, home, time.Now().UTC()); err == nil {
		t.Fatal("arbitrary action was accepted")
	}
}

func TestPreflightAssessmentUsesFixedOptions(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "repo")
	snapshot, err := ReadStatusSnapshot(writeStatusFixture(t, home, root, nil), time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	request, err := DerivePreflight(snapshot, Intent{Action: "start_service", Service: "api", UserPermission: "ask first"}, home, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	needs := 0.95
	if err := request.AttachAssessment(Assessment{NeedsHuman: &needs, NextOption: "start_service", Probabilities: map[string]float64{"start_service": 0.8, "inspect_manifest": 0.1, "continue_without_action": 0.1}}); err != nil {
		t.Fatal(err)
	}
	if request.Assessment == nil || request.Assessment.NextOption != "start_service" {
		t.Fatalf("assessment = %+v", request.Assessment)
	}
	if err := request.AttachAssessment(Assessment{NextOption: "run_arbitrary_shell"}); err == nil {
		t.Fatal("arbitrary preflight option was accepted")
	}
}
