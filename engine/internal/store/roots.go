package store

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// Known roots are the directories where a oberth.yaml has been seen. The
// groups resolver rebuilds its index from them on daemon start instead of
// walking the filesystem, and the config watcher watches them.

// AddRoot records a directory as a known oberth.yaml root. Paths are cleaned
// and made absolute, and adding one twice is a no-op.
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
	rows, err := s.db.Query(`SELECT path FROM known_roots ORDER BY path`)
	if err != nil {
		return nil, fmt.Errorf("reading known_roots: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("reading known_roots: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RemoveRoot forgets a root. Removing an unknown root is not an error.
func (s *Store) RemoveRoot(path string) error {
	clean, err := cleanRoot(path)
	if err != nil {
		return err
	}
	return s.exec(`DELETE FROM known_roots WHERE path = ?`, clean)
}

// cleanRoot is the one spelling of a root path. Every write and every delete
// goes through it, so the same directory cannot sit in the table twice.
//
// It resolves symlinks, because two callers reach this function with two
// spellings of one directory: `AddRoot` gets whatever the caller was handed,
// while `rememberRoots` hands over a path that came from a git repository walk.
// On macOS `/var` is a symlink to `/private/var`, so a project under either one
// arrived as two rows — and `RemoveRoot` deletes by string, so it could never
// remove the other spelling. The table only grew.
func cleanRoot(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("store: root path is empty")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("store: resolving root %q: %w", path, err)
	}
	// EvalSymlinks alone is not enough: it fails on a path that does not exist,
	// and a vanished root is exactly what RemoveRoot is called with (the seed
	// step drops projects that were deleted or moved). Resolving the part that
	// is there and keeping the rest as written names both spellings of a missing
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
			// Only reachable for a path with nothing left to strip, which
			// filepath.Abs has already ruled out: every absolute path resolves
			// down to "/". Keeping the caller's own spelling is the honest
			// fallback.
			return path
		}
		rest = filepath.Join(filepath.Base(prefix), rest)
		prefix = parent
	}
}
