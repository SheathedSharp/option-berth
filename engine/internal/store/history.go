package store

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// The port-event ring: one table, one writer, newest-first reads. Retention is
// SQL-side — the port_events ring trigger in migration 001 keeps the newest
// HistoryCapacity rows — so this file owns encoding an event into its row and
// writing a batch of them.

// The event kinds this ring records. They are the port-scoped subset of the
// events the daemon publishes.
const (
	EventPortUp        = "port_up"
	EventPortDown      = "port_down"
	EventPortRestarted = "port_restarted"
)

// HistoryEvent is one row of the port_events ring. The first six fields are the
// wire shape; the last three are columns the table carries and that are omitted
// from JSON when empty.
type HistoryEvent struct {
	// At is when the event happened, in UTC.
	At time.Time `json:"at"`
	// Kind is one of the EventPort* constants.
	Kind string `json:"kind"`
	// Port is the listening port the event is about.
	Port int `json:"port"`
	// PID is the process that owned the port, when it was observed.
	PID int `json:"pid"`
	// DisplayName is the human name the port had at that moment.
	DisplayName string `json:"display_name"`
	// Group is the project group the port belonged to, when known.
	Group string `json:"group"`

	// The remaining columns are evidence kept for diagnosis; each is omitted
	// from the wire shape when it was not observed.
	Bind        string `json:"bind,omitempty"`
	ProjectRoot string `json:"project_root,omitempty"`
	Command     string `json:"command,omitempty"`
}

// DefaultHistoryLimit is how many events a read returns when it names no limit.
const DefaultHistoryLimit = 50

// HistoryCapacity is how many rows the ring keeps. The limit itself is enforced
// in SQL by the port_events_ring trigger in migration 001; this constant exists
// so a caller can talk about the number without restating it.
const HistoryCapacity = 10000

// historyColumns is the ring's column list in insert order, shared by the write
// statement and the read statement so the two cannot drift apart.
const historyColumns = `at, kind, port, bind, pid, display_name, group_name, project_root, command`

// historyRow is one event with every column already in its stored form.
// Encoding before the transaction opens is what lets a batch be refused whole.
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

// encodeHistory turns an event into its row. A missing kind is refused because
// no reader could interpret the row; a missing time is stamped now, because
// every writer records "this just happened".
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

// decodeHistory reads one row in historyColumns order.
func decodeHistory(scan func(dest ...any) error) (HistoryEvent, error) {
	var (
		event HistoryEvent
		at    string
	)
	if err := scan(&at, &event.Kind, &event.Port, &event.Bind, &event.PID, &event.DisplayName,
		&event.Group, &event.ProjectRoot, &event.Command); err != nil {
		return HistoryEvent{}, err
	}
	parsed, err := timeFromString(at)
	if err != nil {
		return HistoryEvent{}, fmt.Errorf("reading port_events: bad timestamp %q: %w", at, err)
	}
	event.At = parsed
	return event, nil
}

// AppendBatch records several events in one transaction. That is the only write
// path the ring has: a scan tick that sees a whole group come up writes one
// transaction instead of one per service, and a batch with a bad event is
// refused before the transaction opens, so nothing of it can become visible.
func (s *Store) AppendBatch(events []HistoryEvent) error {
	if len(events) == 0 {
		return nil
	}
	rows, err := encodeHistoryBatch(events)
	if err != nil {
		return err
	}
	return s.write("recording port events", func(tx writeTx) error {
		statement, err := tx.Prepare(
			`INSERT INTO port_events (` + historyColumns + `) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			return err
		}
		defer func() { _ = statement.Close() }()
		for i := range rows {
			if _, err := statement.Exec(rows[i].args()...); err != nil {
				return err
			}
		}
		return nil
	})
}

// encodeHistoryBatch prepares every row before the transaction opens.
func encodeHistoryBatch(events []HistoryEvent) ([]historyRow, error) {
	rows := make([]historyRow, len(events))
	for i, event := range events {
		row, err := encodeHistory(event)
		if err != nil {
			return nil, err
		}
		rows[i] = row
	}
	return rows, nil
}

// History returns events newest first, filtered by port when one is named and
// by time when `since` is not zero.
func (s *Store) History(filter HistoryFilter) ([]HistoryEvent, error) {
	query, args := historySelect(filter)
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading port_events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]HistoryEvent, 0, min(filter.limit(), 128))
	for rows.Next() {
		event, err := decodeHistory(rows.Scan)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

// HistoryCount is how many rows the ring currently holds.
func (s *Store) HistoryCount() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT count(*) FROM port_events`).Scan(&count); err != nil {
		return 0, fmt.Errorf("counting port_events: %w", err)
	}
	return count, nil
}

// HistoryFilter is what a history read asks for: one port, one starting time,
// and a row budget.
type HistoryFilter struct {
	// Port restricts the read to one port when set.
	Port *int
	// Since drops anything older than this when it is not the zero time.
	Since time.Time
	// Limit is the most rows to return; zero or less means
	// DefaultHistoryLimit.
	Limit int
}

func (f HistoryFilter) limit() int {
	if f.Limit <= 0 {
		return DefaultHistoryLimit
	}
	return f.Limit
}

// historySelect builds the read statement: newest first, with id as the
// tie-breaker so rows written in one transaction still come back in reverse
// insertion order.
func historySelect(filter HistoryFilter) (string, []any) {
	var (
		conditions []string
		args       []any
	)
	if filter.Port != nil {
		conditions = append(conditions, "port = ?")
		args = append(args, *filter.Port)
	}
	if !filter.Since.IsZero() {
		conditions = append(conditions, "at >= ?")
		args = append(args, timeToString(filter.Since))
	}
	query := "SELECT " + historyColumns + " FROM port_events"
	if len(conditions) > 0 {
		query += " WHERE " + strings.Join(conditions, " AND ")
	}
	return query + " ORDER BY at DESC, id DESC LIMIT ?", append(args, filter.limit())
}
