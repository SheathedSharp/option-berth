package claims

import (
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// Identity derives the project and worktree a claim belongs to from a working
// directory. An explicit project or worktree always wins over what the
// directory says.
//
// Two callers ask this question from two different directories — the shell's
// cwd for `oberth claim`, and the directory an agent started its MCP server in
// — so the derivation lives here rather than in either caller. A key that
// differed between them would give one worktree two sets of ports.
//
// A directory outside any checkout claims under its own name; one with no
// usable name claims under "option-berth", so a key is never empty.
func Identity(cwd, project, worktree string) (string, string) {
	root, linked, found := groups.Find(cwd)
	switch {
	case !found:
		project = firstFilled(project, filepath.Base(cwd))
	case linked != "":
		// groups.Find spells a linked worktree "<repo>@<name>".
		repo, name, _ := strings.Cut(linked, "@")
		project = firstFilled(project, repo)
		worktree = firstFilled(worktree, name)
	default:
		project = firstFilled(project, filepath.Base(root))
	}
	if project == "" {
		project = "option-berth"
	}
	if worktree == "" {
		worktree = DefaultWorktree
	}
	return project, worktree
}

func firstFilled(current, fallback string) string {
	if current != "" {
		return current
	}
	return fallback
}
