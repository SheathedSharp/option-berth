package agentlaunch

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const fixtureID = "12345678-1234-4123-8123-123456789abc"

func sessionFixture(t *testing.T, provider, root string) string {
	t.Helper()
	var entry any
	switch provider {
	case "codex":
		entry = map[string]any{"type": "session_meta", "payload": map[string]string{"id": fixtureID, "cwd": root, "cli_version": "0.42.0"}}
	case "claude":
		entry = map[string]any{"type": "user", "sessionId": fixtureID, "cwd": root, "version": "2.0.0", "message": map[string]string{"content": "private-canary"}}
	case "pi":
		entry = map[string]any{"type": "session", "id": fixtureID, "cwd": root, "version": 3}
	case "opencode":
		entry = map[string]any{"info": map[string]string{"id": "ses_12345678abc", "directory": root, "version": "1.0.0"}, "messages": []string{"private-canary"}}
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

func TestExplicitResumeKeepsProviderIdentityWorktreeAndApprovals(t *testing.T) {
	for _, provider := range []string{"codex", "claude", "pi", "opencode"} {
		t.Run(provider, func(t *testing.T) {
			root, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := sessionFixture(t, provider, root)
			source, err = filepath.EvalSymlinks(source)
			if err != nil {
				t.Fatal(err)
			}
			prompt := "--continue $(not-a-shell)\n中文"
			plan, err := Build(Options{Provider: provider, Worktree: root, ResumeFile: source, Prompt: prompt}, func(name string) (string, error) { return filepath.Join(root, name), nil })
			if err != nil {
				t.Fatal(err)
			}
			if plan.Resume == nil || plan.Resume.ApprovalOwner != "provider_native" || plan.Resume.Worktree != root || plan.Resume.SourceVersion == "" {
				t.Fatalf("bad identity: %+v", plan.Resume)
			}
			var want []string
			switch provider {
			case "codex":
				want = []string{"resume", fixtureID, "--", prompt}
			case "claude":
				want = []string{"--resume", fixtureID, "--", prompt}
			case "pi":
				want = []string{"--session", source, "--", prompt}
			case "opencode":
				want = []string{"--session=ses_12345678abc", "--prompt=" + prompt}
			}
			if !reflect.DeepEqual(plan.Arguments, want) {
				t.Fatalf("argv=%q want %q", plan.Arguments, want)
			}
			encoded, _ := json.Marshal(plan)
			if strings.Contains(string(encoded), "private-canary") {
				t.Fatal("transcript leaked into plan")
			}
			if _, err := Build(Options{Provider: provider, Worktree: t.TempDir(), ResumeFile: source}, nil); err == nil || !strings.Contains(err.Error(), "worktree") {
				t.Fatal("cross-worktree resume was not refused before executable lookup", err)
			}
		})
	}
}

func TestResumeRejectsUnprovenIdentityAndMode(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(t.TempDir(), "session.jsonl")
	for _, raw := range []string{
		`{}`, `{"type":"session_meta","payload":{"id":"--last","cwd":"/","cli_version":"0.1.0"}}`,
		`{"type":"session_meta","payload":{"id":"` + fixtureID + `","cwd":"relative","cli_version":"0.1.0"}}`,
		`{"type":"session_meta","payload":{"id":"` + fixtureID + `","cwd":"/","cli_version":"future-schema"}}`,
		strings.Repeat("x", 256*1024),
	} {
		if err := os.WriteFile(file, []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		looked := false
		_, err := Build(Options{Provider: "codex", Worktree: root, ResumeFile: file}, func(string) (string, error) { looked = true; return "", nil })
		if err == nil || looked {
			t.Fatal("invalid metadata reached provider lookup", err)
		}
		if strings.Contains(err.Error(), raw) {
			t.Fatal("raw input in error")
		}
	}
	valid := sessionFixture(t, "codex", root)
	for _, opts := range []Options{
		{Provider: "codex", Worktree: root, ResumeFile: valid, Mode: "task", Prompt: "test"},
		{Provider: "deepseek", Worktree: root, ResumeFile: valid},
		{Provider: "codex", Worktree: root, ResumeFile: root},
		{Provider: "codex", Worktree: root, ResumeFile: "relative.jsonl"},
	} {
		if _, err := Build(opts, nil); err == nil {
			t.Fatal("unsupported resume accepted")
		}
	}
}

func TestResumeDoesNotReadTheWholeTranscript(t *testing.T) {
	root := t.TempDir()
	path := sessionFixture(t, "codex", root)
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, err = file.WriteString(strings.Repeat("private-message", 1024*1024))
	file.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Build(Options{Provider: "codex", Worktree: root, ResumeFile: path}, func(string) (string, error) { return filepath.Join(root, "codex"), nil }); err != nil {
		t.Fatal(err)
	}
}
