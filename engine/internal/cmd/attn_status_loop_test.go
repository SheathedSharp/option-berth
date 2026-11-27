package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

func attnStatusText(t *testing.T, home, project string, args ...string) string {
	t.Helper()
	resetAttnStatusFlags(t)
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN", "")
	t.Setenv("BERTH_ATTN_KEY", "")
	t.Setenv("BERTH_ATTN_ENDPOINT", "")
	t.Setenv("BERTH_ATTN_MODEL", "")
	argv := []string{"attn", "status", "--project", project}
	argv = append(argv, args...)
	rootCmd.SetArgs(argv)
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn status %v: %v\n%s", argv[2:], err, out)
	}
	return out
}

func writeEnabledAttnSettings(t *testing.T, home string) {
	t.Helper()
	body := `{"schema":"oberth.attn-settings/v1","enabled":true,"endpoint":"https://assess.example/decisions","model":"test/model","api_key":"k"}`
	if err := os.WriteFile(filepath.Join(home, "attn.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func initFakeCheckout(t *testing.T, root string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestAttnStatusOmitsProjectLoopWhenNotAGitWorktree(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeEnabledAttnSettings(t, home)
	out := attnStatusText(t, home, project)
	if strings.Contains(out, "hooks") || strings.Contains(out, "delivery") || strings.Contains(out, "artifact") {
		t.Fatalf("non-git directory still reported a project loop:\n%s", out)
	}

	resetAttnStatusFlags(t)
	t.Setenv("BERTH_HOME", home)
	rootCmd.SetArgs([]string{"attn", "status", "--json", "--project", project})
	var err error
	raw := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hooks", "delivery", "artifact"} {
		if _, ok := doc[field]; ok {
			t.Fatalf("json %s present outside a git worktree: %v", field, doc[field])
		}
	}
}

func TestAttnStatusReportsUnconsumedHooksAndUnobservedArtifact(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	initFakeCheckout(t, project)
	checkout, inRepo := groups.Locate(project)
	if !inRepo {
		t.Fatal("expected a fake checkout")
	}
	project = checkout.Root
	writeEnabledAttnSettings(t, home)

	// Local-only hooks must not count as opt-in.
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.local.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"oberth agent claude-hook"}]}]}}`), 0o600); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second)
	artifactAt := now.Add(time.Hour)
	if _, err := model.AppendTelemetry(home, model.TelemetryRecord{
		Kind: "assessment", Project: "example", SessionID: "session-one",
		Status: "human_required", EventID: "event-one", StateRevision: "rev-one",
		ModelCalls: 1, RecordedAt: now.Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}

	key := strings.Repeat("ab", 32)
	dir := filepath.Join(home, attn.AttentionDir, "claude", key)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	lease := claudehook.Lease{
		WorktreeID: model.WorktreeID(project),
		Consumed:   false,
		Finished:   true,
		Status:     "stopped",
		Reason:     "lease_expired",
		Expires:    now.Add(10 * time.Second),
		Task:       model.TaskContext{Task: "private-task-must-not-appear"},
		Pending:    &claudehook.Feedback{Body: "private-finding-must-not-appear"},
	}
	data, err := json.Marshal(lease)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "lease.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(home, attn.AttentionDir), 0o700); err != nil {
		t.Fatal(err)
	}
	artifact := `{
		"schema":"oberth.attn/v1",
		"event_id":"event-one",
		"state_revision":"rev-one",
		"worktree_root":"` + project + `",
		"generated_at":"` + artifactAt.Format(time.RFC3339Nano) + `",
		"fresh_until":"` + artifactAt.Add(10*time.Minute).Format(time.RFC3339Nano) + `",
		"first_seen":"` + artifactAt.Add(-14*time.Second).Format(time.RFC3339) + `",
		"last_seen":"` + artifactAt.Format(time.RFC3339) + `",
		"event":{"kind":"ready_timeout","service":"secret-service","at":"` + artifactAt.Add(-15*time.Minute).Format(time.RFC3339) + `"},
		"evidence":[{"ref":"secret-ref","log_path":"/secret/log"}],
		"options":[{"id":"inspect_logs"}]
	}`
	if err := os.WriteFile(attn.Path(home, project), []byte(artifact), 0o600); err != nil {
		t.Fatal(err)
	}

	out := attnStatusText(t, home, project)
	for _, want := range []string{
		"last status human_required",
		"no committed .claude/settings.json enabling oberth claude-hook",
		"1 hook session(s); 0 consumed, 1 not consumed",
		"last reason lease_expired",
		"published ready_timeout at " + artifactAt.Format(time.RFC3339),
		"no observation followed (not an automatic assessment)",
		"print-config",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	for _, leaked := range []string{"private-task-must-not-appear", "private-finding-must-not-appear", "secret-service", "/secret/log"} {
		if strings.Contains(out, leaked) {
			t.Fatalf("leaked %q in:\n%s", leaked, out)
		}
	}

	resetAttnStatusFlags(t)
	t.Setenv("BERTH_HOME", home)
	rootCmd.SetArgs([]string{"attn", "status", "--json", "--project", project})
	var jsonErr error
	raw := captureStdout(t, func() { jsonErr = rootCmd.Execute() })
	if jsonErr != nil {
		t.Fatal(jsonErr)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		t.Fatal(err)
	}
	hooks, _ := doc["hooks"].(map[string]any)
	if hooks["enabled"] != false || hooks["settings_present"] != false {
		t.Fatalf("hooks = %v", hooks)
	}
	delivery, _ := doc["delivery"].(map[string]any)
	if delivery["sessions"] != float64(1) || delivery["consumed"] != float64(0) || delivery["last_reason"] != "lease_expired" {
		t.Fatalf("delivery = %v", delivery)
	}
	artifactDoc, _ := doc["artifact"].(map[string]any)
	if artifactDoc["present"] != true || artifactDoc["event_kind"] != "ready_timeout" || artifactDoc["observed_after"] != false {
		t.Fatalf("artifact = %v", artifactDoc)
	}
}

func TestAttnStatusReportsEnabledProjectHooks(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	initFakeCheckout(t, project)
	checkout, inRepo := groups.Locate(project)
	if !inRepo {
		t.Fatal("expected a fake checkout")
	}
	project = checkout.Root
	writeEnabledAttnSettings(t, home)
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"oberth agent claude-hook --project ` + project + `"}]}]}}`
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	out := attnStatusText(t, home, project)
	if !strings.Contains(out, "project .claude/settings.json enables oberth claude-hook") {
		t.Fatalf("enabled hooks not reported:\n%s", out)
	}
	if strings.Contains(out, "print-config") {
		t.Fatalf("suggested hook opt-in even though hooks are enabled:\n%s", out)
	}
}

