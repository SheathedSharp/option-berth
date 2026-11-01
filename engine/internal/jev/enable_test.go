package jev

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attention"
)

// `enable` is the one command that both writes a secret and proves it works.
// What has to hold: a probe that fails writes nothing, a written file is
// owner-only, and the key comes from the source the caller named.

func TestEnableKeySources(t *testing.T) {
	env := func(name string) string {
		if name == "JEV_KEY" {
			return "  secret-from-env  "
		}
		return ""
	}
	readLine := func() (string, error) { return "secret-from-stdin\n", nil }

	fromEnv := EnableOptions{KeyKind: KeyFromEnv, KeyEnv: "JEV_KEY"}
	got, err := fromEnv.ResolveKey(env, readLine)
	if err != nil || got != "secret-from-env" {
		t.Fatalf("env key = %q, err=%v", got, err)
	}

	fromStdin := EnableOptions{KeyKind: KeyFromStdin}
	got, err = fromStdin.ResolveKey(env, readLine)
	if err != nil || got != "secret-from-stdin" {
		t.Fatalf("stdin key = %q, err=%v", got, err)
	}

	fromPrompt := EnableOptions{KeyKind: KeyFromPrompt, KeyValue: " typed "}
	got, err = fromPrompt.ResolveKey(env, readLine)
	if err != nil || got != "typed" {
		t.Fatalf("prompt key = %q, err=%v", got, err)
	}

	// Every failure is an error, never an empty key that would silently disable
	// the provider the person just chose.
	if _, err := (EnableOptions{KeyKind: KeyFromEnv, KeyEnv: "MISSING"}).ResolveKey(env, readLine); err == nil {
		t.Fatal("an empty environment variable was accepted")
	}
	if _, err := (EnableOptions{KeyKind: KeyFromStdin}).ResolveKey(env, func() (string, error) {
		return "\n", nil
	}); err == nil {
		t.Fatal("empty standard input was accepted")
	}
	if _, err := (EnableOptions{KeyKind: KeyFromPrompt}).ResolveKey(env, readLine); err == nil {
		t.Fatal("an empty prompt answer was accepted")
	}
}

func TestWriteSettingsIsOwnerOnlyAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "jev.json")
	cfg := LocalJevConfig{
		Schema: LocalJevConfigSchema, Enabled: true, Provider: ProviderOpenRouter,
		APIKey: "test-key", TimeoutMS: 5000,
	}
	if err := WriteSettings(path, cfg); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %o, want 600", info.Mode().Perm())
	}
	parent, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if parent.Mode().Perm() != 0o700 {
		t.Fatalf("settings directory mode = %o, want 700", parent.Mode().Perm())
	}
	loaded, present, err := LoadLocalJevConfigFrom(path)
	if err != nil || !present {
		t.Fatalf("load = %+v, present=%v, err=%v", loaded, present, err)
	}
	if loaded.APIKey != "test-key" || !loaded.Enabled {
		t.Fatalf("loaded = %+v", loaded)
	}
}

// An empty key is not a configuration: `enable` must not publish one.
func TestWriteSettingsRefusesAnEmptyKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jev.json")
	if err := WriteSettings(path, LocalJevConfig{Schema: LocalJevConfigSchema, Enabled: true}); err == nil {
		t.Fatal("settings with an empty key were written")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("a refused write still created the file: %v", err)
	}
}

func TestDisableKeepsOrForgetsTheKey(t *testing.T) {
	dir := t.TempDir()
	cfg := LocalJevConfig{Schema: LocalJevConfigSchema, Enabled: true, Provider: ProviderOpenRouter, APIKey: "kept-key"}

	kept := filepath.Join(dir, "kept.json")
	if err := DisableSettings(kept, cfg, false); err != nil {
		t.Fatal(err)
	}
	loaded, _, err := LoadLocalJevConfigFrom(kept)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Enabled || loaded.APIKey != "kept-key" {
		t.Fatalf("disable kept = %+v, want disabled with the key", loaded)
	}

	forgotten := filepath.Join(dir, "forgotten.json")
	if err := DisableSettings(forgotten, cfg, true); err != nil {
		t.Fatal(err)
	}
	loaded, _, err = LoadLocalJevConfigFrom(forgotten)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Enabled || loaded.APIKey != "" {
		t.Fatalf("disable --forget = %+v, want disabled without the key", loaded)
	}
	// A forgotten key must not be resolvable afterwards: this is the whole
	// point of the flag.
	if resolved := Resolve(ResolveInput{Settings: loaded, SettingsPresent: true}); resolved.Enabled || resolved.Key != "" {
		t.Fatalf("forgotten settings still resolved a key: %+v", resolved)
	}
}

