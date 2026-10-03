package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The port-event ring. One table, one writer, newest-first reads. Retention is
// SQL-side (the port_events ring trigger in migration 001 keeps the newest
// HistoryCapacity rows), so this file owns three things and nothing else:
// encoding an event for its row, decoding a row back, and the two statements
// that move rows in and out.

// Event kinds written to the history ring. They are the port-scoped subset of
// the state.event kinds the daemon publishes.
const (
	EventPortUp        = "port_up"
	EventPortDown      = "port_down"
	EventPortRestarted = "port_restarted"
)

// HistoryEvent is one row of the port_events ring. The first six fields are
// the wire shape of ports.history; Bind, ProjectRoot and Command are extra
// columns the daemon spec's table carries and are omitted from JSON when
// empty.
type HistoryEvent struct {
	At          time.Time `json:"at"`
	Kind        string    `json:"kind"`
	Port        int       `json:"port"`
	PID         int       `json:"pid"`
	DisplayName string    `json:"display_name"`
	Group       string    `json:"group"`

	Bind        string `json:"bind,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Command     string `json:"command,omitempty"`
}

// DefaultHistoryLimit is what Query uses when limit <= 0, matching
// `oberth history` with no arguments.
const DefaultHistoryLimit = 50

// HistoryCapacity is the number of rows the ring keeps. Enforced in SQL by
// the port_events_ring trigger in migration 001; the constant is here so
// tests and callers can talk about it.
const HistoryCapacity = 10000

// historyColumns is the ring's column list in insert order. The write path and
// the read path both build from it, so the two statements cannot drift apart.
const historyColumns = `at, kind, port, bind, pid, display_name, group_name, project_root, command`

// historyRow is one encoded event: every column already in its storage form.
// Encoding first is what lets a batch be validated before its transaction
// opens.
type historyRow struct {
	at          string
	kind        string
	port        int
	bind        string
	pid         int
	displayName string
	group       string
	projectRoot string
	command     string
}

// encodeHistory turns an event into its row. A zero At is stamped now; a
// missing kind is refused because no reader can interpret the row.
func encodeHistory(e HistoryEvent) (historyRow, error) {
	if strings.TrimSpace(e.Kind) == "" {
		return historyRow{}, errors.New("store: history event needs a kind")
	}
	at := e.At
	if at.IsZero() {
		at = time.Now()
	}
	return historyRow{
		at:          timeToString(at),
		kind:        e.Kind,
		port:        e.Port,
		bind:        e.Bind,
		pid:         e.PID,
		displayName: e.DisplayName,
		group:       e.Group,
		projectRoot: e.ProjectRoot,
		command:     e.Command,
	}, nil
}

func (r historyRow) args() []any {
	return []any{r.at, r.kind, r.port, r.bind, r.pid, r.displayName, r.group, r.projectRoot, r.command}
}

// decodeHistory reads one row, in the column order historyColumns names. scan
// is the row's Scan method.
func decodeHistory(scan func(dest ...any) error) (HistoryEvent, error) {
	var (
		e  HistoryEvent
		at string
	)
	if err := scan(&at, &e.Kind, &e.Port, &e.Bind, &e.PID, &e.DisplayName,
		&e.Group, &e.ProjectRoot, &e.Command); err != nil {
		return HistoryEvent{}, err
	}
	parsed, err := timeFromString(at)
	if err != nil {
		return HistoryEvent{}, fmt.Errorf("reading port_events: bad timestamp %q: %w", at, err)
	}
	e.At = parsed
	return e, nil
}

// Append records one port event. The daemon calls it on the scanner goroutine
// after publishing a delta, never on the RPC path.
func (s *Store) Append(e HistoryEvent) error {
	return s.insertHistory([]HistoryEvent{e})
}

// AppendBatch records several events in one transaction. A scan tick that
// sees a whole group come up writes one transaction instead of N.
func (s *Store) AppendBatch(events []HistoryEvent) error {
	return s.insertHistory(events)
}

// insertHistory is the ring's only write path: validate every event, then
// write the whole set in one transaction with one prepared statement. A bad
// event is refused before the transaction opens, so nothing of a rejected
// batch can ever be visible.
func (s *Store) insertHistory(events []HistoryEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows := make([]historyRow, len(events))
	for i, e := range events {
		row, err := encodeHistory(e)
		if err != nil {
			return err
		}
		rows[i] = row
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("recording port events: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.Prepare(`INSERT INTO port_events (` + historyColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("recording port events: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i := range rows {
		if _, err := stmt.Exec(rows[i].args()...); err != nil {
			return fmt.Errorf("recording port events: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("recording port events: %w", err)
	}
	return nil
}

// Query returns events newest first. port filters to one port when non-nil,
// since drops anything older when non-zero, and limit <= 0 means
// DefaultHistoryLimit.
func (s *Store) Query(port *int, since time.Time, limit int) ([]HistoryEvent, error) {
	if limit <= 0 {
		limit = DefaultHistoryLimit
	}
	query, args := historySelect(port, since, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading port_events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]HistoryEvent, 0, min(limit, 128))
	for rows.Next() {
		e, err := decodeHistory(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// historySelect builds the ring's read statement: newest first, id as the
// tie-breaker so rows written in one transaction still come back in reverse
// insertion order.
func historySelect(port *int, since time.Time, limit int) (string, []any) {
	var (
		where []string
		args  []any
	)
	if port != nil {
		where = append(where, "port = ?")
		args = append(args, *port)
	}
	if !since.IsZero() {
		where = append(where, "at >= ?")
		args = append(args, timeToString(since))
	}
	query := "SELECT " + historyColumns + " FROM port_events"
	if len(where) > 0 {
		query += " WHERE " + strings.Join(where, " AND ")
	}
	query += " ORDER BY at DESC, id DESC LIMIT ?"
	return query, append(args, limit)
}

// HistoryCount is the number of rows currently in the ring.
func (s *Store) HistoryCount() (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM port_events`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting port_events: %w", err)
	}
	return n, nil
}