func TestAttnStatusDoesNotTreatPermissionAllowListAsHookOptIn(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	initFakeCheckout(t, project)
	checkout, inRepo := groups.Locate(project)
	if !inRepo {
		t.Fatal("expected a fake checkout")
	}
	project = checkout.Root
	writeEnabledAttnSettings(t, home)
	if err := os.MkdirAll(filepath.Join(project, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	settings := `{"permissions":{"allow":["Bash(oberth agent claude-hook:*)"]}}`
	if err := os.WriteFile(filepath.Join(project, ".claude", "settings.json"), []byte(settings), 0o600); err != nil {
		t.Fatal(err)
	}
	out := attnStatusText(t, home, project)
	if strings.Contains(out, "enables oberth claude-hook") {
		t.Fatalf("permissions allow-list was treated as hook opt-in:\n%s", out)
	}
	if !strings.Contains(out, "does not enable oberth claude-hook") {
		t.Fatalf("missing not-enabled diagnosis:\n%s", out)
	}
	if !strings.Contains(out, "print-config") {
		t.Fatalf("should still suggest reviewing print-config:\n%s", out)
	}
}

func TestAttnStatusQualityFlagPrintsLedgerInText(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	writeEnabledAttnSettings(t, home)
	if _, err := model.AppendTelemetry(home, model.TelemetryRecord{
		Kind: "assessment", Project: "example", SessionID: "session-one",
		Status: "human_required", EventID: "event-one", StateRevision: "rev-one",
		ModelCalls: 1, RecordedAt: time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
	}); err != nil {
		t.Fatal(err)
	}
	plain := attnStatusText(t, home, project)
	if strings.Contains(plain, "this ledger cannot say whether Attention helped") {
		t.Fatalf("default text already printed the full ledger:\n%s", plain)
	}
	full := attnStatusText(t, home, project, "--quality")
	for _, want := range []string{
		"0 of 1 cases have a person's decision, so this ledger cannot say whether Attention helped",
		"0 recorded follow-through attempts",
		"human_required 1",
	} {
		if !strings.Contains(full, want) {
			t.Fatalf("missing %q in --quality text:\n%s", want, full)
		}
	}
}
