package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Migration 005 owns the sessions table. The run registry is the authority on
// which sessions are live right now; this table remembers the ones that were,
// so a session that went inactive still has a tool, a worktree and a branch
// for as long as port history may refer to it.
//
// The table holds one row per session id. first_seen is written once and
// stays put; every later write refreshes last_seen and the descriptive fields.

// sessionColumns is the column list in scan order. Every statement in this
// file uses it so a write and a read cannot disagree about layout.
const sessionColumns = `id, first_seen, last_seen, tool, branch, worktree, label, detected`

// SessionRow is one persisted session. It mirrors state.Session plus the two
// timestamps but does not import the wire package: the store deals in rows,
// and the runtime service maps them onto the collection.
type SessionRow struct {
	ID        string
	FirstSeen time.Time
	LastSeen  time.Time
	Tool      string
	Branch    string
	Worktree  string
	Label     string
	Detected  bool
}

// SessionStore is the sessions table, obtained from Store.Sessions().
type SessionStore struct{ s *Store }

// Sessions opens the sessions table.
func (s *Store) Sessions() SessionStore { return SessionStore{s: s} }

// errSessionWithoutID rejects a row that could never be looked up again.
var errSessionWithoutID = errors.New("store: a session needs an id")

// Upsert writes a session or refreshes the part of an existing row that can
// change. first_seen is excluded from the update: it is the moment the
// session first appeared, which is what a port-history audit asks for. A zero
// LastSeen means "now": an upsert only happens while the session is doing
// something.
func (t SessionStore) Upsert(row SessionRow) error {
	if row.ID == "" {
		return errSessionWithoutID
	}
	now := time.Now().UTC()
	firstSeen := row.FirstSeen
	if firstSeen.IsZero() {
		firstSeen = now
	}
	lastSeen := row.LastSeen
	if lastSeen.IsZero() {
		lastSeen = now
	}
	return t.s.exec(
		`INSERT INTO sessions(`+sessionColumns+`)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     tool = excluded.tool,
		     label = excluded.label,
		     worktree = excluded.worktree,
		     branch = excluded.branch,
		     detected = excluded.detected,
		     last_seen = excluded.last_seen`,
		row.ID, timeToString(firstSeen), timeToString(lastSeen),
		row.Tool, row.Branch, row.Worktree, row.Label, boolInt(row.Detected),
	)
}

// Touch advances last_seen, which is what keeps a live session outside the
// prune window. An id nobody recorded stays unknown: only Upsert creates.
func (t SessionStore) Touch(id string, at time.Time) error {
	if id == "" {
		return nil
	}
	touchedAt := at
	if touchedAt.IsZero() {
		touchedAt = time.Now()
	}
	return t.s.exec(
		`UPDATE sessions SET last_seen = ? WHERE id = ?`,
		timeToString(touchedAt), id,
	)
}

// List returns every recorded session, most recently seen first.
func (t SessionStore) List() ([]SessionRow, error) {
	cursor, err := t.s.db.Query(
		`SELECT ` + sessionColumns + ` FROM sessions ORDER BY last_seen DESC, id`)
	if err != nil {
		return nil, fmt.Errorf("reading sessions: %w", err)
	}
	defer func() { _ = cursor.Close() }()

	var rows []SessionRow
	for cursor.Next() {
		row, err := scanSession(cursor.Scan)
		if err != nil {
			return nil, fmt.Errorf("reading sessions: %w", err)
		}
		rows = append(rows, row)
	}
	return rows, cursor.Err()
}

// Get reads one session by id. It is a keyed lookup rather than a walk over
// List, so a caller asking about one session does not pay for every session.
func (t SessionStore) Get(id string) (SessionRow, bool, error) {
	if id == "" {
		return SessionRow{}, false, nil
	}
	row, err := scanSession(
		t.s.db.QueryRow(`SELECT `+sessionColumns+` FROM sessions WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRow{}, false, nil
	}
	if err != nil {
		return SessionRow{}, false, fmt.Errorf("reading session %q: %w", id, err)
	}
	return row, true, nil
}

// Prune deletes sessions last seen before cutoff and reports how many went.
// The runtime service calls it with now-sessions.Retention on a timer.
func (t SessionStore) Prune(cutoff time.Time) (int, error) {
	removed, err := t.s.execRows(
		`DELETE FROM sessions WHERE last_seen < ?`, timeToString(cutoff))
	if err != nil {
		return 0, fmt.Errorf("pruning sessions: %w", err)
	}
	return removed, nil
}

// scanSession reads one row in sessionColumns order. A timestamp written by a
// build with a different layout degrades to the zero time instead of losing
// the row: the id, tool and branch are still worth rendering.
func scanSession(scan func(...any) error) (SessionRow, error) {
	var (
		row     SessionRow
		first   string
		last    string
		flagged int
	)
	if err := scan(&row.ID, &first, &last, &row.Tool, &row.Branch,
		&row.Worktree, &row.Label, &flagged); err != nil {
		return SessionRow{}, err
	}
	row.FirstSeen = timeOrZero(first)
	row.LastSeen = timeOrZero(last)
	row.Detected = flagged != 0
	return row, nil
}

// boolInt encodes a boolean for SQLite, which has no boolean storage class.
func boolInt(flag bool) int {
	var encoded int
	if flag {
		encoded = 1
	}
	return encoded
}

// timeOrZero parses a stored timestamp, yielding the zero time when the text
// is not one this build understands.
func timeOrZero(raw string) time.Time {
	parsed, err := timeFromString(raw)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
