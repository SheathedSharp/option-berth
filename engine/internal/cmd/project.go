package cmd

import (
	"path/filepath"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// nearestConfig resolves the manifest for a worktree-scoped command. Keep
// this beside projectAt because both status and lifecycle commands need the
// same boundary, while low-level `start` no longer parses project arguments.
// A directory with no usable manifest answers with the same not_found refusal
// `up` and `restart` give, so every command that needs a project says the same
// thing about one that has none.
func nearestConfig(dir string) (*groups.Config, error) {
	index := groups.NewIndex()
	index.Observe(dir)
	if cfg := index.NearestFor(dir); cfg != nil {
		return cfg, nil
	}
	return nil, missingConfigError(index, dir)
}

// projectAt names the project a directory sits in, the way the runtime service's
// resolver does: a oberth.yaml at or above it names the project outright,
// otherwise the git checkout it belongs to names it — `<repo>`, or
// `<repo>@<worktree>` for a linked worktree.
//
// Empty when the directory is in no project.
func projectAt(dir string) (name, root string) {
	index := groups.NewIndex()
	index.Observe(dir)
	if cfg := index.NearestFor(dir); cfg != nil {
		return index.GroupOf(cfg), filepath.Dir(cfg.Path)
	}
	if r, worktree, ok := groups.Find(dir); ok {
		return groups.GroupName(r, worktree), r
	}
	return "", ""
}
