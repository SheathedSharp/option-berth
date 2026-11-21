package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClaudeHooksSkipsWhenNoManifestApplies(t *testing.T) {
	env := fakeEnv(t)
	env.Project = t.TempDir()
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusSkip)
	if !strings.Contains(row.Summary, "no oberth.yaml") {
		t.Fatalf("summary = %q", row.Summary)
	}
}

func TestClaudeHooksWarnsWhenSettingsJSONIsMissing(t *testing.T) {
	env := integrationProject(t)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Summary, "project Claude hooks are not enabled") {
		t.Fatalf("summary = %q", row.Summary)
	}
	if !strings.Contains(row.Detail, "settings.json") {
		t.Fatalf("detail = %q", row.Detail)
	}
	if strings.Contains(row.Detail, "settings.local.json") {
		t.Fatalf("detail mentioned settings.local.json: %q", row.Detail)
	}
	if !strings.Contains(row.Fix, "print-config") || !strings.Contains(row.Fix, ".claude/settings.json") {
		t.Fatalf("fix = %q", row.Fix)
	}
}

func TestClaudeHooksIgnoresSettingsLocal(t *testing.T) {
	env := integrationProject(t)
	local := filepath.Join(env.Project, ".claude", "settings.local.json")
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, local, `{"hooks":{"Stop":[{"hooks":[{"type":"command","command":"oberth agent claude-hook --project ."}]}]}}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Summary, "project Claude hooks are not enabled") {
		t.Fatalf("summary = %q, local-only hooks must not count", row.Summary)
	}
}

func TestClaudeHooksOKWhenSettingsJSONEnablesTheCommand(t *testing.T) {
	env := integrationProject(t)
	path := filepath.Join(env.Project, ".claude", "settings.json")
	write(t, path, `{
  "hooks": {
    "UserPromptSubmit": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo","timeout":20}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusOK)
	if !strings.Contains(row.Detail, path) {
		t.Fatalf("detail = %q, want the settings.json path", row.Detail)
	}
}

func TestClaudeHooksDoesNotCreateSettings(t *testing.T) {
	env := integrationProject(t)
	_ = run(t, env, checkClaudeHooks)
	if _, err := os.Stat(filepath.Join(env.Project, ".claude", "settings.json")); !os.IsNotExist(err) {
		t.Fatal("the check created settings.json")
	}
	if _, err := os.Stat(filepath.Join(env.Project, ".claude", "settings.local.json")); !os.IsNotExist(err) {
		t.Fatal("the check created settings.local.json")
	}
}
