package agentlaunch

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestProviderPlansKeepPromptAsOneLiteralArgument(t *testing.T) {
	prompt := "--dangerous $(touch nope)\n中文 and 'quotes'"
	for _, tc := range []struct {
		id, mode string
		want     []string
	}{
		{"opencode", "native", []string{"--prompt=" + prompt}},
		{"codex", "native", []string{"--", prompt}},
		{"claude", "native", []string{"--", prompt}},
		{"pi", "native", []string{"--", prompt}},
		{"opencode", "task", []string{"run", "--", prompt}},
		{"codex", "task", []string{"exec", "--", prompt}},
		{"claude", "task", []string{"--print", "--", prompt}},
		{"pi", "task", []string{"--print", "--", prompt}},
		{"deepseek", "task", []string{"--profile", "headless", "--", prompt}},
	} {
		t.Run(tc.id+"/"+tc.mode, func(t *testing.T) {
			root := t.TempDir()
			calls := 0
			plan, err := Build(Options{Provider: tc.id, Mode: tc.mode, Worktree: root, Prompt: prompt}, func(name string) (string, error) { calls++; return filepath.Join(root, name), nil })
			if err != nil || !reflect.DeepEqual(plan.Arguments, tc.want) || calls != 1 {
				t.Fatalf("plan=%+v err=%v calls=%d", plan, err, calls)
			}
			canonical, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatal(err)
			}
			if plan.Worktree != canonical {
				t.Fatal("working directory not canonical")
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 0 {
				t.Fatal("planning changed project files", err)
			}
		})
	}
}
func TestNativePlansDoNotResumeOrDisablePermissions(t *testing.T) {
	for _, p := range Providers(func(string) (string, error) { return "", exec.ErrNotFound }) {
		plan, err := Build(Options{Provider: p.ID, Worktree: t.TempDir()}, func(name string) (string, error) { return filepath.Join(t.TempDir(), name), nil })
		if err != nil {
			t.Fatal(err)
		}
		want := []string{}
		if p.ID == "deepseek" {
			want = []string{"--profile", "tui"}
		}
		if !reflect.DeepEqual(plan.Arguments, want) {
			t.Fatalf("unexpected native policy flags: %v", plan.Arguments)
		}
	}
}
func TestPlanningRejectsUnsafeOrUnsupportedInputs(t *testing.T) {
	root := t.TempDir()
	good := Options{Provider: "codex", Worktree: root}
	for _, change := range []func(*Options){
		func(o *Options) { o.Provider = "unknown" }, func(o *Options) { o.Worktree = "" },
		func(o *Options) { o.Mode = "automatic" }, func(o *Options) { o.Prompt = "bad\x00prompt" },
		func(o *Options) { o.Prompt = strings.Repeat("x", 65537) }, func(o *Options) { o.Mode = "task" },
		func(o *Options) { o.Provider = "deepseek"; o.Prompt = "unsupported native prompt" },
	} {
		opts := good
		change(&opts)
		if _, err := Build(opts, func(name string) (string, error) { return filepath.Join(root, name), nil }); err == nil {
			t.Fatalf("accepted invalid option: provider=%s mode=%s", opts.Provider, opts.Mode)
		}
	}
	for _, resolver := range []func(string) (string, error){
		func(string) (string, error) { return "", errors.New("missing") },
		func(string) (string, error) { return "./codex", nil },
	} {
		if _, err := Build(good, resolver); err == nil {
			t.Fatal("accepted unavailable or relative binary")
		}
	}
}

// An argv boundary alone is not an option-value boundary in yargs: a message
// beginning with --continue would otherwise resume a session instead of being
// sent literally. Attached values keep provider options outside user messages.
func TestOpenCodeNativeMessageCannotBecomeAnOption(t *testing.T) {
	for _, message := range []string{"--continue", "--session=other", "--auto", "--help", "-x", "a=b\n中文 'literal'"} {
		root := t.TempDir()
		plan, err := Build(Options{Provider: "opencode", Worktree: root, Prompt: message},
			func(string) (string, error) { return filepath.Join(root, "opencode"), nil })
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(plan.Arguments, []string{"--prompt=" + message}) {
			t.Fatalf("message may be parsed as provider policy: %q", plan.Arguments)
		}
	}
}
