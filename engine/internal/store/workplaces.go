package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The workplaces and workplace_blocks tables (migration 009).
//
// A workplace is a named scope under one worktree; its blocks are the work
// items recorded in it. This file is the persistence half only: it stores
// rows and enforces the two rules that must hold whatever the caller does —
// one active workplace per worktree, and a block that never moves backwards.
// Naming, selectors and what a block means belong to internal/workplace.

// WorkplaceBlockLimit caps how many blocks one workplace keeps. The oldest go
// first: a workplace is a record of recent work, and an unbounded one would
// grow a text column without limit.
const WorkplaceBlockLimit = 500

// workplaceColumns is the workplaces table's column list in scan order,
// shared by every statement here so the write and the reads cannot drift.
const workplaceColumns = `id, worktree, name, active, created_at, updated_at`

// blockColumns is the same for workplace_blocks.
const blockColumns = `id, workplace, seq, kind, source, input, status, provider, run_id, pid, log_path, output, exit_code, started_at, ended_at`

// WorkplaceRow is one workplace. Blocks is the count of its blocks, filled by
// List; a single-row read leaves it zero.
type WorkplaceRow struct {
	ID        string
	Worktree  string
	Name      string
	Active    bool
	CreatedAt time.Time
	UpdatedAt time.Time
	Blocks    int
}

// BlockRow is one recorded work item. EndedAt is the zero time while the work
// is still running; ExitCode is nil when there is none to report.
type BlockRow struct {
	ID        string
	Workplace string
	Seq       int64
	Kind      string
	Source    string
	Input     string
	Status    string
	Provider  string
	RunID     string
	PID       int
	LogPath   string
	Output    string
	ExitCode  *int
	StartedAt time.Time
	EndedAt   time.Time
}

// WorkplaceStore is the workplaces tables. Get one from Store.Workplaces().
type WorkplaceStore struct{ s *Store }

// Workplaces returns the handle onto the workplaces tables.
func (s *Store) Workplaces() WorkplaceStore { return WorkplaceStore{s: s} }

