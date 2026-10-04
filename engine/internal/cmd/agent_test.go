package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// Invalid run flags must fail before reading terminal input or resolving a tool.
type forbiddenAgentInput struct{ reads int }

func (r *forbiddenAgentInput) Read(_ []byte) (int, error) {
	r.reads++
	return 0, fmt.Errorf("stdin must not be consumed")
}
func TestAgentRunRejectsPlanFlagsBeforeReadingInput(t *testing.T) {
	for _, flags := range [][]string{{"--prompt-stdin"}, {"--json", "--prompt-stdin"}, {"--json"}} {
		t.Run(strings.Join(flags, " "), func(t *testing.T) {
			input := &forbiddenAgentInput{}
			command := newAgentCommand()
			command.SetIn(input)
			command.SetOut(&bytes.Buffer{})
			command.SetErr(&bytes.Buffer{})
			command.SetArgs(append([]string{"run", "codex", "--worktree", t.TempDir()}, flags...))
			err := command.Execute()
			if _, ok := err.(usageError); !ok {
				t.Fatalf("expected usage error before any I/O, got %v", err)
			}
			if input.reads != 0 {
				t.Fatal("invalid run consumed stdin")
			}
		})
	}
}
