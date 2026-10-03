package draft

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/paths"
)

// validAnswer is one schema-shaped answer — the document a CLI is asked to
// produce now, rather than a fenced block to be extracted from prose.
const validAnswer = `{"name":"proj","services":[{"name":"api","cmd":"echo hi","port":3999,` +
	`"why":"package.json scripts.dev","verified":"unverified"}],"notes":["the worker has no port"]}`

// fake is a shell stub standing in for an agent CLI. The structured flags hand
// it an answer path the way codex's --output-schema/-o do, and the body writes
// the answer there — so the stub exercises the contract the real CLIs were
// pinned to. It records its arguments, which is how the invocation contract
// (flags and extra arguments, then the prompt last) stays under test.
func fake(t *testing.T, body string) CLI {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-agent")
	script := "#!/bin/sh\necho \"$@\" > args.txt\n" + body
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return CLI{Name: "fake", Bin: path, Pinned: "stub", Structured: &Structured{
		Args: func(_, _, answerPath string) []string {
			return []string{"--answer", answerPath}
		},
		Answer: codexAnswer,
	}}
}

// answering is a stub that writes answer to the path it is given.
func answering(t *testing.T, answer string) CLI {
	t.Helper()
	return fake(t, "cat > \"$2\" <<'JSON'\n"+answer+"\nJSON\n")
}

// draftFiles lists what is under this run's drafts directory.
func draftFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(paths.Drafts())
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

// The answer is a document, and anything that is not one is refused here rather
// than turned into a draft with silently missing parts.
func TestParseDraft(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{name: "a valid answer", in: validAnswer},
		{name: "no services is an answer", in: `{"name":"proj","services":[]}`},
		{
			name:    "an unknown field is refused, not ignored",
			in:      `{"name":"proj","services":[],"extra":1}`,
			wantErr: true,
		},
		{name: "no name", in: `{"name":"","services":[]}`, wantErr: true},
		{name: "a service with no cmd", in: `{"name":"proj","services":[{"name":"api","why":"x"}]}`, wantErr: true},
		{name: "prose instead of a document", in: "I found no services worth declaring.", wantErr: true},
		{name: "a fenced block is no longer an answer", in: "```yaml\nname: proj\nservices: []\n```", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseDraft([]byte(tt.in))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseDraft(%q) = %+v, want an error", tt.in, got)
				}
				if !errors.Is(err, ErrNoDraft) {
					t.Fatalf("error = %v, want ErrNoDraft", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDraft: %v", err)
			}
		})
	}
}

