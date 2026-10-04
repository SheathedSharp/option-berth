package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
)

func TestAgentPlanReadsPromptWithoutExecutingProvider(t *testing.T) {
	if os.PathSeparator == '\\' {
		t.Skip("fixture is an executable Unix script")
	}
	root := t.TempDir()
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "must-not-run")
	body := "#!/bin/sh\n: > '" + marker + "'\nexit 71\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("SYNTHETIC_AGENT_CREDENTIAL", "not-for-plan-output")
	prompt := "--flag \"quotes\"; $(never)\nnext line"
	command := newAgentCommand()
	command.SetArgs([]string{"plan", "codex", "--worktree", root, "--prompt-stdin", "--json"})
	command.SetIn(strings.NewReader(prompt))
	output := captureStdout(t, func() {
		if err := command.Execute(); err != nil {
			t.Fatal(err)
		}
	})
	var plan agentlaunch.Plan
	if err := json.Unmarshal([]byte(output), &plan); err != nil {
		t.Fatal(err)
	}
	if len(plan.Arguments) != 2 || plan.Arguments[1] != prompt {
		t.Fatal("prompt was reinterpreted")
	}
	if strings.Contains(output, "not-for-plan-output") {
		t.Fatal("environment leaked into plan")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("planning ran external tool")
	}
}
