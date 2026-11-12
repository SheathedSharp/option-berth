// Package groups decides which project each listening port belongs to.
//
// A group is a name plus the ports currently resolved to it. Nothing creates a
// group directly: it exists because a pin, an `oberth start` run, an
// `oberth.yaml`, a Compose project or a git checkout resolved a port to it.
// Resolve walks that precedence chain, and Groups turns the result into the
// `state.Group` rows the daemon publishes.
package groups

import (
	"os"
	"path/filepath"
	"strings"
)

// maxWalk caps every upward walk in this package, so a symlink loop cannot hang
// a scan.
const maxWalk = 64

// Find locates the git checkout containing cwd.
//
// root holds the `.git` entry — a directory in a normal clone, a file in a
// linked worktree or a submodule. worktree is the qualified group name
// `<repo>@<worktree>` when root is a linked worktree, i.e. when its `.git` file
// points into another repository's `worktrees/` directory; otherwise empty. ok
// is false for an empty cwd or when no repository contains it.
//
// Find is the name-shaped view of Locate, which owns discovery: they share one
// walk and one `.git` parse, so the two cannot disagree about the checkout or
// whether it is linked.
func Find(cwd string) (root, worktree string, ok bool) {
	co, found := Locate(cwd)
	if !found {
		return "", "", false
	}
	if !co.Linked() {
		return co.Root, "", true
	}
	return co.Root, filepath.Base(co.Main) + "@" + co.Worktree, true
}

// GroupName is the automatic group name for a checkout: the qualified
// `<repo>@<worktree>` name for a linked worktree, otherwise the base name of
// the checkout directory.
func GroupName(root, worktree string) string {
	switch {
	case worktree != "":
		return worktree
	case root == "":
		return ""
	default:
		return filepath.Base(root)
	}
}

// gitdirTarget reads the `gitdir:` line of a `.git` file, made absolute against
// root and cleaned. It is "" when the file cannot be read or carries no such
// line.
func gitdirTarget(root, gitFile string) string {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		target, found := strings.CutPrefix(strings.TrimSpace(line), "gitdir:")
		if !found {
			continue
		}
		target = strings.TrimSpace(target)
		if target == "" {
			return ""
		}
		if filepath.IsAbs(target) {
			return filepath.Clean(target)
		}
		return filepath.Clean(filepath.Join(root, target))
	}
	return ""
}

// worktreeMain answers the main checkout's root when target is a linked
// worktree's admin directory, `<main>/.git/worktrees/<name>`. A submodule's
// directory (`…/.git/modules/<name>`, or anything else) yields "".
func worktreeMain(target string) string {
	if target == "" {
		return ""
	}
	parts := strings.Split(filepath.ToSlash(filepath.Clean(target)), "/")
	if len(parts) < 3 {
		return ""
	}
	last := len(parts) - 1
	if parts[last-1] != "worktrees" || parts[last-2] != ".git" {
		return ""
	}
	main := filepath.FromSlash(strings.Join(parts[:last-2], "/"))
	switch filepath.Base(main) {
	case "", ".", string(filepath.Separator):
		return ""
	}
	return main
}
