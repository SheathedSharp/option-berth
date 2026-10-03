package draft

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// rules is the drafting brief that goes in front of every run. It is embedded
// rather than read from a file or a user-installed skill, so the rules and the
// code that depends on them — the fenced-block contract, the no-shell rule —
// travel as one version. A copy installed somewhere is a copy a version behind.
//
//go:embed brief.md
var rules string

// maxCandidates bounds the listener list in a brief. A machine can have fifty
// ports open under one project; past this the brief stops being evidence and
// starts being noise, and the same cap the decision layer uses is a reasonable
// place to stop.
const maxCandidates = 40

// Facts is what the engine already knows, handed to the agent so it starts
// from evidence instead of from zero.
type Facts struct {
	// Root is the project directory; the agent runs with it as its cwd.
	Root string
	// Group is the name the manifest gets.
	Group string
	// CLI is the agent being asked (claude, codex), for the record.
	CLI string
	// Candidates are the listeners a scan found inside the project.
	Candidates []groups.Candidate
	// Existing is the current oberth.yaml, when there is one.
	Existing string
}

// Brief renders the whole prompt: the drafting rules, then this project.
func Brief(f Facts) string {
	var b strings.Builder
	b.WriteString(rules)
	b.WriteString("\n---\n\n# This project\n\n")
	fmt.Fprintf(&b, "- root: %s\n- worktree name: %s\n", f.Root, f.Group)

	if existing := strings.TrimSpace(f.Existing); existing != "" {
		b.WriteString("\nThe project already has a oberth.yaml. Keep what is right in it,\n")
		b.WriteString("fix what is wrong, and answer with the complete draft — this is what\n")
		b.WriteString("the file says today:\n\n")
		b.WriteString(strings.TrimRight(f.Existing, "\n"))
		b.WriteString("\n")
	} else {
		b.WriteString("\nThere is no oberth.yaml yet.\n")
	}

	if len(f.Candidates) == 0 {
		b.WriteString("\nNothing is listening inside the project right now, so there is no scan\n" +
			"evidence to start from. Find the services in the project's own files.\n")
		return b.String()
	}

	b.WriteString("\n## Listening inside the project right now\n\n")
	b.WriteString("Port, process, where it works, pid — then how it was started.\n\n")
	for i, c := range f.Candidates {
		if i == maxCandidates {
			fmt.Fprintf(&b, "- … and %d more listeners, left out\n", len(f.Candidates)-maxCandidates)
			break
		}
		process := c.Process
		if process == "" {
			process = "?"
		}
		where := c.Cwd
		if where == "" {
			where = "."
		}
		fmt.Fprintf(&b, "- %d  %s  %s  pid %d\n", c.Port, process, where, c.PID)
		if c.Command != "" {
			fmt.Fprintf(&b, "    %s\n", strings.Join(strings.Fields(c.Command), " "))
		}
	}
	return b.String()
}
