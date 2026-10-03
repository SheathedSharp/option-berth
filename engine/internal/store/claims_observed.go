package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
)

// ErrClaimsChanged means the observed reservation set no longer describes
// storage. Callers must observe again, not retry an unconditional key delete.
var ErrClaimsChanged = errors.New("port reservations changed after observation")

// ClaimObservation is an operation-owned value snapshot. Rows must not be
// changed by production callers. Its store-local revision detects even an
// identical refresh or delete/recreate through the same Store, without holding
// a database or claims lock while processes are being stopped.
type ClaimObservation struct {
	Rows     []ClaimRow
	owner    *Store
	revision uint64
}

// ObserveContext captures selected complete keys in one read transaction.
// It does not sweep expiry. No long-lived transaction escapes this function.
func (c Claims) ObserveContext(ctx context.Context, keys ...string) (ClaimObservation, error) {
	if err := ctx.Err(); err != nil {
		return ClaimObservation{}, err
	}
	if !c.s.wmu.TryLock() {
		return ClaimObservation{}, errors.New("store: claims writer is busy; retry observation")
	}
	defer c.s.wmu.Unlock()
	tx, err := c.s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClaimObservation{}, err
	}
	defer func() { _ = tx.Rollback() }()
	unique := make(map[string]bool, len(keys))
	for _, key := range keys {
		unique[key] = true
	}
	ordered := make([]string, 0, len(unique))
	for key := range unique {
		ordered = append(ordered, key)
	}
	sort.Strings(ordered)
	observation := ClaimObservation{owner: c.s, revision: c.s.claimsRevision}
	for _, key := range ordered {
		got, err := readObservedClaims(ctx, tx, key)
		if err != nil {
			return ClaimObservation{}, err
		}
		observation.Rows = append(observation.Rows, got...)
	}
	if err := tx.Commit(); err != nil {
		return ClaimObservation{}, err
	}
	return observation, nil
}

func readObservedClaims(ctx context.Context, tx *sql.Tx, key string) ([]ClaimRow, error) {
	rows, err := tx.QueryContext(ctx, `SELECT `+claimColumns+` FROM claims WHERE key = ? ORDER BY port`, key)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var result []ClaimRow
	for rows.Next() {
		row, err := decodeClaim(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return result, nil
}

// DeleteObservedContext removes only complete keys whose rows still equal the
// captured set. Comparison and deletion share one transaction; a refreshed,
// added, removed or reassigned row retains the entire set. No expiry sweep is
// performed here: a failed stop must not release unrelated reservations.
//
// A store-local revision rejects intervening writes through this Store; SQL
// value comparison also detects changed rows from other connections. An external
// writer recreating byte-identical rows remains indistinguishable under the
// existing schema, so arbitrary multi-writer lease protocols are not promised.
func (c Claims) DeleteObservedContext(ctx context.Context, observation ClaimObservation) (int, error) {
	observed := observation.Rows
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(observed) == 0 {
		return 0, nil
	}
	byKey := make(map[string]map[int]claimEncoded)
	ports := make(map[int]bool, len(observed))
	for _, row := range observed {
		if row.CreatedAt.IsZero() || row.ExpiresAt.IsZero() {
			return 0, errors.New("store: reservation observation needs both timestamps")
		}
		e, err := encodeClaim(row)
		if err != nil {
			return 0, err
		}
		if ports[e.port] {
			return 0, errors.New("store: duplicate observed reservation port")
		}
		ports[e.port] = true
		if byKey[e.key] == nil {
			byKey[e.key] = make(map[int]claimEncoded)
		}
		byKey[e.key][e.port] = e
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	// Refuse contention rather than adding an uncancellable mutex wait to down.
	if !c.s.wmu.TryLock() {
		return 0, errors.New("store: reservation release writer is busy; retry after observing")
	}
	defer c.s.wmu.Unlock()
	if observation.owner != c.s || observation.revision != c.s.claimsRevision {
		return 0, ErrClaimsChanged
	}
	tx, err := c.s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("releasing observed reservations: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, key := range keys {
		if err := matchingClaims(ctx, tx, key, byKey[key]); err != nil {
			return 0, err
		}
	}
	removed := 0
	for _, key := range keys {
		result, err := tx.ExecContext(ctx, `DELETE FROM claims WHERE key = ?`, key)
		if err != nil {
			return 0, fmt.Errorf("releasing observed reservations: %w", err)
		}
		n, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		if n != int64(len(byKey[key])) {
			return 0, ErrClaimsChanged
		}
		removed += int(n)
	}
	c.s.claimsRevision++
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("releasing observed reservations: %w", err)
	}
	return removed, nil
}

func matchingClaims(ctx context.Context, tx *sql.Tx, key string, expected map[int]claimEncoded) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+claimColumns+` FROM claims WHERE key = ?`, key)
	if err != nil {
		return fmt.Errorf("reading observed reservations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	count := 0
	for rows.Next() {
		var current claimEncoded
		if err := rows.Scan(&current.port, &current.key, &current.project, &current.worktree,
			&current.createdAt, &current.expiresAt); err != nil {
			return err
		}
		want, ok := expected[current.port]
		if !ok || current != want {
			return ErrClaimsChanged
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != len(expected) {
		return ErrClaimsChanged
	}
	return rows.Close()
}
