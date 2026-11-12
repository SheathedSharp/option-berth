package store

import "fmt"

// Group aliases are project names set by a rename for a project that has no
// oberth.yaml to write the name into. They are keyed by the root of the
// project's main checkout, so that checkout and every linked worktree of it
// pick the alias up together.

// upsertGroupAlias keeps one row per main checkout: a second rename replaces
// the first instead of failing on the key.
const upsertGroupAlias = `
INSERT INTO group_aliases (root, name, created_at)
VALUES (?, ?, ?)
ON CONFLICT(root) DO UPDATE SET
	name = excluded.name,
	created_at = excluded.created_at`

// prepareAlias validates both halves of an alias write. The root is cleaned by
// the same rule every other path-keyed table uses; the name has to be usable as
// a project selector.
func prepareAlias(root, name string) (string, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return "", err
	}
	if err := validName(name); err != nil {
		return "", err
	}
	return clean, nil
}

// SetGroupAlias names the project whose main checkout is at root, replacing any
// alias it already had.
func (s *Store) SetGroupAlias(root, name string) error {
	clean, err := prepareAlias(root, name)
	if err != nil {
		return err
	}
	return s.exec(upsertGroupAlias, clean, name, nowString())
}

// clearGroupAlias drops the single row keyed by a main checkout root. Deleting
// an absent row is not an error.
const clearGroupAlias = `DELETE FROM group_aliases WHERE root = ?`

// ClearGroupAlias removes the alias for root; a missing row is not an error.
func (s *Store) ClearGroupAlias(root string) error {
	clean, err := cleanRoot(root)
	if err != nil {
		return err
	}
	return s.exec(clearGroupAlias, clean)
}

// GroupAliases maps every main checkout root to the project name set for it.
func (s *Store) GroupAliases() (map[string]string, error) {
	aliases, err := s.allKeyed(`SELECT root, name FROM group_aliases`)
	if err != nil {
		return nil, fmt.Errorf("reading group_aliases: %w", err)
	}
	return aliases, nil
}
