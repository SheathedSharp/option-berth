package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/jev"
)

func TestBindFeedbackIdentityUsesCanonicalProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "example-worker")
	input := jev.FeedbackInput{Project: "example-worker", WorktreeID: jev.WorktreeID(root)}
	if err := bindFeedbackIdentity(&input, root); err != nil {
		t.Fatal(err)
	}
	if input.Project != "example-worker" || input.WorktreeID != jev.WorktreeID(root) {
		t.Fatalf("identity = %+v", input)
	}
	input.WorktreeID = jev.WorktreeID(filepath.Join(t.TempDir(), "other"))
	if err := bindFeedbackIdentity(&input, root); err == nil {
		t.Fatal("mismatched worktree was accepted")
	}
}

func TestRunFeedbackBindsProjectAndRejectsMismatch(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "example-worker")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", home)
	feedbackPath := filepath.Join(t.TempDir(), "feedback.json")
	data, err := json.Marshal(jev.FeedbackInput{
		Schema:  jev.FeedbackSchema,
		EventID: "event-1", StateRevision: "revision-1", HumanNeeded: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedbackPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runFeedback(feedbackPath, "", root); err != nil {
		t.Fatal(err)
	}
	records, err := jev.ReadTelemetry(home)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	if records[0].Project != "example-worker" || records[0].WorktreeID != jev.WorktreeID(root) {
		t.Fatalf("record identity = %+v", records[0])
	}
	bad := jev.FeedbackInput{Schema: jev.FeedbackSchema, EventID: "event-2", StateRevision: "revision-2", WorktreeID: jev.WorktreeID(filepath.Join(t.TempDir(), "other")), HumanNeeded: boolPtr(true)}
	badData, _ := json.Marshal(bad)
	if err := os.WriteFile(feedbackPath, badData, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runFeedback(feedbackPath, "", root); err == nil {
		t.Fatal("mismatched feedback was accepted")
	}
}

func boolPtr(value bool) *bool { return &value }
