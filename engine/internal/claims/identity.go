package claims

import (
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// Identity derives the checkout a directory belongs to: the project is the
// repository and the worktree is the checkout within it, so two linked
// worktrees of one repository are two identities and get different addresses.
//
// The runtime service and the scanner both ask this question, from a directory that may
// be the manifest's or a service's, so the derivation lives here rather than
// being spelled out at each call site: a key that came out differently between
// two callers would hand one worktree two sets of ports, which is exactly what
// reservations exist to prevent.
//
// A directory outside any git checkout is its own project named after the
// directory, and a directory with no name at all is "option-berth", so a
// project is never empty.
func Identity(dir string) (project, worktree string) {
	root, linked, ok := groups.Find(dir)
	switch {
	case !ok:
		project = filepath.Base(dir)
	case linked != "":
		// groups.Find spells a linked worktree "<repo>@<name>".
		repo, name, _ := strings.Cut(linked, "@")
		project, worktree = repo, name
	default:
		project = filepath.Base(root)
	}
	if project == "" {
		project = "option-berth"
	}
	return project, worktreeName(worktree)
}
