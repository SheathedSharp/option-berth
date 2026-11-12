package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

func resetAttnPreflightFlags(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		attnPreflightStatusFlag = ""
		attnPreflightFileFlag = ""
		attnPreflightActionFlag = ""
		attnPreflightServiceFlag = ""
		attnPreflightDraftFlag = ""
		attnPreflightPermissionFlag = ""
		attnPreflightApprovedFlag = false
		attnTaskFlag = ""
		attnTaskFileFlag = ""
		attnAuthorizeFlag = nil
	})
}

// Without consent the bounded request is still the product: the agent gets the
// fact document to present, and the provider is never contacted.
func TestAttnPreflightWritesFactRequestWithoutConsent(t *testing.T) {
	resetAttnPreflightFlags(t)
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	root := filepath.Join(t.TempDir(), "repo")
	statusPath := filepath.Join(t.TempDir(), "status.json")
	status := map[string]any{
		"scope": map[string]any{"root": root},
		"worktree": map[string]any{
			"name": "demo", "branch": "codex/test", "root_dir": root,
			"services": []any{map[string]any{"name": "api", "running": false}},
		},
		"drafts": []any{},
	}
	data, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statusPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	attnPreflightStatusFlag = statusPath
	attnPreflightActionFlag = "restart_service"
	attnPreflightServiceFlag = "api"
	attnPreflightPermissionFlag = "ask before restart"
	if err := runAttnPreflight(attnPreflightCmd, nil); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(home, "attention", "preflight", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("preflight entries = %v, err=%v", entries, err)
	}
	request, err := model.ReadPreflight(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if request.Intent.Action != "restart_service" || request.Target.Service != "api" || request.Assessment != nil {
		t.Fatalf("request = %+v", request)
	}
}

func TestPreflightContextOffersOnlyFixedRequestOptions(t *testing.T) {
	request := model.Preflight{
		Intent:  model.Intent{Action: "restart_service"},
		Options: []attn.Option{{ID: "restart_service"}, {ID: "inspect_manifest"}, {ID: "continue_without_action"}},
	}
	ctx := preflightTaskContext(request, model.TaskContext{Task: "prepare the integration test"})
	if ctx.Task == "" || len(ctx.AuthorizedOptions) != len(request.Options) {
		t.Fatalf("preflight context = %+v", ctx)
	}
	for _, option := range request.Options {
		found := false
		for _, authorized := range ctx.AuthorizedOptions {
			if authorized == option.ID {
				found = true
			}
		}
		if !found {
			t.Fatalf("preflight context omitted fixed option %q: %+v", option.ID, ctx)
		}
	}
}

func TestPreflightTaskContextIncludesIntentWithoutExceedingBound(t *testing.T) {
	request := model.Preflight{
		Intent: model.Intent{Action: "restart_service", UserPermission: "ask first"},
		Target: model.PreflightTarget{Project: "demo", Service: "api"},
	}
	got := preflightTaskContext(request, model.TaskContext{Task: strings.Repeat("x", 4000)})
	if len([]byte(got.Task)) > 2000 || got.AuthorizedOptions[0] != "restart_service" {
		t.Fatalf("task context = %+v", got)
	}
	if !strings.Contains(got.Task, "restart_service") || !strings.Contains(got.Task, "ask first") {
		t.Fatalf("intent context omitted: %q", got.Task)
	}
}