// The probe is a real request with a synthetic, redacted event. It must succeed
// on a working provider and classify a broken one.
func TestProbeOnceSucceedsAndClassifiesFailures(t *testing.T) {
	good := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"model":"jev-test","answers":{
			"task_relevant":{"type":"noul","noul":0.2},
			"needs_human":{"type":"noul","noul":0.8}}}`))
	}))
	defer good.Close()
	cfg := LocalJevConfig{Provider: ProviderTypeSafe, Endpoint: good.URL, APIKey: "k", TimeoutMS: 3000}
	result := ProbeWithClient(context.Background(), cfg, good.Client())
	if !result.OK {
		t.Fatalf("probe failed: %+v", result)
	}
	if result.Reason != "" || result.Text == "" {
		t.Fatalf("probe result = %+v", result)
	}

	bad := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	result = ProbeWithClient(context.Background(), LocalJevConfig{Provider: ProviderTypeSafe, Endpoint: bad.URL, APIKey: "k", TimeoutMS: 3000}, bad.Client())
	if result.OK || result.Reason != ReasonUnauthorized {
		t.Fatalf("failed probe = %+v, want unauthorized", result)
	}
	if result.Text == "" {
		t.Fatal("a failed probe has no next step")
	}
}

func TestKeyFingerprintNeverEchoesTheKey(t *testing.T) {
	const key = "sk-very-secret-value"
	fingerprint := KeyFingerprint(key)
	if fingerprint == "" || strings.Contains(fingerprint, key) {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	if KeyFingerprint("") != "" {
		t.Fatal("an empty key has a fingerprint")
	}
	if KeyFingerprint(key) != KeyFingerprint(key) {
		t.Fatal("fingerprint is not stable")
	}
}

// The preview has to show the real request shape, and it must not contain the
// task's absolute paths or the key.
func TestPreviewRequestShowsTheBoundaryWithoutSecrets(t *testing.T) {
	declared := 3000
	artifact := attention.Artifact{
		Schema: attention.Schema, EventID: "event-1", StateRevision: "rev-1",
		WorktreeRoot: "/Users/alice/work/private-project", Branch: "feature/x",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339Nano),
		FreshUntil:  time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		Event: attention.Event{
			Kind: attention.EventServiceFailed, Service: "api", Reason: "start_failed",
			DeclaredPort: &declared, At: time.Now().UTC().Format(time.RFC3339Nano),
		},
		Evidence: []attention.Evidence{{Ref: "status.worktree.services[api].last_exit", LogPath: "/Users/alice/work/private-project/api.log"}},
		Options:  []attention.Option{{ID: "inspect_logs"}, {ID: "restart_service"}},
	}
	body, err := PreviewRequest(artifact, TaskContext{
		Task: "fix the api startup", AuthorizedOptions: []string{"inspect_logs", "restart_service"},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	for _, forbidden := range []string{"/Users/alice", "private-project", "api.log"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("preview leaks %q: %s", forbidden, text)
		}
	}
	state := body["state"].(map[string]any)
	if state["worktree_id"] == "" || state["worktree_id"] == artifact.WorktreeRoot {
		t.Fatalf("worktree_id = %v", state["worktree_id"])
	}
	if state["model"] != "<the configured model>" {
		t.Fatalf("preview named a model: %v", state["model"])
	}
	questions := body["questions"].([]map[string]string)
	if len(questions) != 3 {
		t.Fatalf("questions = %v, want task_relevant, needs_human and next_option", questions)
	}
}

func TestPreviewRequiresAnAuthorizedOption(t *testing.T) {
	artifact := attention.Artifact{
		Options: []attention.Option{{ID: "inspect_logs"}},
		Event:   attention.Event{Kind: attention.EventServiceFailed, Service: "api"},
	}
	if _, err := PreviewRequest(artifact, TaskContext{
		Task: "fix it", AuthorizedOptions: []string{"stop_service"},
	}); err == nil {
		t.Fatal("preview accepted an authorization the event does not offer")
	}
}

// The boundary summary is data, so `enable --json` and the human text cannot
// disagree about what is sent.
func TestSendsSummaryCoversBothDirections(t *testing.T) {
	summary := SendsSummary()
	for _, key := range []string{"always", "never", "depends"} {
		list, ok := summary[key].([]string)
		if !ok || len(list) == 0 {
			t.Fatalf("summary %q = %v", key, summary[key])
		}
	}
	text := HumanSendsSummary()
	if !strings.Contains(text, "Will be sent") || !strings.Contains(text, "Will never be sent") {
		t.Fatalf("human summary = %q", text)
	}
	if !strings.Contains(text, "oberth jev preview") {
		t.Fatal("the human summary does not point at the exact body")
	}
}
