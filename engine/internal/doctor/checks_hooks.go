package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/sheathedsharp/option-berth/engine/internal/claudehook"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

const projectSettingsLimit = 256 * 1024

// checkClaudeHooks reports whether the worktree's committable Claude settings
// already include the option-berth hook command. It never reads
// settings.local.json, never reads the user-global Claude config, and never
// writes hook configuration.
func checkClaudeHooks(_ context.Context, env *Env) rpc.DoctorCheck {
	if env.Project == "" {
		return skip("no project directory", "")
	}
	if len(nearestManifest(projectConfigDirs(env.Project))) == 0 {
		return skip("no "+groups.ConfigName+" for this project", "")
	}
	var looked []string
	for _, dir := range projectConfigDirs(env.Project) {
		path := filepath.Join(dir, ".claude", "settings.json")
		text, err := readProjectSettingsJSON(path)
		switch {
		case os.IsNotExist(err):
			looked = append(looked, path)
		case err != nil:
			return warn(
				"this worktree has "+groups.ConfigName+" but its project Claude settings could not be read",
				path+": "+err.Error(),
				"fix the project's .claude/settings.json, then merge hooks from `oberth agent claude-hook --print-config`. doctor does not read settings.local.json")
		case settingsContainClaudeHook(text):
			if problems := hookBudgetProblems(text); len(problems) > 0 {
				return warn(
					"project Claude hooks are enabled, but the hook budget is too short",
					path+": "+strings.Join(problems, "; "),
					"merge a fresh `oberth agent claude-hook --project <worktree> --print-config` into the project's .claude/settings.json; keep timeout at 20 or higher, and do not pass `--lease 10s`. A newer oberth binary cannot stop Claude from cutting a shorter hook timeout first. doctor does not rewrite this file")
			}
			return ok("project .claude/settings.json enables oberth Claude hooks", path)
		default:
			looked = append(looked, path+" (no claude-hook)")
		}
	}
	return warn(
		"this worktree has "+groups.ConfigName+" but project Claude hooks are not enabled",
		"looked at "+strings.Join(looked, ", "),
		"merge the JSON from `oberth agent claude-hook --project <worktree> --print-config` into the project's .claude/settings.json; do not rely on settings.local.json, and do not install global hooks automatically")
}

func readProjectSettingsJSON(path string) (string, error) {
	if filepath.Base(path) != "settings.json" {
		return "", fmt.Errorf("refusing to read %s", filepath.Base(path))
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, projectSettingsLimit+1))
	if err != nil {
		return "", fmt.Errorf("reading: %w", err)
	}
	if len(data) > projectSettingsLimit {
		return "", fmt.Errorf("file exceeds %d bytes", projectSettingsLimit)
	}
	if !json.Valid(data) {
		return "", fmt.Errorf("not valid JSON")
	}
	return string(data), nil
}

func settingsContainClaudeHook(text string) bool {
	return strings.Contains(text, "claude-hook")
}

// hookBudgetProblems reports hook commands whose explicit Claude timeout or
// --lease is shorter than the hook command budget. A newer binary cannot
// stop Claude from cutting a shorter explicit timeout first. Omitting
// timeout is not a short budget: Claude Code uses its own official defaults
// (30s for UserPromptSubmit, 600s for Stop).
func hookBudgetProblems(text string) []string {
	var doc map[string]any
	if err := json.Unmarshal([]byte(text), &doc); err != nil {
		return nil
	}
	hooks, _ := doc["hooks"].(map[string]any)
	budget := claudehook.HookCommandTimeout
	var problems []string
	for event, raw := range hooks {
		blocks, _ := raw.([]any)
		for _, block := range blocks {
			holder, _ := block.(map[string]any)
			inner, _ := holder["hooks"].([]any)
			for _, hook := range inner {
				item, _ := hook.(map[string]any)
				command, _ := item["command"].(string)
				if !strings.Contains(command, "claude-hook") {
					continue
				}
				timeout, hasTimeout := hookCommandTimeout(item)
				if hasTimeout && timeout < budget {
					problems = append(problems, fmt.Sprintf("%s timeout is %s, shorter than the %s hook command budget", event, timeout, budget))
				}
				if lease, ok := hookCommandLease(command); ok && lease < budget {
					problems = append(problems, fmt.Sprintf("%s --lease is %s, shorter than the %s hook command budget", event, lease, budget))
				}
			}
		}
	}
	sort.Strings(problems)
	return problems
}

func hookCommandTimeout(item map[string]any) (time.Duration, bool) {
	raw, ok := item["timeout"]
	if !ok || raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case float64:
		return time.Duration(v * float64(time.Second)), true
	case json.Number:
		seconds, err := v.Float64()
		if err != nil {
			return 0, false
		}
		return time.Duration(seconds * float64(time.Second)), true
	default:
		return 0, false
	}
}

func hookCommandLease(command string) (time.Duration, bool) {
	tokens := splitHookCommand(command)
	for i, token := range tokens {
		switch {
		case token == "--lease" && i+1 < len(tokens):
			d, err := time.ParseDuration(tokens[i+1])
			if err != nil {
				return 0, false
			}
			return d, true
		case strings.HasPrefix(token, "--lease="):
			d, err := time.ParseDuration(strings.TrimPrefix(token, "--lease="))
			if err != nil {
				return 0, false
			}
			return d, true
		}
	}
	return 0, false
}

func splitHookCommand(command string) []string {
	var out []string
	var b strings.Builder
	var quote rune
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				b.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteRune(r)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
