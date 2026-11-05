package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Migration 006 creates the claims table. A claim is book-keeping rather than
// a bind: the port is only spoken for, so `ports.next` and later claims step
// over it. Each row is one claimed port; the key groups the ports one
// (project, worktree) pair holds.

// ErrClaimed reports that another key already holds the port. The daemon maps
// it to the contract's claim_conflict (1201).
var ErrClaimed = errors.New("port is claimed by another key")

// claimColumns lists the table's columns in scan order. Every statement here
// uses it so a write and a read cannot disagree about layout.
const claimColumns = `port, key, project, worktree, expires_at, created_at`

// ClaimRow is one claimed port.
type ClaimRow struct {
	// Port is the reservation itself.
	Port int

	// Key names the holder; project and worktree are its decoded halves.
	Key      string
	Project  string
	Worktree string

	// The reservation's lifetime.
	ExpiresAt time.Time
	CreatedAt time.Time
}

// Claims is the claims table. Obtain one from Store.Claims; the zero value is
// not usable.
type Claims struct{ s *Store }

// Claims opens the claims table.
func (s *Store) Claims() Claims { return Claims{s: s} }

// encodedClaim is one row in storage form. Encoding before the transaction
// starts is what lets an invalid row be refused with nothing written.
type encodedClaim struct {
	port                   int
	key, project, worktree string
	expiresAt, createdAt   string
}

func encodeClaim(row ClaimRow) (encodedClaim, error) {
	if row.Port < 1 || row.Port > 65535 {
		return encodedClaim{}, fmt.Errorf("store: claim port %d is out of range", row.Port)
	}
	if strings.TrimSpace(row.Key) == "" {
		return encodedClaim{}, errors.New("store: a claim needs a key")
	}
	return encodedClaim{
		port:      row.Port,
		key:       row.Key,
		project:   row.Project,
		worktree:  row.Worktree,
		expiresAt: timeToString(nowIfZero(row.ExpiresAt)),
		createdAt: timeToString(nowIfZero(row.CreatedAt)),
	}, nil
}

func (e encodedClaim) values() []any {
	return []any{e.port, e.key, e.project, e.worktree, e.expiresAt, e.createdAt}
}

// decodeClaim reads one row in claimColumns order.
func decodeClaim(scan func(...any) error) (ClaimRow, error) {
	var (
		row              ClaimRow
		expires, created string
	)
	if err := scan(&row.Port, &row.Key, &row.Project, &row.Worktree, &expires, &created); err != nil {
		return ClaimRow{}, err
	}
	parsedExpires, err := timeFromString(expires)
	if err != nil {
		return ClaimRow{}, fmt.Errorf("claim on port %d has an unreadable expires_at: %w", row.Port, err)
	}
	parsedCreated, err := timeFromString(created)
	if err != nil {
		return ClaimRow{}, fmt.Errorf("claim on port %d has an unreadable created_at: %w", row.Port, err)
	}
	row.ExpiresAt, row.CreatedAt = parsedExpires, parsedCreated
	return row, nil
}

// Put writes a set of claims. A port this key already holds keeps its stored
// created_at, so refreshing does not restart the reservation's clock. The
// whole set lands in one transaction: every port is claimed or none is, which
// is what makes two acquires racing for a block fail cleanly rather than
// interleave.
//
// A port held by another unexpired key is never taken: Put returns ErrClaimed
// and rolls back. Call Expire first to sweep stale rows.
func (c Claims) Put(rows ...ClaimRow) error {
	if len(rows) == 0 {
		return nil
	}
	batch := make([]encodedClaim, len(rows))
	for i, row := range rows {
		encoded, err := encodeClaim(row)
		if err != nil {
			return err
		}
		batch[i] = encoded
	}

	tx, release, err := c.writeTx()
	if err != nil {
		return err
	}
	defer release()

	for i := range batch {
		if err := writeClaim(tx, &batch[i]); err != nil {
			return err
		}
	}
	// Invalidate observations before committing, including a commit whose
	// outcome is ambiguous: even a byte-identical refresh must stop an older
	// release from deleting the row.
	c.s.claimsRevision++
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("claiming ports: %w", err)
	}
	return nil
}

// writeTx opens the claims write transaction under the store's write lock.
// release rolls back (a committed transaction ignores it) and unlocks, in that
// order, which is the order the deferred calls had before.
func (c Claims) writeTx() (*sql.Tx, func(), error) {
	c.s.wmu.Lock()
	tx, err := c.s.db.Begin()
	if err != nil {
		c.s.wmu.Unlock()
		return nil, nil, fmt.Errorf("claiming ports: %w", err)
	}
	return tx, func() {
		_ = tx.Rollback()
		c.s.wmu.Unlock()
	}, nil
}

// writeClaim checks the current holder and writes one encoded row.
func writeClaim(tx *sql.Tx, claim *encodedClaim) error {
	var holder, createdAt string
	err := tx.QueryRow(
		`SELECT key, created_at FROM claims WHERE port = ?`, claim.port).Scan(&holder, &createdAt)
	switch {
	case err == nil && holder != claim.key:
		return fmt.Errorf("port %d: %w (%s)", claim.port, ErrClaimed, holder)
	case err == nil:
		claim.createdAt = createdAt
	}
	if _, err := tx.Exec(
		`INSERT INTO claims(`+claimColumns+`)
		 VALUES(?, ?, ?, ?, ?, ?)
		 ON CONFLICT(port) DO UPDATE SET
		     key = excluded.key,
		     project = excluded.project,
		     worktree = excluded.worktree,
		     expires_at = excluded.expires_at`,
		claim.values()...,
	); err != nil {
		return fmt.Errorf("claiming port %d: %w", claim.port, err)
	}
	return nil
}

// Get returns the ports held under key, lowest first.
func (c Claims) Get(key string) ([]ClaimRow, error) {
	return c.query(`SELECT `+claimColumns+` FROM claims WHERE key = ? ORDER BY port`, key)
}

// List returns every claim row, ordered by key then port.
func (c Claims) List() ([]ClaimRow, error) {
	return c.query(`SELECT ` + claimColumns + ` FROM claims ORDER BY key, port`)
}

// Delete drops every port held under key and reports how many rows went.
// Releasing a key nobody holds succeeds with zero.
func (c Claims) Delete(key string) (int, error) {
	return c.remove(`DELETE FROM claims WHERE key = ?`, key)
}

// Expire drops every claim that expired at or before now and reports how many
// rows went. Each claims call sweeps first, which is the lazy pruning the
// contract asks for.
func (c Claims) Expire(now time.Time) (int, error) {
	return c.remove(`DELETE FROM claims WHERE expires_at <= ?`, timeToString(nowIfZero(now)))
}

func (c Claims) remove(query string, args ...any) (int, error) {
	c.s.wmu.Lock()
	defer c.s.wmu.Unlock()
	result, err := c.s.db.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("removing claims: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		c.s.claimsRevision++
		return 0, err
	}
	if affected > 0 {
		c.s.claimsRevision++
	}
	return int(affected), nil
}

func (c Claims) query(query string, args ...any) ([]ClaimRow, error) {
	cursor, err := c.s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("reading claims: %w", err)
	}
	defer func() { _ = cursor.Close() }()
	var out []ClaimRow
	for cursor.Next() {
		row, err := decodeClaim(cursor.Scan)
		if err != nil {
			return nil, fmt.Errorf("reading claims: %w", err)
		}
		out = append(out, row)
	}
	return out, cursor.Err()
}

// nowIfZero turns an omitted timestamp into now.
func nowIfZero(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now()
	}
	return t
}
