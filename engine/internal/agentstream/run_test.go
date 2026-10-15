package agentstream

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const resumeFixtureID = "12345678-1234-4123-8123-123456789abc"

func writeCodexResumeFile(t *testing.T, worktree string) string {
	t.Helper()
	entry := map[string]any{
		"type": "session_meta",
		"payload": map[string]string{
			"id": resumeFixtureID, "cwd": worktree, "cli_version": "0.42.0",
		},
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunRejectsResumeWorktreeMismatch(t *testing.T) {
	recorded := t.TempDir()
	selected := t.TempDir()
	file := writeCodexResumeFile(t, recorded)
	err := Run(Options{
		Provider: "codex", Worktree: selected, ResumeFile: file,
	}, strings.NewReader(""), io.Discard, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "worktree") {
		t.Fatalf("expected worktree mismatch error, got %v", err)
	}
}
