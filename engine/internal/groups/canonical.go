package groups

import "path/filepath"

// Canonical is the one spelling of a path this package compares and indexes by:
// absolute, cleaned, symlinks resolved.
//
// The index is keyed by directory, so the same directory reached two ways would
// otherwise be two projects. macOS makes that the normal case: $TMPDIR is
// /var/folders/… while the kernel reports a process's cwd as
// /private/var/folders/…. A manifest filed under one spelling is invisible to a
// lookup using the other, and `oberth start` falls back to the git root or the
// directory name instead of the name its `oberth.yaml` declares.
//
// Callers canonicalize where a path enters the resolver — Load, the index's
// Observe/Nearest/AddFile/Reload, Find and spawn.Resolve — and pass canonical
// paths from then on.
//
// A path that does not exist yet (an `oberth.yaml` about to be written, or one
// just deleted) still canonicalizes: the deepest existing ancestor is resolved
// and the missing names are appended. The empty path stays empty.
func Canonical(path string) string {
	if path == "" {
		return ""
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = filepath.Clean(path)
	}
	if resolved, exists := symlinksResolved(abs); exists {
		return resolved
	}
	return resolveMissingTail(abs)
}

// symlinksResolved asks the filesystem for the real spelling of a path that
// exists.
func symlinksResolved(path string) (string, bool) {
	resolved, err := filepath.EvalSymlinks(path)
	return resolved, err == nil
}

// resolveMissingTail resolves the part of abs that exists and re-attaches the
// names below it. EvalSymlinks needs every component to exist, so the walk
// climbs until it finds one that does.
func resolveMissingTail(abs string) string {
	var missing []string
	dir := abs
	for climbed := 0; climbed < maxWalk; climbed++ {
		parent := filepath.Dir(dir)
		if parent == dir {
			return abs
		}
		missing = append(missing, filepath.Base(dir))
		dir = parent
		resolved, ok := symlinksResolved(dir)
		if !ok {
			continue
		}
		for i := len(missing) - 1; i >= 0; i-- {
			resolved = filepath.Join(resolved, missing[i])
		}
		return resolved
	}
	return abs
}
