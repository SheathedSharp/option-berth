package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Known roots are the directories where an oberth.yaml has been seen. The groups
// resolver rebuilds its index from them on daemon start instead of walking the
// filesystem, and the config watcher watches them.

// AddRoot records a directory as a known oberth.yaml root. The path is cleaned
// first, and adding the same one twice is a no-op.
func (s *Store) AddRoot(path string) error {
	clean, err := cleanRoot(path)
	if err != nil {
		return err
	}
	return s.exec(
		`INSERT INTO known_roots(path, added_at) VALUES(?, ?)
		 ON CONFLICT(path) DO NOTHING`,
		clean, nowString(),
	)
}

// Roots returns every known root, sorted.
func (s *Store) Roots() ([]string, error) {
	roots, err := s.textColumn(`SELECT path FROM known_roots ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("reading known_roots: %w", err)
	}
	return roots, nil
}

// RemoveRoot forgets a root. Removing an unknown root is not an error.
func (s *Store) RemoveRoot(path string) error {
	clean, err := cleanRoot(path)
	if err != nil {
		return err
	}
	return s.exec(`DELETE FROM known_roots WHERE path = ?`, clean)
}

// CleanRoot is cleanRoot for callers outside this package that key their own
// rows by worktree path. There is one spelling of a root, and the workplace
// tables need the same one the roots table uses: two spellings of one checkout
// would be two sets of workplaces.
func CleanRoot(path string) (string, error) { return cleanRoot(path) }

// cleanRoot is the one spelling of a root path, and every write and delete goes
// through it, so one directory cannot occupy two rows.
//
// Resolving symlinks is the point, not a tidy-up: two callers reach this with
// two spellings of one directory — AddRoot gets whatever the caller passed,
// while the scanner's root walk produces its own. On macOS `/var` is a symlink
// to `/private/var`, so a project under either spelling used to become two rows,
// and RemoveRoot deletes by string, so it could never remove the other one.
func cleanRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("store: root path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("store: resolving root %q: %w", path, err)
	}
	// EvalSymlinks alone is not enough: it fails on a path that does not exist,
	// and a vanished root is exactly what RemoveRoot is called with — the seed
	// step drops projects that were deleted or moved. Resolving the part that
	// exists and keeping the rest as written names both spellings of a missing
	// directory the same way.
	return resolveExistingPrefix(filepath.Clean(abs)), nil
}

// resolveExistingPrefix resolves the symlinks in whatever leading part of path
// exists and appends the rest as written.
func resolveExistingPrefix(path string) string {
	prefix, rest := path, ""
	for {
		resolved, err := filepath.EvalSymlinks(prefix)
		if err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(prefix)
		if parent == prefix {
			// An absolute path always resolves down to "/", so this is
			// unreachable in practice; keeping the caller's spelling is the
			// honest answer if a platform ever surprises us.
			return path
		}
		rest = filepath.Join(filepath.Base(prefix), rest)
		prefix = parent
	}
}
