package runner

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

func TestBindFeedbackIdentityUsesCanonicalProject(t *testing.T) {
	root := filepath.Join(t.TempDir(), "example-worker")
	input := model.FeedbackInput{Project: "example-worker", WorktreeID: model.WorktreeID(root)}
	if err := bindFeedbackIdentity(&input, root); err != nil {
		t.Fatal(err)
	}
	if input.Project != "example-worker" || input.WorktreeID != model.WorktreeID(root) {
		t.Fatalf("identity = %+v", input)
	}
	input.WorktreeID = model.WorktreeID(filepath.Join(t.TempDir(), "other"))
	if err := bindFeedbackIdentity(&input, root); err == nil {
		t.Fatal("mismatched worktree was accepted")
	}
}

func TestFeedbackBindsProjectAndRejectsMismatch(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "example-worker")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BERTH_HOME", home)
	feedbackPath := filepath.Join(t.TempDir(), "feedback.json")
	data, err := json.Marshal(model.FeedbackInput{
		Schema:  model.FeedbackSchema,
		EventID: "event-1", StateRevision: "revision-1", HumanNeeded: boolPtr(true),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(feedbackPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Feedback(feedbackPath, "", root); err != nil {
		t.Fatal(err)
	}
	records, err := model.ReadTelemetry(home)
	if err != nil || len(records) != 1 {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
	if records[0].Project != "example-worker" || records[0].WorktreeID != model.WorktreeID(root) {
		t.Fatalf("record identity = %+v", records[0])
	}
	bad := model.FeedbackInput{Schema: model.FeedbackSchema, EventID: "event-2", StateRevision: "revision-2", WorktreeID: model.WorktreeID(filepath.Join(t.TempDir(), "other")), HumanNeeded: boolPtr(true)}
	badData, _ := json.Marshal(bad)
	if err := os.WriteFile(feedbackPath, badData, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Feedback(feedbackPath, "", root); err == nil {
		t.Fatal("mismatched feedback was accepted")
	}
}

func boolPtr(value bool) *bool { return &value }
