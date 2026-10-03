package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// The sessions table (migration 005). The daemon's run registry is the
// authority on which sessions are *live*; this table is the memory of the ones
// that were, so an inactive session still has a tool, a worktree and a branch
// to render for the seven days it is kept.
//
// One row per session id. first_seen is written once and never moved, because
// it is when the session first started something; everything else including
// last_seen is refreshed on every touch.

// sessionColumns is the table's column list in scan order, shared by every
// statement here so the write and the reads cannot drift apart.
const sessionColumns = `id, tool, label, worktree, branch, detected, first_seen, last_seen`

// SessionRow is one persisted session. It deliberately mirrors state.Session
// plus the two timestamps, without importing the wire package: the store's job
// is rows, and the daemon maps them onto the collection.
type SessionRow struct {
	ID        string
	Tool      string
	Label     string
	Worktree  string
	Branch    string
	Detected  bool
	FirstSeen time.Time
	LastSeen  time.Time
}

// SessionStore is the sessions table. Get one from Store.Sessions().
type SessionStore struct{ s *Store }

// Sessions returns the handle onto the sessions table.
func (s *Store) Sessions() SessionStore { return SessionStore{s: s} }

// Upsert records a session, or refreshes the mutable half of one already
// recorded. first_seen is not part of the update: it keeps the moment the
// session first appeared, which is what an audit of a port's history asks for.
// A zero LastSeen means now (an upsert only happens while the session is doing
// something).
func (t SessionStore) Upsert(row SessionRow) error {
	if row.ID == "" {
		return fmt.Errorf("store: a session needs an id")
	}
	now := time.Now().UTC()
	if row.FirstSeen.IsZero() {
		row.FirstSeen = now
	}
	if row.LastSeen.IsZero() {
		row.LastSeen = now
	}
	return t.s.exec(
		`INSERT INTO sessions(`+sessionColumns+`)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     tool      = excluded.tool,
		     label     = excluded.label,
		     worktree  = excluded.worktree,
		     branch    = excluded.branch,
		     detected  = excluded.detected,
		     last_seen = excluded.last_seen`,
		row.ID, row.Tool, row.Label, row.Worktree, row.Branch, boolInt(row.Detected),
		timeToString(row.FirstSeen), timeToString(row.LastSeen),
	)
}

// Touch moves a session's last_seen forward, which is what keeps a live
// session out of the prune window. Touching an unknown session is a no-op:
// only Upsert creates rows.
func (t SessionStore) Touch(id string, at time.Time) error {
	if id == "" {
		return nil
	}
	if at.IsZero() {
		at = time.Now()
	}
	return t.s.exec(`UPDATE sessions SET last_seen = ? WHERE id = ?`, timeToString(at), id)
}

// List returns every recorded session, most recently seen first.
func (t SessionStore) List() ([]SessionRow, error) {
	rows, err := t.s.db.Query(`SELECT ` + sessionColumns + ` FROM sessions ORDER BY last_seen DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("reading sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []SessionRow
	for rows.Next() {
		r, err := scanSession(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("reading sessions: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one session by id. It is a keyed read, not a walk over List:
// the caller that asks for one session should not pay for every session.
func (t SessionStore) Get(id string) (SessionRow, bool, error) {
	if id == "" {
		return SessionRow{}, false, nil
	}
	r, err := scanSession(t.s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, fmt.Errorf("reading session %q: %w", id, err)
	}
	return r, true, nil
}

// Prune drops sessions last seen before cutoff and reports how many went.
// The daemon calls it with now-sessions.Retention on a timer.
func (t SessionStore) Prune(cutoff time.Time) (int, error) {
	n, err := t.s.execRows(`DELETE FROM sessions WHERE last_seen < ?`, timeToString(cutoff))
	if err != nil {
		return 0, fmt.Errorf("pruning sessions: %w", err)
	}
	return n, nil
}

// scanSession reads one row in sessionColumns order. A timestamp written by a
// build with a different layout degrades to the zero time rather than
// dropping the row: the id, tool and branch are still worth rendering.
func scanSession(scan func(dest ...any) error) (SessionRow, error) {
	var (
		r        SessionRow
		detected int
		first    string
		last     string
	)
	if err := scan(&r.ID, &r.Tool, &r.Label, &r.Worktree, &r.Branch,
		&detected, &first, &last); err != nil {
		return SessionRow{}, err
	}
	r.Detected = detected != 0
	r.FirstSeen = timeOrZero(first)
	r.LastSeen = timeOrZero(last)
	return r, nil
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// timeOrZero parses a stored timestamp, yielding the zero time for a row
// written by something that used a different layout.
func timeOrZero(s string) time.Time {
	t, err := timeFromString(s)
	if err != nil {
		return time.Time{}
	}
	return t
}
