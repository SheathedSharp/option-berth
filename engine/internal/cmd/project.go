package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

// nearestConfig resolves the manifest for a worktree-scoped command. Keep
// this beside projectAt because both status and lifecycle commands need the
// same boundary, while low-level `start` no longer parses project arguments.
func nearestConfig(dir string) (*groups.Config, error) {
	index := groups.NewIndex()
	index.Observe(dir)
	if cfg := index.NearestFor(dir); cfg != nil {
		return cfg, nil
	}
	if bad := index.Invalid(); len(bad) > 0 {
		return nil, fmt.Errorf("%s cannot be used: %w", groups.ConfigName, bad[0].Err)
	}
	return nil, fmt.Errorf("no %s at or above %s\nhint: `oberth init` writes one, or use `oberth up <project>`",
		groups.ConfigName, shortPath(dir))
}

// projectAt names the project a directory sits in, the way the daemon's
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
