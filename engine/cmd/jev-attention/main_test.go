package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
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

// Consent is the one behavior change in this package: a key is a secret, not
// permission. These tables pin the rule the old default* helpers encoded by
// accident — environment keys used to switch model on by their mere presence.
func TestConsentComesOnlyFromAnExplicitEnable(t *testing.T) {
	const endpoint, modelName = "https://assess.example/decisions", "test/model"
	enabled := model.LocalConfig{Schema: model.LocalConfigSchema, Enabled: true, Preset: "openrouter", Endpoint: endpoint, Model: modelName, APIKey: "settings-key"}
	disabled := model.LocalConfig{Schema: model.LocalConfigSchema, Enabled: false, Preset: "openrouter", Endpoint: endpoint, Model: modelName, APIKey: "settings-key"}
	cases := []struct {
		name        string
		input       model.ResolveInput
		wantEnabled bool
		wantReason  string
		wantKey     string
	}{
		{
			name:        "environment key alone is not consent",
			input:       model.ResolveInput{Key: "env-key"},
			wantEnabled: false,
			wantReason:  model.ReasonDisabled,
		},
		{
			name:        "settings enabled is consent and supplies its own key",
			input:       model.ResolveInput{Settings: enabled, SettingsPresent: true},
			wantEnabled: true,
			wantKey:     "settings-key",
		},
		{
			name:        "settings enabled falls back to the environment key",
			input:       model.ResolveInput{Settings: enabled, SettingsPresent: true, Key: "env-key"},
			wantEnabled: true,
			wantKey:     "env-key",
		},
		{
			name:        "explicitly disabled settings are not overridden by a key",
			input:       model.ResolveInput{Settings: disabled, SettingsPresent: true, Key: "env-key"},
			wantEnabled: false,
			wantReason:  model.ReasonDisabled,
		},
		{
			name:        "one-shot BERTH_ATTN is consent without a settings file",
			input:       model.ResolveInput{OneShot: "1", Endpoint: endpoint, Model: modelName, Key: "env-key"},
			wantEnabled: true,
			wantKey:     "env-key",
		},
		{
			name:        "BERTH_ATTN=0 is not consent",
			input:       model.ResolveInput{OneShot: "0", Endpoint: endpoint, Model: modelName, Key: "env-key"},
			wantEnabled: false,
			wantReason:  model.ReasonDisabled,
		},
		{
			name:        "enabled settings with no key anywhere reports no_key",
			input:       model.ResolveInput{Settings: model.LocalConfig{Schema: model.LocalConfigSchema, Enabled: true, Endpoint: endpoint, Model: modelName}, SettingsPresent: true},
			wantEnabled: true,
			wantReason:  model.ReasonNoKey,
		},
		{
			name:        "a malformed settings file is a closed door, not a fallthrough",
			input:       model.ResolveInput{SettingsPresent: true, SettingsErr: errors.New("bad json"), Key: "env-key"},
			wantEnabled: false,
			wantReason:  model.ReasonInvalidConfig,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := model.Resolve(tc.input)
			if got.Enabled != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v (%+v)", got.Enabled, tc.wantEnabled, got)
			}
			if got.Reason != tc.wantReason {
				t.Fatalf("reason = %q, want %q", got.Reason, tc.wantReason)
			}
			if tc.wantKey != "" && got.Key != tc.wantKey {
				t.Fatalf("key = %q, want %q", got.Key, tc.wantKey)
			}
			if !got.Enabled && got.Key != "" {
				t.Fatalf("a disabled configuration still resolved a key: %q", got.Key)
			}
		})
	}
}

func TestResolvedClientRefusesWithoutConsent(t *testing.T) {
	withoutConsent := model.Resolved{Key: "k", Endpoint: "https://assess.example/decisions", Model: "test/model", Reason: model.ReasonDisabled}
	if _, err := withoutConsent.Client(); err == nil {
		t.Fatal("a disabled configuration produced a client")
	}
	enabled := model.Resolved{Enabled: true, Key: "k", Endpoint: "https://assess.example/decisions", Model: "test/model", Timeout: time.Second}
	client, err := enabled.Client()
	if err != nil {
		t.Fatalf("enabled configuration: %v", err)
	}
	if client.APIKey != "k" || client.Endpoint != "https://assess.example/decisions" {
		t.Fatalf("client = %+v", client)
	}
}

