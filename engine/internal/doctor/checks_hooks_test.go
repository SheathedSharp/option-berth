package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

func TestClaudeHooksWarnsWhenHookTimeoutIsShorterThanTheCommandBudget(t *testing.T) {
	env := integrationProject(t)
	path := filepath.Join(env.Project, ".claude", "settings.json")
	write(t, path, `{
  "hooks": {
    "Stop": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo --lease 10m","timeout":10}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Summary, "hook budget is too short") {
		t.Fatalf("summary = %q", row.Summary)
	}
	if !strings.Contains(row.Detail, path) || !strings.Contains(row.Detail, "Stop timeout is 10s") {
		t.Fatalf("detail = %q", row.Detail)
	}
	if !strings.Contains(row.Fix, "print-config") || !strings.Contains(row.Fix, "timeout at 20") {
		t.Fatalf("fix = %q", row.Fix)
	}
	if !strings.Contains(row.Fix, "cannot stop Claude") {
		t.Fatalf("fix = %q, want a clear limit of a new binary", row.Fix)
	}
}

func TestClaudeHooksWarnsWhenLeaseIsShorterThanTheCommandBudget(t *testing.T) {
	env := integrationProject(t)
	write(t, filepath.Join(env.Project, ".claude", "settings.json"), `{
  "hooks": {
    "Stop": [{"hooks":[{"type":"command","command":"'/usr/bin/oberth' 'agent' 'claude-hook' '--project' '/tmp/demo' '--lease' '10s'","timeout":20}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Detail, "Stop --lease is 10s") {
		t.Fatalf("detail = %q", row.Detail)
	}
	if strings.Contains(row.Detail, "timeout is") {
		t.Fatalf("timeout 20 should not be reported as short: %q", row.Detail)
	}
}

func TestClaudeHooksOKWhenTimeoutIsOmitted(t *testing.T) {
	env := integrationProject(t)
	write(t, filepath.Join(env.Project, ".claude", "settings.json"), `{
  "hooks": {
    "UserPromptSubmit": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo --lease 10m0s"}]}],
    "Stop": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo --lease 10m0s"}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusOK)
	if strings.Contains(row.Summary, "too short") || strings.Contains(row.Detail, "no timeout") {
		t.Fatalf("omitted timeout must not be treated as a short budget: summary=%q detail=%q", row.Summary, row.Detail)
	}
}

func TestClaudeHooksWarnsForShortLeaseEvenWhenTimeoutIsOmitted(t *testing.T) {
	env := integrationProject(t)
	write(t, filepath.Join(env.Project, ".claude", "settings.json"), `{
  "hooks": {
    "Stop": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo --lease 10s"}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusWarn)
	if !strings.Contains(row.Detail, "Stop --lease is 10s") {
		t.Fatalf("detail = %q", row.Detail)
	}
	if strings.Contains(row.Detail, "no timeout") {
		t.Fatalf("omitted timeout must not be reported as a short budget: %q", row.Detail)
	}
}

func TestClaudeHooksStillOKWithSufficientTimeoutAndDefaultLease(t *testing.T) {
	env := integrationProject(t)
	write(t, filepath.Join(env.Project, ".claude", "settings.json"), `{
  "hooks": {
    "Stop": [{"hooks":[{"type":"command","command":"oberth agent claude-hook --project /tmp/demo --lease 10m0s","timeout":20}]}]
  }
}`)
	row := run(t, env, checkClaudeHooks)
	wantStatus(t, row, StatusOK)
}

func TestSplitHookCommandReadsQuotedLease(t *testing.T) {
	tokens := splitHookCommand("'/usr/bin/oberth' 'agent' 'claude-hook' '--lease' '10s'")
	lease, ok := hookCommandLease(strings.Join([]string{
		"'/usr/bin/oberth'", "'agent'", "'claude-hook'", "'--lease'", "'10s'",
	}, " "))
	if !ok || lease != 10*time.Second {
		t.Fatalf("quoted lease = %v %v, tokens=%v", lease, ok, tokens)
	}
	lease, ok = hookCommandLease("oberth agent claude-hook --lease=15s")
	if !ok || lease != 15*time.Second {
		t.Fatalf("--lease= form = %v %v", lease, ok)
	}
}
