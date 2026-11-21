package cmd

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
)

func TestClaudeHookPrintConfigTimeoutMatchesCommandBudget(t *testing.T) {
	if claudehook.HookCommandTimeout <= model.DefaultTimeout {
		t.Fatalf("hook command budget %s must exceed Attention client timeout %s", claudehook.HookCommandTimeout, model.DefaultTimeout)
	}
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("isolated git init: %s %v", out, err)
	}
	rootCmd.SetArgs([]string{"agent", "claude-hook", "--project", dir, "--print-config"})
	t.Cleanup(func() { rootCmd.SetArgs(nil) })
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("print-config: %v\n%s", err, out)
	}
	var doc struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Timeout int `json:"timeout"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if e := json.Unmarshal([]byte(out), &doc); e != nil {
		t.Fatalf("parse print-config: %v\n%s", e, out)
	}
	want := int(claudehook.HookCommandTimeout.Seconds())
	if want <= 0 {
		t.Fatal("timeout seconds must be positive")
	}
	for event, groups := range doc.Hooks {
		if len(groups) == 0 || len(groups[0].Hooks) == 0 {
			t.Fatalf("event %s missing hook entry", event)
		}
		if got := groups[0].Hooks[0].Timeout; got != want {
			t.Fatalf("event %s timeout=%d, want %d (command budget)", event, got, want)
		}
	}
	for _, event := range []string{"UserPromptSubmit", "Stop", "StopFailure", "SessionEnd"} {
		if _, ok := doc.Hooks[event]; !ok {
			t.Fatalf("missing event %s", event)
		}
	}
}