func TestRedactedStateNeverCarriesTheKey(t *testing.T) {
	resolved := model.Resolve(model.ResolveInput{Settings: model.LocalConfig{
		Schema: model.LocalConfigSchema, Enabled: true, Preset: "openrouter",
		Endpoint: "https://assess.example/decisions", Model: "test/model", APIKey: "secret-key",
	}, SettingsPresent: true})
	state := resolved.RedactedState()
	encoded, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret-key") {
		t.Fatalf("redacted state leaks the key: %s", encoded)
	}
	if state["key_source"] != string(model.SourceSettings) {
		t.Fatalf("key_source = %v", state["key_source"])
	}
}

func TestLocalSettingsSupplyEndpointModelAndKey(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "attn.json"), []byte(`{
      "schema":"oberth.attn-settings/v1",
      "enabled":true,
      "preset":"openrouter",
      "endpoint":"https://local.example/decisions",
      "model":"local/model",
      "api_key":"local-key",
      "timeout_ms":5000
    }`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := resolveConsent("", false)
	if !got.Enabled || got.Preset != "openrouter" || got.Endpoint != "https://local.example/decisions" || got.Model != "local/model" {
		t.Fatalf("resolved = %+v", got)
	}
	if got.Key != "local-key" || got.Timeout != 5*time.Second {
		t.Fatalf("key/timeout = %q/%s", got.Key, got.Timeout)
	}
	if got.KeySource != model.SourceSettings {
		t.Fatalf("key source = %q", got.KeySource)
	}
}

func TestEnvironmentFieldsSupplyEndpointAndModelButNotConsent(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN_KEY", "environment-key")
	t.Setenv("BERTH_ATTN_ENDPOINT", "https://env.example/decisions")
	t.Setenv("BERTH_ATTN_MODEL", "env/model")
	got := resolveConsent("", false)
	if got.Enabled {
		t.Fatalf("an environment key enabled the assessment: %+v", got)
	}
	if got.Endpoint != "https://env.example/decisions" || got.Model != "env/model" {
		t.Fatalf("environment fields = %+v", got)
	}
	if got.Key != "" {
		t.Fatalf("a disabled configuration leaked the key: %q", got.Key)
	}
}

func TestExplicitAPIKeyFlagWinsAndCanDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN_KEY", "")
	if err := os.WriteFile(filepath.Join(home, "attn.json"), []byte(`{"schema":"oberth.attn-settings/v1","enabled":true,"preset":"openrouter","endpoint":"https://assess.example/decisions","model":"test/model","api_key":"local-key"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveConsent("", false); got.Key != "local-key" {
		t.Fatalf("unset flag = %q, want the configured key", got.Key)
	}
	if got := resolveConsent("flag-key", true); got.Key != "flag-key" || got.KeySource != model.SourceFlag {
		t.Fatalf("explicit value = %+v, want it verbatim", got)
	}
	if got := resolveConsent("", true); got.Key != "" {
		t.Fatalf("explicit empty value = %q, want the key disabled", got.Key)
	}
}

func TestRunPreflightWritesFactRequestWhenModelUnavailable(t *testing.T) {
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
	// No consent: the request must still be written, and no provider contacted.
	disabled := model.Resolved{Endpoint: "https://assess.example/decisions", Model: "test/model", Timeout: time.Second, Reason: model.ReasonDisabled}
	if err := runPreflight(statusPath, "", "restart_service", "api", "", "ask before restart", false, disabled, model.TaskContext{}); err != nil {
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
	got := preflightTaskContext(request, model.TaskContext{Task: string(make([]byte, 4000))})
	if len([]byte(got.Task)) > 2000 || got.AuthorizedOptions[0] != "restart_service" {
		t.Fatalf("task context = %+v", got)
	}
	if !strings.Contains(got.Task, "restart_service") || !strings.Contains(got.Task, "ask first") {
		t.Fatalf("intent context omitted: %q", got.Task)
	}
}
