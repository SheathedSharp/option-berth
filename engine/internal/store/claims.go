package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// The claims table holds one row per reserved port. A reservation is
// book-keeping, not a bind: nothing here touches a socket, it only records who
// may use which port, so the runtime service's allocator and `ports.next` step around it
// and a service that is started again finds its address unchanged.

// ErrClaimed reports that a port is already held by a different key. The
// runtime service maps it to the contract's claim_conflict (1201).
var ErrClaimed = errors.New("port is claimed by another key")

// claimColumns is the column list in scan order. Every statement here reads it
// from this one constant, so a write and a read cannot drift apart.
const claimColumns = `port, key, project, worktree, created_at, expires_at`

// ClaimRow is one reserved port and the checkout it belongs to. Since is when
// the service first took the port and does not move on a renewal; Until is when
// the reservation lapses unless the service is started again.
type ClaimRow struct {
	Port     int
	Key      string
	Project  string
	Worktree string
	Since    time.Time
	Until    time.Time
}

// Claims reads and writes the reservations of one store. Build it with
// Store.Claims rather than as a literal, so it always carries a live store.
type Claims struct {
	store *Store
}

// Claims is the reservations table of this store.
func (s *Store) Claims() Claims { return Claims{store: s} }

// claimWrite is one row with every column already in its stored form.
// Encoding before the transaction opens is what lets a bad row be refused with
// nothing written.
type claimWrite struct {
	port                   int
	key, project, worktree string
	since, expiresAt       string
}

// encodeClaim refuses a row a reader could not interpret. A missing timestamp
// is refused rather than filled in with the current time: the reservation's
// start and end are facts its owner states, and inventing one would make this
// row say something no caller asked for.
func encodeClaim(r ClaimRow) (claimWrite, error) {
	if r.Port < 1 || r.Port > 65535 {
		return claimWrite{}, fmt.Errorf("store: claim port %d is out of range", r.Port)
	}
	if strings.TrimSpace(r.Key) == "" {
		return claimWrite{}, errors.New("store: a claim needs a key")
	}
	if r.Since.IsZero() || r.Until.IsZero() {
		return claimWrite{}, errors.New("store: a claim needs both its start and its expiry")
	}
	return claimWrite{
		port:      r.Port,
		key:       r.Key,
		project:   r.Project,
		worktree:  r.Worktree,
		since:     timeToString(r.Since),
		expiresAt: timeToString(r.Until),
	}, nil
}

// encodeClaims prepares every row before the transaction opens, so a single bad
// row refuses the whole set with nothing written.
func encodeClaims(rows []ClaimRow) ([]claimWrite, error) {
	writes := make([]claimWrite, len(rows))
	for i, r := range rows {
		w, err := encodeClaim(r)
		if err != nil {
			return nil, err
		}
		writes[i] = w
	}
	return writes, nil
}

func (w claimWrite) args() []any {
	return []any{w.port, w.key, w.project, w.worktree, w.since, w.expiresAt}
}

// decodeClaim reads one row in claimColumns order, refusing a timestamp it
// cannot parse rather than inventing one.
func decodeClaim(scan func(dest ...any) error) (ClaimRow, error) {
	var (
		r                ClaimRow
		created, expires string
	)
	if err := scan(&r.Port, &r.Key, &r.Project, &r.Worktree, &created, &expires); err != nil {
		return ClaimRow{}, err
	}
	for _, field := range []struct {
		column string
		raw    string
		dest   *time.Time
	}{
		{"created_at", created, &r.Since},
		{"expires_at", expires, &r.Until},
	} {
		at, err := timeFromString(field.raw)
		if err != nil {
			return ClaimRow{}, fmt.Errorf("claim on port %d has an unreadable %s: %w", r.Port, field.column, err)
		}
		*field.dest = at
	}
	return r, nil
}

// Put writes rows in one transaction, so either every port is reserved or none
// is: two acquirers racing for the same block fail cleanly instead of
// interleaving. Re-writing a port this same key already holds keeps the stored
// since and moves only the expiry, so a renewal does not restart the clock.
//
// A port held by a different key is never taken: Put returns ErrClaimed and
// rolls back. Sweep expired rows first (see Expire), or a stale row blocks a
// live caller.
func (c Claims) Put(rows ...ClaimRow) error {
	if len(rows) == 0 {
		return nil
	}
	writes, err := encodeClaims(rows)
	if err != nil {
		return err
	}
	return c.write("claiming ports", func(tx *sql.Tx) error {
		for i := range writes {
			if err := putClaim(tx, &writes[i]); err != nil {
				return err
			}
		}
		return nil
	})
}