func TestRunArchivesAValidDraft(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	root := t.TempDir()
	cli := answering(t, validAnswer)

	res, err := Run(context.Background(), Request{
		CLI: cli, Root: root, Prompt: "the brief", Timeout: 10 * time.Second,
		Group: "proj", Name: "draft", Stream: io.Discard,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// The structured flags come first, and the prompt is still the last thing
	// on the command line.
	args, err := os.ReadFile(filepath.Join(root, "args.txt"))
	if err != nil {
		t.Fatalf("the stub recorded no arguments: %v", err)
	}
	fields := strings.Fields(strings.TrimSpace(string(args)))
	if len(fields) == 0 || fields[0] != "--answer" {
		t.Fatalf("args = %q, want the answer flag first", args)
	}
	if last := fields[len(fields)-1]; last != "brief" {
		t.Fatalf("the prompt is not last: %q", last)
	}

	// The draft was rendered, parsed with the manifest loader, and archived
	// with the provenance header in front of it.
	if res.Path != paths.Draft("proj") {
		t.Fatalf("Path = %q, want %q", res.Path, paths.Draft("proj"))
	}
	data, err := os.ReadFile(res.Path)
	if err != nil {
		t.Fatalf("no draft archived: %v", err)
	}
	if !strings.HasPrefix(string(data), "# Drafted by fake (stub)") {
		t.Errorf("the archive does not open with its provenance:\n%s", data)
	}
	if !strings.Contains(string(data), "# why: package.json scripts.dev") {
		t.Errorf("the answer's provenance did not reach the file:\n%s", data)
	}
	if !strings.Contains(string(data), "# note: the worker has no port") {
		t.Errorf("the answer's notes did not reach the file:\n%s", data)
	}
	cfg, err := groups.Parse(res.Path, data)
	if err != nil {
		t.Fatalf("the archived draft does not parse: %v", err)
	}
	if len(cfg.Services) != 1 || cfg.Services[0].Name != "api" || cfg.Services[0].Port != 3999 {
		t.Fatalf("archived services = %+v", cfg.Services)
	}
	if res.Config == nil || res.Config.Name != "proj" {
		t.Fatalf("Result.Config = %+v", res.Config)
	}
}

func TestRunDryRunKeepsNothing(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	cli := answering(t, validAnswer)

	res, err := Run(context.Background(), Request{
		CLI: cli, Root: t.TempDir(), Prompt: "brief", Timeout: 10 * time.Second,
		Group: "proj", Name: "draft", Stream: io.Discard, DryRun: true,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Path != "" {
		t.Errorf("Path = %q, want empty on dry-run", res.Path)
	}
	if res.Draft == "" {
		t.Error("a dry run still returns the rendered draft")
	}
	if files := draftFiles(t); len(files) != 0 {
		t.Errorf("dry-run wrote %v", files)
	}
}

// A bad value is still caught by the loader, with line numbers against the file
// it would have been written to.
func TestRunRefusesABadDraft(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	cli := answering(t, `{"name":"proj","services":[{"name":"api","cmd":"echo hi","port":70000,"why":"x"}]}`)

	_, err := Run(context.Background(), Request{
		CLI: cli, Root: t.TempDir(), Prompt: "brief", Timeout: 10 * time.Second,
		Group: "proj", Name: "draft", Stream: io.Discard,
	})
	var configErr *groups.ConfigError
	if !errors.As(err, &configErr) {
		t.Fatalf("error = %v, want the config loader's own error with line numbers", err)
	}
	if !strings.Contains(err.Error(), "70000") {
		t.Errorf("error should name the bad port: %v", err)
	}
	if files := draftFiles(t); len(files) != 0 {
		t.Errorf("an invalid draft was archived anyway: %v", files)
	}
}

func TestRunWithoutAnAnswerSaysWhatWasMissing(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	cli := fake(t, "echo 'no services found, sorry'\n")

	_, err := Run(context.Background(), Request{
		CLI: cli, Root: t.TempDir(), Prompt: "brief", Timeout: 10 * time.Second,
		Group: "proj", Name: "draft", Stream: io.Discard,
	})
	if !errors.Is(err, ErrNoDraft) {
		t.Fatalf("error = %v, want ErrNoDraft", err)
	}
}

func TestRunReportsANonZeroExit(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	cli := fake(t, "echo 'boom' >&2\nexit 3\n")

	_, err := Run(context.Background(), Request{
		CLI: cli, Root: t.TempDir(), Prompt: "brief", Timeout: 10 * time.Second,
		Group: "proj", Name: "draft", Stream: io.Discard,
	})
	if !errors.Is(err, ErrAgentFailed) {
		t.Fatalf("error = %v, want ErrAgentFailed", err)
	}
	if !strings.Contains(err.Error(), "exit code 3") {
		t.Errorf("error should carry the code: %v", err)
	}
	if files := draftFiles(t); len(files) != 0 {
		t.Errorf("a failed run archived something: %v", files)
	}
}

func TestRunTimesOutAndKillsTheGroup(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	cli := fake(t, "sleep 30\n")

	start := time.Now()
	_, err := Run(context.Background(), Request{
		CLI: cli, Root: t.TempDir(), Prompt: "brief", Timeout: 300 * time.Millisecond,
		Group: "proj", Name: "draft", Stream: io.Discard,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("error = %v, want ErrTimeout", err)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("the timeout did not kill the run: it took %s", elapsed)
	}
}

// --- the answer readers -----------------------------------------------------

// claude answers with an envelope on stdout, and a detached run's log mixes
// stderr into it — the reader keeps the last object that is the envelope and
// ignores everything else on the stream.
func TestClaudeAnswer(t *testing.T) {
	stdout := "⚠ some warning this CLI prints on stderr\n" +
		`{"is_error":false,"result":"done","structured_output":{"name":"proj","services":[]}}` + "\n"
	out, err := claudeAnswer(stdout, "")
	if err != nil {
		t.Fatalf("claudeAnswer: %v", err)
	}
	if string(out) != `{"name":"proj","services":[]}` {
		t.Fatalf("answer = %s", out)
	}
	streamed := `{"type":"system","subtype":"init"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read"}]}}` + "\n" +
		`{"type":"result","is_error":false,"result":"done","structured_output":{"name":"proj","services":[]}}` + "\n"
	out, err = claudeAnswer(streamed, "")
	if err != nil || string(out) != `{"name":"proj","services":[]}` {
		t.Fatalf("streamed answer = %s, err = %v", out, err)
	}

	// A failed run: the sentence a person needs comes back, and it is not
	// wrapped in a sentinel that would bury it.
	failed := `{"is_error":true,"result":"Failed to authenticate. API Error: 403 quota exhausted"}`
	if _, err := claudeAnswer(failed, ""); err == nil ||
		!strings.Contains(err.Error(), "403 quota exhausted") {
		t.Fatalf("claudeAnswer(failed) = %v, want the CLI's own sentence", err)
	}

	if _, err := claudeAnswer("just prose\n", ""); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("claudeAnswer(prose) = %v, want ErrNoDraft", err)
	}
}

func TestCodexAnswer(t *testing.T) {
	dir := t.TempDir()
	answer := filepath.Join(dir, "answer.json")
	if err := os.WriteFile(answer, []byte(`{"name":"proj","services":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := codexAnswer("events, not the answer\n", answer)
	if err != nil {
		t.Fatalf("codexAnswer: %v", err)
	}
	if string(out) != `{"name":"proj","services":[]}` {
		t.Fatalf("answer = %s", out)
	}

	if _, err := codexAnswer("", filepath.Join(dir, "missing.json")); !errors.Is(err, ErrNoDraft) {
		t.Fatalf("a run with no answer file must be ErrNoDraft, got %v", err)
	}
}
