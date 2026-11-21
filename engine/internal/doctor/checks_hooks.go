package doctor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