// putClaim stores one row inside the caller's transaction, renewing the row
// this same key already holds instead of moving its start.
func putClaim(tx *sql.Tx, w *claimWrite) error {
	holder, since, held, err := storedClaim(tx, w.port)
	if err != nil {
		return err
	}
	if held && holder != w.key {
		return fmt.Errorf("port %d: %w (%s)", w.port, ErrClaimed, holder)
	}
	if held {
		// Renewal: keep when this key first took the port, move only the expiry.
		w.since = since
		if _, err := tx.Exec(
			`UPDATE claims SET project = ?, worktree = ?, expires_at = ? WHERE port = ?`,
			w.project, w.worktree, w.expiresAt, w.port,
		); err != nil {
			return fmt.Errorf("renewing port %d: %w", w.port, err)
		}
		return nil
	}
	if _, err := tx.Exec(
		`INSERT INTO claims(`+claimColumns+`) VALUES(?, ?, ?, ?, ?, ?)`,
		w.args()...,
	); err != nil {
		return fmt.Errorf("claiming port %d: %w", w.port, err)
	}
	return nil
}

// storedClaim reports who currently holds a port, and when they took it.
func storedClaim(tx *sql.Tx, port int) (key, since string, held bool, err error) {
	err = tx.QueryRow(`SELECT key, created_at FROM claims WHERE port = ?`, port).
		Scan(&key, &since)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", "", false, nil
	case err != nil:
		return "", "", false, fmt.Errorf("reading the holder of port %d: %w", port, err)
	}
	return key, since, true, nil
}

// write runs fn in a write transaction under the store's writer lock, bumping
// the reservations revision before the commit. The revision moves before the
// commit is attempted — including an ambiguous failure — so even a
// byte-identical refresh prevents an older release from deleting the row.
func (c Claims) write(what string, fn func(*sql.Tx) error) error {
	c.store.wmu.Lock()
	defer c.store.wmu.Unlock()

	tx, err := c.store.db.Begin()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(tx); err != nil {
		return err
	}
	c.store.reservationsRevision++
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// Get returns the ports held under key, lowest first.
func (c Claims) Get(key string) ([]ClaimRow, error) {
	return c.selectClaims(`SELECT `+claimColumns+` FROM claims WHERE key = ? ORDER BY port`, key)
}

// List returns every reservation, by key then port.
func (c Claims) List() ([]ClaimRow, error) {
	return c.selectClaims(`SELECT ` + claimColumns + ` FROM claims ORDER BY key, port`)
}

// Delete drops every port held under key and reports how many rows went.
// Releasing a key nobody holds is not an error.
func (c Claims) Delete(key string) (int, error) {
	return c.deleteWhere(`DELETE FROM claims WHERE key = ?`, key)
}

// Expire drops every reservation that lapsed at or before now and reports how
// many rows went. Every claims call sweeps first: lazy pruning, not a timer.
func (c Claims) Expire(now time.Time) (int, error) {
	if now.IsZero() {
		return 0, errors.New("store: expiring reservations needs a time")
	}
	return c.deleteWhere(`DELETE FROM claims WHERE expires_at <= ?`, timeToString(now))
}

func (c Claims) deleteWhere(query string, args ...any) (int, error) {
	c.store.wmu.Lock()
	defer c.store.wmu.Unlock()
	result, err := c.store.db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("removing claims: %w", err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		c.store.reservationsRevision++
		return 0, err
	}
	if n > 0 {
		c.store.reservationsRevision++
	}
	return int(n), nil
}

// selectClaims runs a select over the reservation columns and decodes every row
// it returns. A row that cannot be read fails the whole read: a partially
// decoded reservation set is not evidence a caller may act on.
func (c Claims) selectClaims(query string, args ...any) ([]ClaimRow, error) {
	rows, err := c.store.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading claims: %w", err)
	}
	reservations, err := scanClaims(rows)
	if err != nil {
		return nil, fmt.Errorf("reading claims: %w", err)
	}
	return reservations, nil
}

// scanClaims decodes a whole result set over the reservation columns and
// closes it, so no caller has to remember the close.
func scanClaims(rows *sql.Rows) ([]ClaimRow, error) {
	defer func() { _ = rows.Close() }()
	var reservations []ClaimRow
	for rows.Next() {
		r, err := decodeClaim(rows.Scan)
		if err != nil {
			return nil, err
		}
		reservations = append(reservations, r)
	}
	return reservations, rows.Err()
}