// Create adds a workplace. When row.Active is set the same transaction clears
// the worktree's previous active row first, which is what keeps the partial
// unique index satisfiable rather than something every caller must remember.
func (t WorkplaceStore) Create(row WorkplaceRow) error {
	if row.ID == "" {
		return errors.New("store: a workplace needs an id")
	}
	if row.Worktree == "" {
		return errors.New("store: a workplace needs a worktree")
	}
	now := time.Now().UTC()
	if row.CreatedAt.IsZero() {
		row.CreatedAt = now
	}
	if row.UpdatedAt.IsZero() {
		row.UpdatedAt = row.CreatedAt
	}

	t.s.wmu.Lock()
	defer t.s.wmu.Unlock()

	tx, err := t.s.db.Begin()
	if err != nil {
		return fmt.Errorf("creating workplace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if row.Active {
		if _, err := tx.Exec(`UPDATE workplaces SET active = 0 WHERE worktree = ?`, row.Worktree); err != nil {
			return fmt.Errorf("creating workplace: %w", err)
		}
	}
	if _, err := tx.Exec(
		`INSERT INTO workplaces(`+workplaceColumns+`) VALUES(?, ?, ?, ?, ?, ?)`,
		row.ID, row.Worktree, row.Name, boolInt(row.Active),
		timeToString(row.CreatedAt), timeToString(row.UpdatedAt),
	); err != nil {
		return fmt.Errorf("creating workplace %q: %w", row.ID, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("creating workplace: %w", err)
	}
	return nil
}

// List returns a worktree's workplaces, oldest first, each with its block
// count. Ordering by creation rather than by activity is deliberate: the list
// is a record of the session, and reordering it as work happens would move
// rows under the reader.
func (t WorkplaceStore) List(worktree string) ([]WorkplaceRow, error) {
	rows, err := t.s.db.Query(
		`SELECT w.id, w.worktree, w.name, w.active, w.created_at, w.updated_at,
		        (SELECT count(*) FROM workplace_blocks b WHERE b.workplace = w.id)
		   FROM workplaces w
		  WHERE w.worktree = ?
		  ORDER BY w.created_at, w.id`, worktree)
	if err != nil {
		return nil, fmt.Errorf("reading workplaces: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []WorkplaceRow
	for rows.Next() {
		r, err := scanWorkplace(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("reading workplaces: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns one workplace by id, with its block count — the same row List
// carries, so a caller that just changed one does not have to list them all to
// say what it now looks like.
func (t WorkplaceStore) Get(id string) (WorkplaceRow, bool, error) {
	if id == "" {
		return WorkplaceRow{}, false, nil
	}
	r, err := scanWorkplace(t.s.db.QueryRow(
		`SELECT w.id, w.worktree, w.name, w.active, w.created_at, w.updated_at,
		        (SELECT count(*) FROM workplace_blocks b WHERE b.workplace = w.id)
		   FROM workplaces w WHERE w.id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return WorkplaceRow{}, false, nil
	}
	if err != nil {
		return WorkplaceRow{}, false, fmt.Errorf("reading workplace %q: %w", id, err)
	}
	return r, true, nil
}

// SetActive makes one workplace the target new work lands in. It refuses an id
// that belongs to another worktree rather than silently doing nothing: the
// caller asked for a switch, and a switch that did not happen is a failure.
func (t WorkplaceStore) SetActive(worktree, id string) error {
	t.s.wmu.Lock()
	defer t.s.wmu.Unlock()

	tx, err := t.s.db.Begin()
	if err != nil {
		return fmt.Errorf("activating workplace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var owner string
	err = tx.QueryRow(`SELECT worktree FROM workplaces WHERE id = ?`, id).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("activating workplace: %w", ErrWorkplaceNotFound)
	}
	if err != nil {
		return fmt.Errorf("activating workplace: %w", err)
	}
	if owner != worktree {
		return fmt.Errorf("activating workplace %q: %w", id, ErrWorkplaceElsewhere)
	}
	if _, err := tx.Exec(`UPDATE workplaces SET active = 0 WHERE worktree = ?`, worktree); err != nil {
		return fmt.Errorf("activating workplace: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE workplaces SET active = 1, updated_at = ? WHERE id = ?`,
		nowString(), id); err != nil {
		return fmt.Errorf("activating workplace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("activating workplace: %w", err)
	}
	return nil
}

// Errors SetActive reports. They are sentinel values so the daemon can map
// them onto the wire codes without matching on message text.
var (
	ErrWorkplaceNotFound  = errors.New("no such workplace")
	ErrWorkplaceElsewhere = errors.New("that workplace belongs to another worktree")
)

// Delete removes a workplace and its blocks in one transaction. When the row
// that went was the active one, the worktree's most recently created
// remaining workplace becomes active; when none is left, no workplace is
// active and the returned id is empty. Deleting something already gone
// reports false rather than failing: the caller asked for it to not be there.
func (t WorkplaceStore) Delete(id string) (bool, string, error) {
	t.s.wmu.Lock()
	defer t.s.wmu.Unlock()

	tx, err := t.s.db.Begin()
	if err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var worktree string
	var active int
	err = tx.QueryRow(`SELECT worktree, active FROM workplaces WHERE id = ?`, id).
		Scan(&worktree, &active)
	if errors.Is(err, sql.ErrNoRows) {
		return false, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	// Blocks go explicitly rather than through ON DELETE CASCADE: the daemon
	// opens the database with foreign keys on, the CLI's read-only path does
	// not, and the record must not depend on which of them ran the delete.
	if _, err := tx.Exec(`DELETE FROM workplace_blocks WHERE workplace = ?`, id); err != nil {
		return false, "", fmt.Errorf("deleting workplace blocks: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM workplaces WHERE id = ?`, id); err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	if active == 0 {
		if err := tx.Commit(); err != nil {
			return false, "", fmt.Errorf("deleting workplace: %w", err)
		}
		return true, "", nil
	}

	var next string
	err = tx.QueryRow(
		`SELECT id FROM workplaces WHERE worktree = ? ORDER BY created_at DESC, id DESC LIMIT 1`,
		worktree).Scan(&next)
	if errors.Is(err, sql.ErrNoRows) {
		if err := tx.Commit(); err != nil {
			return false, "", fmt.Errorf("deleting workplace: %w", err)
		}
		return true, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	if _, err := tx.Exec(`UPDATE workplaces SET active = 1 WHERE id = ?`, next); err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, "", fmt.Errorf("deleting workplace: %w", err)
	}
	return true, next, nil
}

// AppendBlock records a new block, giving it the next sequence number in its
// workplace. A block id that is already recorded is refused: Append is for a
// work item nobody has seen before, and Upsert is for a second observation of
// one that exists.
func (t WorkplaceStore) AppendBlock(row BlockRow) (BlockRow, error) {
	if row.ID == "" {
		return BlockRow{}, errors.New("store: a block needs an id")
	}
	if row.Workplace == "" {
		return BlockRow{}, errors.New("store: a block needs a workplace")
	}
	if row.StartedAt.IsZero() {
		row.StartedAt = time.Now().UTC()
	}

	t.s.wmu.Lock()
	defer t.s.wmu.Unlock()

	tx, err := t.s.db.Begin()
	if err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var exists int
	if err := tx.QueryRow(`SELECT count(*) FROM workplace_blocks WHERE id = ?`, row.ID).Scan(&exists); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	if exists > 0 {
		return BlockRow{}, fmt.Errorf("recording block %q: %w", row.ID, ErrBlockExists)
	}

	row, err = insertBlock(tx, row)
	if err != nil {
		return BlockRow{}, err
	}
	if err := trimBlocks(tx, row.Workplace); err != nil {
		return BlockRow{}, err
	}
	if _, err := tx.Exec(`UPDATE workplaces SET updated_at = ? WHERE id = ?`, nowString(), row.Workplace); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	return row, nil
}

// ErrBlockExists is returned by AppendBlock for an id that is already
// recorded; the caller wants Upsert instead.
var ErrBlockExists = errors.New("that block is already recorded")

// UpsertBlock records an observation of a work item: the first one inserts it,
// a later one updates it. The record never moves backwards — a status that is
// an outcome is not replaced by one that is not, and a field the new
// observation leaves empty does not erase what an earlier one wrote. That is
// what lets the two ends of a work item arrive in either order without the
// outcome being lost.
//
// outcomes names the statuses that are outcomes rather than states work is
// still in. The vocabulary belongs to the caller — internal/workplace defines
// it — because this package stores what it is told and does not keep a second
// copy of what a status means.
func (t WorkplaceStore) UpsertBlock(row BlockRow, outcomes []string) (BlockRow, error) {
	if row.ID == "" {
		return BlockRow{}, errors.New("store: a block needs an id")
	}
	if row.Workplace == "" {
		return BlockRow{}, errors.New("store: a block needs a workplace")
	}
	if row.StartedAt.IsZero() {
		row.StartedAt = time.Now().UTC()
	}

	t.s.wmu.Lock()
	defer t.s.wmu.Unlock()

	tx, err := t.s.db.Begin()
	if err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := blockIn(tx, row.ID)
	if err != nil {
		return BlockRow{}, err
	}
	switch {
	case !found:
		row, err = insertBlock(tx, row)
		if err != nil {
			return BlockRow{}, err
		}
		if err := trimBlocks(tx, row.Workplace); err != nil {
			return BlockRow{}, err
		}
	case existing.Workplace != row.Workplace:
		return BlockRow{}, fmt.Errorf("recording block %q: %w", row.ID, ErrBlockElsewhere)
	default:
		row = mergeBlock(existing, row, outcomeSet(outcomes))
		if err := updateBlock(tx, row); err != nil {
			return BlockRow{}, err
		}
	}
	if _, err := tx.Exec(`UPDATE workplaces SET updated_at = ? WHERE id = ?`, nowString(), row.Workplace); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	return row, nil
}

// ErrBlockElsewhere is returned when a block id is re-used inside a different
// workplace: ids name one work item, and moving one between workplaces would
// rewrite history rather than add to it.
var ErrBlockElsewhere = errors.New("that block belongs to another workplace")

// Blocks returns a workplace's blocks oldest first. A limit above zero keeps
// the newest limit blocks and reports them oldest first; the workplace's own
// cap (WorkplaceBlockLimit) applies on write, so a read without a limit is
// bounded too.
func (t WorkplaceStore) Blocks(workplace string, limit int) ([]BlockRow, error) {
	if limit <= 0 || limit > WorkplaceBlockLimit {
		limit = WorkplaceBlockLimit
	}
	rows, err := t.s.db.Query(
		`SELECT `+blockColumns+` FROM (
		     SELECT `+blockColumns+` FROM workplace_blocks
		      WHERE workplace = ? ORDER BY seq DESC LIMIT ?
		 ) ORDER BY seq`, workplace, limit)
	if err != nil {
		return nil, fmt.Errorf("reading workplace blocks: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []BlockRow
	for rows.Next() {
		b, err := scanBlock(rows.Scan)
		if err != nil {
			return nil, fmt.Errorf("reading workplace blocks: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// BlockCount reports how many blocks a workplace has, which is what tells a
// truncated read from a complete one.
func (t WorkplaceStore) BlockCount(workplace string) (int, error) {
	var n int
	if err := t.s.db.QueryRow(
		`SELECT count(*) FROM workplace_blocks WHERE workplace = ?`, workplace).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting workplace blocks: %w", err)
	}
	return n, nil
}

// CloseBlocksByRun closes every block of one run that has no outcome yet, and
// reports how many it closed.
//
// It is how a run the CLI did not wait for — a detached `oberth start` — gets
// an outcome: the daemon owns that process and learns how it ended, and the
// run's id is what ties the two together. outcomes is the same vocabulary
// UpsertBlock takes, and it is what keeps this from overwriting an observation
// that already arrived: a block that has one is not closed again.
func (t WorkplaceStore) CloseBlocksByRun(runID, status string, code *int, endedAt time.Time, outcomes []string) (int, error) {
	if runID == "" {
		return 0, nil
	}
	if endedAt.IsZero() {
		endedAt = time.Now().UTC()
	}
	args := []any{status, exitCodeValue(code), timeToString(endedAt), runID}
	placeholders := make([]string, 0, len(outcomes))
	for _, outcome := range outcomes {
		placeholders = append(placeholders, "?")
		args = append(args, outcome)
	}
	if len(placeholders) == 0 {
		// No vocabulary means nothing is an outcome, so every block of the run
		// is still open. A caller that got here passed an empty list by
		// mistake; closing everything is the reading that matches it.
		placeholders = append(placeholders, "''")
	}
	return t.s.execRows(
		`UPDATE workplace_blocks
		    SET status = ?, exit_code = ?, ended_at = ?
		  WHERE run_id = ? AND status NOT IN (`+strings.Join(placeholders, ", ")+`)`,
		args...)
}

// insertBlock writes a new row with the next sequence number in its workplace.
func insertBlock(tx *sql.Tx, row BlockRow) (BlockRow, error) {
	var max sql.NullInt64
	if err := tx.QueryRow(
		`SELECT max(seq) FROM workplace_blocks WHERE workplace = ?`, row.Workplace).Scan(&max); err != nil {
		return BlockRow{}, fmt.Errorf("recording block: %w", err)
	}
	row.Seq = max.Int64 + 1
	if _, err := tx.Exec(
		`INSERT INTO workplace_blocks(`+blockColumns+`) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		row.ID, row.Workplace, row.Seq, row.Kind, row.Source, row.Input, row.Status,
		row.Provider, row.RunID, row.PID, row.LogPath, row.Output, exitCodeValue(row.ExitCode),
		timeToString(row.StartedAt), timeOrEmpty(row.EndedAt),
	); err != nil {
		return BlockRow{}, fmt.Errorf("recording block %q: %w", row.ID, err)
	}
	return row, nil
}

// updateBlock writes a merged observation back. seq is not part of it: the
// order work happened in is decided by the first observation, not the last.
func updateBlock(tx *sql.Tx, row BlockRow) error {
	if _, err := tx.Exec(
		`UPDATE workplace_blocks SET
		     kind = ?, source = ?, input = ?, status = ?, provider = ?, run_id = ?,
		     pid = ?, log_path = ?, output = ?, exit_code = ?, started_at = ?, ended_at = ?
		 WHERE id = ?`,
		row.Kind, row.Source, row.Input, row.Status, row.Provider, row.RunID,
		row.PID, row.LogPath, row.Output, exitCodeValue(row.ExitCode),
		timeToString(row.StartedAt), timeOrEmpty(row.EndedAt), row.ID,
	); err != nil {
		return fmt.Errorf("recording block %q: %w", row.ID, err)
	}
	return nil
}

// mergeBlock applies a later observation to a recorded block.
//
// Having an outcome is a property of the status, not a separate flag: once a
// work item has one, only another outcome replaces it.
func mergeBlock(existing, incoming BlockRow, outcomes map[string]bool) BlockRow {
	merged := existing
	if incoming.Status != "" && !(outcomes[existing.Status] && !outcomes[incoming.Status]) {
		merged.Status = incoming.Status
	}
	for _, f := range []struct {
		dst *string
		src string
	}{
		{&merged.Kind, incoming.Kind},
		{&merged.Source, incoming.Source},
		{&merged.Input, incoming.Input},
		{&merged.Provider, incoming.Provider},
		{&merged.RunID, incoming.RunID},
		{&merged.LogPath, incoming.LogPath},
		{&merged.Output, incoming.Output},
	} {
		if f.src != "" {
			*f.dst = f.src
		}
	}
	if incoming.PID != 0 {
		merged.PID = incoming.PID
	}
	if incoming.ExitCode != nil {
		code := *incoming.ExitCode
		merged.ExitCode = &code
	}
	if !incoming.StartedAt.IsZero() && merged.StartedAt.IsZero() {
		merged.StartedAt = incoming.StartedAt
	}
	if !incoming.EndedAt.IsZero() {
		merged.EndedAt = incoming.EndedAt
	}
	return merged
}

// outcomeSet indexes the caller's outcome vocabulary for the merge.
func outcomeSet(outcomes []string) map[string]bool {
	set := make(map[string]bool, len(outcomes))
	for _, status := range outcomes {
		set[status] = true
	}
	return set
}

// trimBlocks drops the oldest blocks beyond the per-workplace cap.
func trimBlocks(tx *sql.Tx, workplace string) error {
	if _, err := tx.Exec(
		`DELETE FROM workplace_blocks
		  WHERE workplace = ? AND seq <= (
		      SELECT max(seq) - ? FROM workplace_blocks WHERE workplace = ?
		  )`, workplace, WorkplaceBlockLimit, workplace); err != nil {
		return fmt.Errorf("trimming workplace blocks: %w", err)
	}
	return nil
}

// blockIn reads one block inside an open transaction.
func blockIn(tx *sql.Tx, id string) (BlockRow, bool, error) {
	b, err := scanBlock(tx.QueryRow(`SELECT `+blockColumns+` FROM workplace_blocks WHERE id = ?`, id).Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return BlockRow{}, false, nil
	}
	if err != nil {
		return BlockRow{}, false, fmt.Errorf("reading block %q: %w", id, err)
	}
	return b, true, nil
}

// scanWorkplace reads one row in the workplaces column order plus the block
// count every read of this table carries.
func scanWorkplace(scan func(dest ...any) error) (WorkplaceRow, error) {
	var (
		r         WorkplaceRow
		active    int
		createdAt string
		updatedAt string
	)
	if err := scan(&r.ID, &r.Worktree, &r.Name, &active, &createdAt, &updatedAt, &r.Blocks); err != nil {
		return WorkplaceRow{}, err
	}
	r.Active = active != 0
	r.CreatedAt = timeOrZero(createdAt)
	r.UpdatedAt = timeOrZero(updatedAt)
	return r, nil
}

func scanBlock(scan func(dest ...any) error) (BlockRow, error) {
	var (
		b        BlockRow
		exitCode sql.NullInt64
		started  string
		ended    string
	)
	if err := scan(&b.ID, &b.Workplace, &b.Seq, &b.Kind, &b.Source, &b.Input, &b.Status,
		&b.Provider, &b.RunID, &b.PID, &b.LogPath, &b.Output, &exitCode, &started, &ended); err != nil {
		return BlockRow{}, err
	}
	if exitCode.Valid {
		code := int(exitCode.Int64)
		b.ExitCode = &code
	}
	b.StartedAt = timeOrZero(started)
	b.EndedAt = timeOrZero(ended)
	return b, nil
}

func exitCodeValue(code *int) any {
	if code == nil {
		return nil
	}
	return *code
}

func timeOrEmpty(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return timeToString(t)
}
