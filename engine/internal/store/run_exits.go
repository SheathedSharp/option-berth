package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// RunExitRow is the durable form of one run that has ended. It deliberately
// carries the fields needed to render runs.list and status without importing
// the daemon's run registry into the store package.
type RunExitRow struct {
	ID         string
	PID        int
	Group      string
	Name       string
	Cmd        string
	Cwd        string
	PortHint   int
	StartedAt  time.Time
	ConfigPath string
	StartID    string
	Origin     string
	LogPath    string
	LogOffset  int64
	Code       int
	Reason     string
	ExitedAt   time.Time
	LastLines  []string
}

// RunExitLimit is the number of finished runs retained by the daemon. The
// registry applies the same bound in memory; keeping it here makes the SQL
// query's retention contract explicit at the storage boundary.
const RunExitLimit = 100

// AppendRunExit records one finished run and prunes older rows so only the
// newest RunExitLimit entries remain.
func (s *Store) AppendRunExit(row RunExitRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: run exit store is not open")
	}
	if row.StartedAt.IsZero() {
		row.StartedAt = row.ExitedAt
	}
	if row.ExitedAt.IsZero() {
		row.ExitedAt = time.Now()
	}
	lines := row.LastLines
	if lines == nil {
		lines = []string{}
	}
	raw, err := json.Marshal(lines)
	if err != nil {
		return fmt.Errorf("store: encoding run exit log: %w", err)
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("store: beginning run exit transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	_, err = tx.Exec(`
		INSERT INTO run_exits(
			run_id, pid, group_name, service_name, cmd, cwd, port_hint,
			started_at, config_path, start_id, origin, log_path, log_offset,
			code, reason, exited_at, last_lines
		) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.PID, row.Group, row.Name, row.Cmd, row.Cwd, row.PortHint,
		timeToString(row.StartedAt), row.ConfigPath, row.StartID, row.Origin,
		row.LogPath, row.LogOffset, row.Code, row.Reason, timeToString(row.ExitedAt),
		string(raw),
	)
	if err != nil {
		return fmt.Errorf("store: recording run exit: %w", err)
	}
	_, err = tx.Exec(`
		DELETE FROM run_exits
		WHERE id NOT IN (SELECT id FROM run_exits ORDER BY exited_at DESC, id DESC LIMIT ?)`, RunExitLimit)
	if err != nil {
		return fmt.Errorf("store: pruning run exits: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: committing run exit: %w", err)
	}
	return nil
}

// RunExits returns finished runs newest first. A non-positive limit uses the
// registry's bounded history size.
func (s *Store) RunExits(limit int) ([]RunExitRow, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("store: run exit store is not open")
	}
	if limit <= 0 {
		limit = RunExitLimit
	}
	rows, err := s.db.Query(`
		SELECT run_id, pid, group_name, service_name, cmd, cwd, port_hint,
		       started_at, config_path, start_id, origin, log_path, log_offset,
		       code, reason, exited_at, last_lines
		  FROM run_exits
		 ORDER BY exited_at DESC, id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("store: reading run exits: %w", err)
	}
	defer func() { _ = rows.Close() }()

	out := make([]RunExitRow, 0, limit)
	for rows.Next() {
		var (
			row    RunExitRow
			start  string
			exited string
			lines  string
		)
		if err := rows.Scan(
			&row.ID, &row.PID, &row.Group, &row.Name, &row.Cmd, &row.Cwd, &row.PortHint,
			&start, &row.ConfigPath, &row.StartID, &row.Origin, &row.LogPath, &row.LogOffset,
			&row.Code, &row.Reason, &exited, &lines,
		); err != nil {
			return nil, fmt.Errorf("store: reading run exit: %w", err)
		}
		row.StartedAt = timeOrZero(start)
		row.ExitedAt = timeOrZero(exited)
		if strings.TrimSpace(lines) == "" {
			row.LastLines = []string{}
		} else if err := json.Unmarshal([]byte(lines), &row.LastLines); err != nil {
			return nil, fmt.Errorf("store: reading run exit log: %w", err)
		}
		if row.LastLines == nil {
			row.LastLines = []string{}
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: reading run exits: %w", err)
	}
	return out, nil
}

// CorrectLatestRunExit changes the latest recent exit for pid. A stop request
// can race with the child reaper, so the registry may first record a crash and
// then correct it to stopped.
func (s *Store) CorrectLatestRunExit(pid int, since time.Time, reason string) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: run exit store is not open")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	var id int64
	err := s.db.QueryRow(`
		SELECT id FROM run_exits
		 WHERE pid = ? AND exited_at >= ?
		 ORDER BY exited_at DESC, id DESC LIMIT 1`, pid, timeToString(since)).Scan(&id)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil
		}
		return fmt.Errorf("store: finding run exit to correct: %w", err)
	}
	if _, err := s.db.Exec(`UPDATE run_exits SET reason = ? WHERE id = ?`, reason, id); err != nil {
		return fmt.Errorf("store: correcting run exit: %w", err)
	}
	return nil
}

// CorrectRunExit changes only the exact event the registry observed. The
// legacy PID-only helper above remains for compatibility, but production reap
// correction must not select a different generation after PID reuse.
func (s *Store) CorrectRunExit(row RunExitRow) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("store: run exit store is not open")
	}
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.Exec(`UPDATE run_exits SET reason = ?
		WHERE run_id = ? AND pid = ? AND started_at = ? AND exited_at = ?`,
		row.Reason, row.ID, row.PID, timeToString(row.StartedAt), timeToString(row.ExitedAt))
	if err != nil {
		return fmt.Errorf("store: correcting run exit generation: %w", err)
	}
	return nil
}

// RenameRunExitGroups follows a project rename in the durable history.
func (s *Store) RenameRunExitGroups(renames map[string]string) error {
	if len(renames) == 0 {
		return nil
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("store: run exit store is not open")
	}
	keys := make([]string, 0, len(renames))
	for from, to := range renames {
		if strings.TrimSpace(from) != "" && strings.TrimSpace(to) != "" {
			keys = append(keys, from)
		}
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	var query strings.Builder
	query.WriteString("UPDATE run_exits SET group_name = CASE group_name")
	args := make([]any, 0, len(keys)*3)
	for _, from := range keys {
		query.WriteString(" WHEN ? THEN ?")
		args = append(args, from, renames[from])
	}
	query.WriteString(" ELSE group_name END WHERE group_name IN (")
	for i, from := range keys {
		if i > 0 {
			query.WriteByte(',')
		}
		query.WriteByte('?')
		args = append(args, from)
	}
	query.WriteByte(')')
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if _, err := s.db.Exec(query.String(), args...); err != nil {
		return fmt.Errorf("store: renaming run exits: %w", err)
	}
	return nil
}
