package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Worktree identities are the `${worktree}` tokens the daemon has seen, with
// the canonical root each token was derived from and the group the checkout was
// published under. The row outlives the directory: after a checkout is deleted
// or moved, its databases and data directories still carry the token, and this
// is where a reader finds out which path they belonged to.
//
// The table is an identity ledger, not runtime truth. It records only what was
// observed; whether the root still exists is answered by the reader against the
// filesystem.

// WorktreeIdentity is one observed checkout identity.
type WorktreeIdentity struct {
	// Identity is the `${worktree}` token: "wt_" and twelve hex digits.
	Identity string
	// Root is the canonical directory the token was derived from.
	Root string
	// Group is the group name the checkout's manifest was published under.
	// Empty when the identity was recorded before a manifest named it.
	Group string
	// ConfigPath is the manifest the identity was observed through. When the
	// directory still exists this is the exact manifest to re-read; when it is
	// gone the path is evidence, not something to open.
	ConfigPath string
	// Project and Worktree are the claim key's two halves for this checkout,
	// kept so a deleted checkout's auto-port reservations can still be found.
	Project  string
	Worktree string
	// FirstSeen is when the ledger first recorded the checkout. LastSeen is
	// the last write, not a per-scan heartbeat: an unchanged identity is not
	// rewritten on every tick. Existence is always decided by the reader
	// against the filesystem, never by these timestamps.
	FirstSeen time.Time
	LastSeen  time.Time
}

// worktreeIdentityColumns is the column list in scan order, shared by every
// statement here so the write and the reads cannot drift apart.
const worktreeIdentityColumns = `identity, root, group_name, config_path, project, worktree, first_seen, last_seen`

// RecordWorktreeIdentity records one observation. The same identity updates the
// group, claim key and last_seen and keeps first_seen; the root is canonical,
// so two spellings of one directory cannot become two rows.
func (s *Store) RecordWorktreeIdentity(rec WorktreeIdentity) error {
	identity := strings.TrimSpace(rec.Identity)
	if identity == "" {
		return errors.New("store: worktree identity is empty")
	}
	root, err := cleanRoot(rec.Root)
	if err != nil {
		return err
	}
	now := time.Now()
	first := rec.FirstSeen
	if first.IsZero() {
		first = now
	}
	last := rec.LastSeen
	if last.IsZero() {
		last = now
	}
	return s.exec(
		`INSERT INTO worktree_identities(`+worktreeIdentityColumns+`)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(identity) DO UPDATE SET
		   root = excluded.root,
		   group_name = CASE WHEN excluded.group_name = '' THEN worktree_identities.group_name ELSE excluded.group_name END,
		   config_path = CASE WHEN excluded.config_path = '' THEN worktree_identities.config_path ELSE excluded.config_path END,
		   project = CASE WHEN excluded.project = '' THEN worktree_identities.project ELSE excluded.project END,
		   worktree = CASE WHEN excluded.worktree = '' THEN worktree_identities.worktree ELSE excluded.worktree END,
		   last_seen = excluded.last_seen`,
		identity, root, strings.TrimSpace(rec.Group), strings.TrimSpace(rec.ConfigPath),
		strings.TrimSpace(rec.Project), strings.TrimSpace(rec.Worktree),
		timeToString(first), timeToString(last),
	)
}

// WorktreeIdentities returns every recorded identity, oldest first.
func (s *Store) WorktreeIdentities() ([]WorktreeIdentity, error) {
	rows, err := s.db.Query(
		`SELECT ` + worktreeIdentityColumns + `
		 FROM worktree_identities ORDER BY first_seen, identity`)
	if err != nil {
		return nil, fmt.Errorf("reading worktree identities: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []WorktreeIdentity
	for rows.Next() {
		rec, err := scanWorktreeIdentity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

// WorktreeIdentity returns the identity with this token.
func (s *Store) WorktreeIdentity(identity string) (WorktreeIdentity, bool, error) {
	row := s.db.QueryRow(
		`SELECT `+worktreeIdentityColumns+`
		 FROM worktree_identities WHERE identity = ?`, strings.TrimSpace(identity))
	rec, err := scanWorktreeIdentity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return WorktreeIdentity{}, false, nil
	}
	if err != nil {
		return WorktreeIdentity{}, false, err
	}
	return rec, true, nil
}

// WorktreeIdentityByRoot returns the identity recorded for a canonical root.
func (s *Store) WorktreeIdentityByRoot(root string) (WorktreeIdentity, bool, error) {
	clean, err := cleanRoot(root)
	if err != nil {
		return WorktreeIdentity{}, false, err
	}
	row := s.db.QueryRow(
		`SELECT `+worktreeIdentityColumns+`
		 FROM worktree_identities WHERE root = ? ORDER BY first_seen DESC LIMIT 1`, clean)
	rec, err := scanWorktreeIdentity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return WorktreeIdentity{}, false, nil
	}
	if err != nil {
		return WorktreeIdentity{}, false, err
	}
	return rec, true, nil
}

// WorktreeIdentityGroup returns every identity last published under this group
// name. More than one distinct identity means a by-name stop cannot tell which
// checkout was meant.
func (s *Store) WorktreeIdentityGroup(group string) ([]WorktreeIdentity, error) {
	rows, err := s.db.Query(
		`SELECT `+worktreeIdentityColumns+`
		 FROM worktree_identities WHERE group_name = ? ORDER BY first_seen, identity`,
		strings.TrimSpace(group))
	if err != nil {
		return nil, fmt.Errorf("reading worktree identities for group %q: %w", group, err)
	}
	defer func() { _ = rows.Close() }()
	var out []WorktreeIdentity
	for rows.Next() {
		rec, err := scanWorktreeIdentity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, rec)
	}
	return out, rows.Err()
}

func scanWorktreeIdentity(scan func(dest ...any) error) (WorktreeIdentity, error) {
	var (
		rec         WorktreeIdentity
		first, last string
	)
	if err := scan(&rec.Identity, &rec.Root, &rec.Group, &rec.ConfigPath,
		&rec.Project, &rec.Worktree, &first, &last); err != nil {
		return WorktreeIdentity{}, err
	}
	var err error
	if rec.FirstSeen, err = timeFromString(first); err != nil {
		return WorktreeIdentity{}, fmt.Errorf("worktree identity %s has an unreadable first_seen: %w", rec.Identity, err)
	}
	if rec.LastSeen, err = timeFromString(last); err != nil {
		return WorktreeIdentity{}, fmt.Errorf("worktree identity %s has an unreadable last_seen: %w", rec.Identity, err)
	}
	return rec, nil
}
