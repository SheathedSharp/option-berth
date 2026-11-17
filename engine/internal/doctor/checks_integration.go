package doctor

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// agentInstructionLimit bounds how much of an instruction file is read. An
// AGENTS.md is a note to an agent, not a corpus; a file past this size is read
// only far enough to answer "does it mention oberth", and a match past the
// limit is not claimed.
const agentInstructionLimit = 512 << 10

// checkAgentIntegration answers "will an agent working in this worktree learn
// that oberth manages its services?". The engine cannot load a tool's session
// or edit its settings, so the worktree's own AGENTS.md / CLAUDE.md is the one
// session-entry surface it can point at. This reads those files; it never
// writes them, and it does not judge the prose beyond the product name.
func checkAgentIntegration(_ context.Context, env *Env) rpc.DoctorCheck {
	if env.Project == "" {
		return skip("no project directory", "")
	}
	files := nearestManifest(projectConfigDirs(env.Project))
	if len(files) == 0 {
		return skip("no "+groups.ConfigName+" for this project", "")
	}
	// The instruction file belongs to the worktree, not necessarily to the
	// manifest's own directory: a manifest may sit in a package below the
	// repository root. Look from the manifest up to the git root.
	var looked []string
	for _, dir := range projectConfigDirs(filepath.Dir(files[0])) {
		for _, name := range []string{"AGENTS.md", "CLAUDE.md"} {
			path := filepath.Join(dir, name)
			text, err := readAgentInstructions(path)
			switch {
			case os.IsNotExist(err):
				looked = append(looked, path)
			case err != nil:
				looked = append(looked, path+" ("+err.Error()+")")
			case strings.Contains(strings.ToLower(text), "oberth"):
				return ok("the worktree's agent instructions mention oberth", path)
			default:
				looked = append(looked, path)
			}
		}
	}
	return warn(
		"this worktree has "+groups.ConfigName+" but its agent instructions do not mention oberth",
		"looked at "+strings.Join(looked, ", "),
		"paste the section from docs/integrations.md into AGENTS.md or CLAUDE.md, so an agent starts with status/up/down instead of nohup and manual kills")
}

// readAgentInstructions reads at most agentInstructionLimit bytes. A file that
// cannot be read is reported to the caller, not treated as absent.
func readAgentInstructions(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, agentInstructionLimit))
	if err != nil {
		return "", fmt.Errorf("reading: %w", err)
	}
	return string(data), nil
}
