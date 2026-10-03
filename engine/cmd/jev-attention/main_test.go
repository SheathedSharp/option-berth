package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/attention"
)

func TestLoadContextRequiresExplicitTaskAndAuthorization(t *testing.T) {
	if _, err := loadContext("", "", "inspect_logs"); err == nil {
		t.Fatal("missing task was accepted")
	}
	partial, err := loadContext("", "fix service", "")
	if err == nil {
		t.Fatal("missing authorization was accepted")
	}
	if partial.Task != "fix service" {
		t.Fatalf("partial task context = %+v, want task preserved for preflight", partial)
	}
	got, err := loadContext("", "fix service", " inspect_logs, restart_service ")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "fix service" || len(got.AuthorizedOptions) != 2 {
		t.Fatalf("context = %+v", got)
	}
}

func TestLoadContextFileAndFlagOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(path, []byte(`{"task":"old task","authorized_options":["inspect_logs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadContext(path, "new task", "restart_service")
	if err != nil {
		t.Fatal(err)
	}
	if got.Task != "new task" || len(got.AuthorizedOptions) != 1 || got.AuthorizedOptions[0] != "restart_service" {
		t.Fatalf("context = %+v", got)
	}
}

func TestSafeReasonDoesNotExposeProviderDetails(t *testing.T) {
	if got := safeReason(attention.ErrUnavailable); got != "Jev unavailable" {
		t.Fatalf("reason = %q", got)
	}
}

func TestOpenRouterEnvironmentSelectsJevDefaults(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "openrouter-test-key")
	t.Setenv("OPENROUTER_ENDPOINT", "")
	t.Setenv("OPENROUTER_MODEL", "")
	t.Setenv("TYPESAFE_API_KEY", "typesafe-test-key")
	t.Setenv("TYPESAFE_ENDPOINT", "https://typesafe.example/systemone")
	t.Setenv("TYPESAFE_MODEL", "jev-test")
	if got := defaultEndpoint(); got != attention.DefaultOpenRouterEndpoint {
		t.Fatalf("endpoint = %q, want OpenRouter decisions endpoint", got)
	}
	if got := defaultModel(); got != attention.DefaultOpenRouterModel {
		t.Fatalf("model = %q, want pinned OpenRouter Jev model", got)
	}
	if got := defaultAPIKey(); got != "openrouter-test-key" {
		t.Fatalf("api key = %q, want OpenRouter key", got)
	}
}

func TestLocalJevSettingsSelectOpenRouterDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "jev.json"), []byte(`{
      "schema":"oberth.jev-config/v1",
      "enabled":true,
      "provider":"openrouter",
      "endpoint":"https://local.example/decisions",
      "model":"local/jev",
      "api_key":"local-key",
      "timeout_ms":5000
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultEndpoint(); got != "https://local.example/decisions" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := defaultModel(); got != "local/jev" {
		t.Fatalf("model = %q", got)
	}
	if got := defaultAPIKey(); got != "local-key" {
		t.Fatalf("api key = %q", got)
	}
	if got := defaultTimeout(); got != 5*time.Second {
		t.Fatalf("timeout = %s", got)
	}
}

func TestEnvironmentKeyOverridesLocalJevSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	if err := os.WriteFile(filepath.Join(home, "jev.json"), []byte(`{"schema":"oberth.jev-config/v1","enabled":true,"provider":"openrouter","api_key":"local-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENROUTER_API_KEY", "environment-key")
	t.Setenv("OPENROUTER_ENDPOINT", "https://env.example/decisions")
	t.Setenv("OPENROUTER_MODEL", "env/jev")
	if got := defaultAPIKey(); got != "environment-key" {
		t.Fatalf("api key = %q", got)
	}
	if got := defaultEndpoint(); got != "https://env.example/decisions" {
		t.Fatalf("endpoint = %q", got)
	}
	if got := defaultModel(); got != "env/jev" {
		t.Fatalf("model = %q", got)
	}
}

func TestDisabledLocalJevSettingsDoNotFallThroughToLegacyKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "jev.json"), []byte(`{"schema":"oberth.jev-config/v1","enabled":false,"api_key":"local-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "decide.key"), []byte("legacy-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultAPIKey(); got != "" {
		t.Fatalf("disabled config returned key %q", got)
	}
}

func TestLegacyJevKeyRemainsAvailableWithoutNewSettings(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "decide.key"), []byte("legacy-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := defaultAPIKey(); got != "legacy-key" {
		t.Fatalf("legacy api key = %q", got)
	}
	if got := defaultEndpoint(); got != attention.DefaultOpenRouterEndpoint {
		t.Fatalf("legacy endpoint = %q", got)
	}
}

func TestEffectiveAPIKeyPrefersExplicitFlagOverConfiguredKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("OPENROUTER_API_KEY", "")
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "jev.json"), []byte(`{"schema":"oberth.jev-config/v1","enabled":true,"provider":"openrouter","api_key":"local-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := effectiveAPIKey("", false); got != "local-key" {
		t.Fatalf("unset flag = %q, want the configured key", got)
	}
	if got := effectiveAPIKey("flag-key", true); got != "flag-key" {
		t.Fatalf("explicit value = %q, want it verbatim", got)
	}
	if got := effectiveAPIKey("", true); got != "" {
		t.Fatalf("explicit empty value = %q, want the key disabled", got)
	}
}

func TestRunPreflightWritesFactRequestWhenJevUnavailable(t *testing.T) {
	home := t.TempDir()
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
	t.Setenv("BERTH_HOME", home)
	if err := runPreflight(statusPath, "", "restart_service", "api", "", "ask before restart", false, attention.DefaultJevEndpoint, attention.DefaultJevModel, "", time.Second, attention.TaskContext{}); err != nil {
		t.Fatal(err)
	}
	entries, err := filepath.Glob(filepath.Join(home, "attention", "preflight", "*.json"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("preflight entries = %v, err=%v", entries, err)
	}
	request, err := attention.ReadPreflight(entries[0])
	if err != nil {
		t.Fatal(err)
	}
	if request.Intent.Action != "restart_service" || request.Target.Service != "api" || request.Assessment != nil {
		t.Fatalf("request = %+v", request)
	}
}

func TestPreflightContextOffersOnlyFixedRequestOptions(t *testing.T) {
	request := attention.Preflight{
		Intent:  attention.Intent{Action: "restart_service"},
		Options: []attention.Option{{ID: "restart_service"}, {ID: "inspect_manifest"}, {ID: "continue_without_action"}},
	}
	ctx := preflightTaskContext(request, attention.TaskContext{Task: "prepare the integration test"})
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
	request := attention.Preflight{
		Intent: attention.Intent{Action: "restart_service", UserPermission: "ask first"},
		Target: attention.PreflightTarget{Project: "demo", Service: "api"},
	}
	got := preflightTaskContext(request, attention.TaskContext{Task: string(make([]byte, 4000))})
	if len([]byte(got.Task)) > 2000 || got.AuthorizedOptions[0] != "restart_service" {
		t.Fatalf("task context = %+v", got)
	}
	if !strings.Contains(got.Task, "restart_service") || !strings.Contains(got.Task, "ask first") {
		t.Fatalf("intent context omitted: %q", got.Task)
	}
}
