// Package agentlaunch adapts existing coding-agent entry points, not their models,
// permissions, tools, conversation stores or reasoning loops.
package agentlaunch

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Provider struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Command      string `json:"command"`
	Installed    bool   `json:"installed"`
	NativePrompt bool   `json:"native_prompt"`
	Note         string `json:"note,omitempty"`
	ResumeFile   bool   `json:"resume_file"`
}

func Providers(lookPath func(string) (string, error)) []Provider {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	result := []Provider{
		{ID: "opencode", Name: "OpenCode", Command: "opencode", NativePrompt: true},
		{ID: "codex", Name: "Codex", Command: "codex", NativePrompt: true},
		{ID: "claude", Name: "Claude Code", Command: "claude", NativePrompt: true},
		{ID: "deepseek", Name: "DeepSeek Harness", Command: "dsh", Note: "native mode requires a user-installed tui profile; tasks use the headless profile"},
		{ID: "pi", Name: "Pi", Command: "pi", NativePrompt: true},
	}
	for i := range result {
		p, err := lookPath(result[i].Command)
		result[i].Installed = err == nil && filepath.IsAbs(p)
		result[i].ResumeFile = result[i].ID != "deepseek"
	}
	return result
}

type Plan struct {
	Provider   string           `json:"provider"`
	Executable string           `json:"executable"`
	Arguments  []string         `json:"arguments"`
	Worktree   string           `json:"worktree"`
	Mode       string           `json:"mode"`
	Resume     *ResumeReference `json:"resume,omitempty"`
}

type Options struct{ Provider, Worktree, Prompt, Mode, ResumeFile string }

// Build is read-only. It never runs --help, logs in, creates a profile or starts a
// daemon. The caller must explicitly execute the returned argv in its own PTY.
func Build(opts Options, lookPath func(string) (string, error)) (Plan, error) {
	var plan Plan
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if opts.Mode == "" {
		opts.Mode = "native"
	}
	if opts.Mode != "native" && opts.Mode != "task" {
		return plan, errors.New("agent mode must be native or task")
	}
	if strings.IndexByte(opts.Prompt, 0) >= 0 || len(opts.Prompt) > 64*1024 {
		return plan, errors.New("agent prompt must be NUL-free and at most 64 KiB")
	}
	var provider *Provider
	for _, p := range Providers(func(string) (string, error) { return "", exec.ErrNotFound }) {
		if p.ID == opts.Provider {
			copy := p
			provider = &copy
			break
		}
	}
	if provider == nil {
		return plan, errors.New("unknown agent; use oberth agent list")
	}
	if opts.Worktree == "" {
		return plan, errors.New("an explicit --worktree directory is required")
	}
	abs, err := filepath.Abs(opts.Worktree)
	if err != nil {
		return plan, err
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return plan, fmt.Errorf("cannot resolve selected worktree: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return plan, err
	}
	if !info.IsDir() {
		return plan, errors.New("selected worktree is not a directory")
	}
	var resume *ResumeReference
	if opts.ResumeFile != "" {
		if opts.Mode != "native" {
			return plan, errors.New("explicit continuation currently requires native mode")
		}
		resume, err = inspectResume(opts.Provider, opts.ResumeFile, abs)
		if err != nil {
			return plan, err
		}
	}
	executable, err := lookPath(provider.Command)
	if err != nil || !filepath.IsAbs(executable) {
		return plan, fmt.Errorf("%s is not installed on an absolute PATH; install and authenticate it explicitly", provider.Name)
	}
	args := []string{}
	if opts.Mode == "task" && strings.TrimSpace(opts.Prompt) == "" {
		return plan, errors.New("task mode requires a non-empty prompt")
	}
	if opts.Mode == "native" {
		switch opts.Provider {
		case "opencode":
			if opts.Prompt != "" {
				// yargs treats a separate leading-dash value as another option.
				// Keep the value attached so natural language cannot enable flags.
				args = append(args, "--prompt="+opts.Prompt)
			}
		case "codex", "claude", "pi":
			if opts.Prompt != "" {
				args = append(args, "--", opts.Prompt)
			}
		case "deepseek":
			if opts.Prompt != "" {
				return plan, errors.New("DeepSeek native prompt injection is not supported; use native mode without a prompt or --mode task")
			}
			args = append(args, "--profile", "tui")
		}
	} else {
		switch opts.Provider {
		case "opencode":
			args = []string{"run", "--", opts.Prompt}
		case "codex":
			args = []string{"exec", "--", opts.Prompt}
		case "claude":
			args = []string{"--print", "--", opts.Prompt}
		case "pi":
			args = []string{"--print", "--", opts.Prompt}
		case "deepseek":
			args = []string{"--profile", "headless", "--", opts.Prompt}
		}
	}
	if resume != nil {
		args = resumeArguments(opts.Provider, resume, opts.Prompt)
	}
	return Plan{Provider: opts.Provider, Executable: executable, Arguments: args, Worktree: abs, Mode: opts.Mode, Resume: resume}, nil
}
