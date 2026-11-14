// Package store is the runtime service's SQLite database: renames, group pins, the
// port-event history ring, the set of known oberth.yaml roots, and the tables
// sibling packages register for themselves.
//
// The file lives beside config.yaml (~/.option-berth/option-berth.db) and is
// created on first use. modernc.org/sqlite is the driver — pure Go, so
// CGO_ENABLED=0 release builds still work.
//
// The runtime service normally owns the store. The CLI's no-runtime-service path may open it
// read-only to keep durable run-exit evidence while the runtime service is down; see
// OpenReadOnly.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/paths"
	sqlite "modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// Timestamps are stored as fixed-width UTC RFC 3339 text with nanoseconds
// always at nine digits. That shape is deliberate: string comparison is then
// chronological, so a `since` filter can be a plain SQL comparison.
const timeLayout = "2006-01-02T15:04:05.000000000Z07:00"

func timeToString(t time.Time) string { return t.UTC().Format(timeLayout) }

func nowString() string { return timeToString(time.Now()) }

func timeFromString(s string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

// ResetInfo records a database that was thrown away because the file on disk
// was not one SQLite could read. The runtime service reports it as a db_reset event, so
// a person can find the parked file.
type ResetInfo struct {
	Path    string // the database that was reset
	MovedTo string // where the unusable file went
	Reason  string // the driver error that triggered it
}

// Options tunes Open. The zero value is what Open uses.
type Options struct {
	// owned retains SQLite exclusive ownership on one non-expiring connection.
	owned bool
	// OnReset runs after a corrupt database has been parked and replaced, and
	// before Open returns. Nil is fine.
	OnReset func(ResetInfo)
	// BusyTimeout bounds how long one statement waits for a competing writer;
	// zero means five seconds.
	BusyTimeout time.Duration
}

// Store is an open option-berth database. It is safe for concurrent use: reads
// run on the connection pool, which WAL lets proceed while a write is in
// flight, and ordinary writes are serialised behind one mutex. A conditional
// write (the reservation release) refuses contention instead of queueing behind
// it; another Store handle on the same file can still cause SQLITE_BUSY.
type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
	owned    bool

	wmu sync.Mutex

	// reservationsRevision invalidates an in-flight release observation when a
	// reservation is written through this Store. Guarded by wmu; it is a
	// counter, not a second copy of the table.
	reservationsRevision uint64

	reset *ResetInfo
}

// Path is the default database location: option-berth.db in the directory
// internal/paths owns, next to config.yaml. BERTH_DB overrides it, which is what
// the tests and `oberth runtime service run` with a temp root use.
func Path() string { return paths.DB() }

// Open opens the database at path, creating it if needed and migrating it to
// the newest registered version. An empty path means Path().
//
// A file that is not a usable SQLite database is parked as
// <path>.corrupt-<timestamp> and replaced by a fresh one: Open still succeeds,
// and Reset says it happened.
func Open(path string) (*Store, error) { return OpenWith(path, Options{}) }

// OpenReadOnly opens an existing database without running migrations or
// changing journal mode. It is for local readers that need durable evidence
// while the runtime service is stopped; callers must close the returned store.
func OpenReadOnly(path string) (*Store, error) {
	path = databasePath(path)
	if err := ValidateStorageFilesystem(path); err != nil {
		return nil, err
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uri := url.URL{Scheme: "file", Path: readOnlyURIPath(abs), RawQuery: "mode=ro&_pragma=busy_timeout(50)"}
	dsn := uri.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, ownershipError(path, fmt.Errorf("opening %s read-only: %w", path, err))
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, ownershipError(path, fmt.Errorf("opening %s read-only: %w", path, err))
	}
	return &Store{db: db, path: path, readOnly: true}, nil
}

// readOnlyURIPath is the URL path for a file: URI. A Windows absolute path
// reaches here as `C:/...` after filepath.ToSlash, which is a drive letter, not
// a URL path; the leading slash keeps it one path segment list instead of a
// scheme-relative form. Unix paths already start with `/` and pass unchanged.
func readOnlyURIPath(abs string) string {
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	return uriPath
}

// OpenWith is Open with options.
//
// Corruption is the one failure it recovers from: the unusable file is parked
// beside itself and a fresh database is created in its place. Every other
// failure is returned, because a permission error or a full disk would recur on
// the second attempt and losing the file would not help.
func OpenWith(path string, opts Options) (*Store, error) {
	path = databasePath(path)
	if err := prepareFile(path); err != nil {
		return nil, err
	}
	store, err := openAt(path, opts)
	if err == nil || opts.owned || !isCorruption(err) {
		return store, err
	}
	return recreate(path, opts, err)
}

// chmodFile is the owner-only permission check's syscall seam. Tests replace it
// to exercise a refusal a normal temp file cannot produce; production uses the
// real chmod.
var chmodFile = os.Chmod

// databasePath resolves the one default every opener shares.
func databasePath(path string) string {
	if strings.TrimSpace(path) == "" {
		return Path()
	}
	return path
}

// recreate parks an unusable database and opens a fresh one in its place,
// recording what happened so the runtime service can report a db_reset event.
func recreate(path string, opts Options, cause error) (*Store, error) {
	movedTo, err := moveAside(path)
	if err != nil {
		return nil, fmt.Errorf("%s is unusable (%v) and could not be moved aside: %w", path, cause, err)
	}
	if err := prepareFile(path); err != nil {
		return nil, err
	}
	fresh, err := openAt(path, opts)
	if err != nil {
		return nil, fmt.Errorf("recreating %s after moving the unusable file to %s: %w", path, movedTo, err)
	}
	fresh.reset = &ResetInfo{Path: path, MovedTo: movedTo, Reason: cause.Error()}
	if opts.OnReset != nil {
		opts.OnReset(*fresh.reset)
	}
	return fresh, nil
}

// openAt opens and migrates, with no corruption recovery: it is openAt's caller
// that decides whether a failure is worth starting over from.
func openAt(path string, opts Options) (*Store, error) {
	dsn := dsnForOptions(path, opts.BusyTimeout, opts.owned)
	var db *sql.DB
	var err error
	if opts.owned {
		db, err = newOwnedDB(dsn)
	} else {
		db, err = sql.Open("sqlite", dsn)
	}
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// Writes are serialised in Go and readers are short, so a small pool is
	// enough; the idle timeout keeps a quiet runtime service from holding connections.
	if opts.owned {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		// Do not expire the connection: SQLite owns the lifetime lease.
		// BEGIN EXCLUSIVE forces acquisition even on an already-current schema;
		// COMMIT ends the empty transaction, retaining EXCLUSIVE-mode locks.
		if _, err := db.Exec(`BEGIN EXCLUSIVE`); err != nil {
			return nil, closeAfterFailure(db, err)
		}
		if _, err := db.Exec(`COMMIT`); err != nil {
			return nil, closeAfterFailure(db, err)
		}
		var locking, journal string
		if err := db.QueryRow(`PRAGMA locking_mode`).Scan(&locking); err != nil {
			return nil, closeAfterFailure(db, err)
		}
		if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
			return nil, closeAfterFailure(db, err)
		}
		if locking != "exclusive" || journal != "wal" {
			return nil, closeAfterFailure(db, fmt.Errorf("SQLite did not retain required exclusive WAL ownership"))
		}

	} else {
		db.SetMaxOpenConns(8)
		db.SetMaxIdleConns(4)
		db.SetConnMaxIdleTime(5 * time.Minute)
	}

	// Touch the schema first: a file that is not a database fails here, where
	// the caller can still recover, rather than on the first real query.
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		return nil, closeAfterFailure(db, fmt.Errorf("opening %s: %w", path, err))
	}
	if err := migrate(db); err != nil {
		return nil, closeAfterFailure(db, err)
	}
	// The database holds a person's service history; keep it owner-only.
	if err := chmodFile(path, 0o600); err != nil && !os.IsNotExist(err) {
		_ = db.Close()
		return nil, fmt.Errorf("securing %s: %w", path, err)
	}
	return &Store{db: db, path: path, owned: opts.owned}, nil
}

// closeAfterFailure releases a handle that never became a usable Store, keeping
// the original error as the one the caller sees.
func closeAfterFailure(db *sql.DB, err error) error {
	_ = db.Close()
	return err
}

// defaultBusyTimeout is how long one statement waits for a competing writer.
const defaultBusyTimeout = 5 * time.Second

// dsnFor is the connection string: WAL so readers proceed during a write, and
// foreign keys on so a migration's constraints are real.
func dsnFor(path string, busy time.Duration) string { return dsnForOptions(path, busy, false) }

func dsnForOptions(path string, busy time.Duration, owned bool) string {
	if busy <= 0 {
		busy = defaultBusyTimeout
	}
	pragmas := fmt.Sprintf("_pragma=busy_timeout(%d)", busy.Milliseconds())
	if owned {
		pragmas += "&_pragma=locking_mode(EXCLUSIVE)"
	}
	pragmas += "&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)"
	uri := url.URL{Scheme: "file", RawQuery: pragmas}
	if filepath.IsAbs(path) {
		uri.Path = readOnlyURIPath(path)
	} else {
		uri.Opaque = url.PathEscape(filepath.ToSlash(path))
	}
	return uri.String()
}

// prepareFile makes the parent directory (0700) and creates the database file
// (0600) when it is missing, so the file's permissions do not depend on the
// process umask. An existing file is left alone — SQLite takes it from there.
func prepareFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("creating the directory for %s: %w", path, err)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	switch {
	case errors.Is(err, os.ErrExist):
		return nil
	case err != nil:
		return fmt.Errorf("creating %s: %w", path, err)
	}
	// A zero-length file is a valid empty SQLite database.
	return file.Close()
}

// moveAside parks an unusable database — and its WAL sidecars, so the parked
// set is complete — beside itself, and returns the main file's new path.
func moveAside(path string) (string, error) {
	target, err := freeCorruptName(path)
	if err != nil {
		return "", err
	}
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	moveSidecars(path, target)
	return target, nil
}

// moveSidecars parks the WAL and shared-memory files beside the main one. A
// sidecar that does not exist is not an error; the parked set is complete when
// it is complete.
func moveSidecars(path, target string) {
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Rename(path+suffix, target+suffix)
	}
}

// freeCorruptName is a `<path>.corrupt-<stamp>` name that does not exist yet.
// The counter is only reached when two resets land in the same second.
func freeCorruptName(path string) (string, error) {
	stamp := path + ".corrupt-" + time.Now().UTC().Format("20060102-150405")
	target := stamp
	for attempt := 1; ; attempt++ {
		_, err := os.Stat(target)
		if errors.Is(err, os.ErrNotExist) {
			return target, nil
		}
		if err != nil {
			return "", err
		}
		target = fmt.Sprintf("%s-%d", stamp, attempt)
	}
}

// isCorruption reports whether err means "this file is not a database option-berth
// can use". It is the only failure Open recovers from by starting over; a
// permission error, a full disk or a locked file is the caller's to see.
//
// The driver's own code is the first answer, because it is exact. Some
// open-time failures arrive as plain strings with no code attached, so the
// message is checked too — a shorter list than it looks, since these four
// phrases are the only ways SQLite says "not a database".
func isCorruption(err error) bool {
	if err == nil {
		return false
	}
	var driverErr *sqlite.Error
	if errors.As(err, &driverErr) {
		switch driverErr.Code() & 0xff {
		case sqlite3.SQLITE_NOTADB, sqlite3.SQLITE_CORRUPT, sqlite3.SQLITE_FORMAT:
			return true
		}
	}
	message := strings.ToLower(err.Error())
	for _, phrase := range corruptionPhrases {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

// corruptionPhrases are the open-time messages SQLite prints when the file it
// was handed is not a database it can read.
var corruptionPhrases = []string{
	"file is not a database",
	"file is encrypted or is not a database",
	"database disk image is malformed",
	"malformed database schema",
}

// Close releases the database, leaving one file behind rather than three.
//
// WAL keeps a `-wal` and a `-shm` beside the database, and whichever pooled
// connection closes last writes them again — after Close has returned, as far
// as the caller can tell. A runtime service that has stopped, or a test removing its
// temp directory, cannot wait for that: the removal fails with "directory not
// empty" against a sidecar recreated behind it. Checkpointing and leaving WAL
// mode deletes both while we still hold the connection that owns them. A busy
// database refuses the switch, which is the state we were already in, so that
// error is not worth reporting.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	if s.readOnly {
		return s.db.Close()
	}
	s.checkpointAndLeaveWAL()
	return s.db.Close()
}

// checkpointTimeout bounds the wait for the connection checkpointing needs. A
// leaked or stuck connection is a bug, but it must not make Close hang forever:
// runtime service shutdown and a test's temp-directory cleanup both depend on Close
// returning.
const checkpointTimeout = 5 * time.Second

// checkpointAndLeaveWAL folds the write-ahead log into the database and takes
// the file out of WAL mode, both of which delete the two sidecars. It needs the
// pool down to one connection: journal_mode only changes when one connection is
// left to change it.
func (s *Store) checkpointAndLeaveWAL() {
	s.checkpointAndLeaveWALWithin(checkpointTimeout)
}

// checkpointAndLeaveWALWithin is checkpointAndLeaveWAL with an explicit budget.
// The budget covers acquiring the connection and both pragmas: if a caller
// holds the only connection, Close gives up instead of waiting forever.
func (s *Store) checkpointAndLeaveWALWithin(budget time.Duration) {
	if !s.owned {
		s.db.SetMaxIdleConns(0)
		s.db.SetMaxOpenConns(1)
	}
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
	_, _ = conn.ExecContext(ctx, `PRAGMA journal_mode(DELETE)`)
}

// DBPath is the file this store is backed by.
func (s *Store) DBPath() string { return s.path }

// DB exposes the raw handle. The cross-spec contract hands this to runtime service
// extensions through HostContext; nothing inside option-berth should reach for it.
func (s *Store) DB() *sql.DB { return s.db }

// Reset reports the database Open had to throw away, or nil when none was. The
// runtime service reads it right after Open and reports a db_reset event.
func (s *Store) Reset() *ResetInfo {
	if s == nil || s.reset == nil {
		return nil
	}
	cp := *s.reset
	return &cp
}

// writeTx is the statement surface a write callback gets: enough to run
// statements inside the transaction the store opened, and nothing that could
// commit or roll it back behind the store's back.
type writeTx interface {
	Exec(query string, args ...any) (sql.Result, error)
	Prepare(query string) (*sql.Stmt, error)
}

// write runs fn inside one transaction under the writer lock, so either every
// statement fn ran is visible or none is. A failure rolls back and is reported
// with the step the caller named.
func (s *Store) write(what string, fn func(writeTx) error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := fn(tx); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%s: %w", what, err)
	}
	return nil
}

// textColumn reads a one-text-column result into a slice, in the order the
// caller's query asks for.
func (s *Store) textColumn(query string, args ...any) ([]string, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var values []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, rows.Err()
}

// writeLocked runs fn with the single-writer lock held. Every write path on
// a Store handle goes through here so two methods cannot interleave Exec.
func (s *Store) writeLocked(fn func() error) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	return fn()
}

// exec runs one write statement with the writer lock held.
func (s *Store) exec(query string, args ...any) error {
	return s.writeLocked(func() error {
		_, err := s.db.Exec(query, args...)
		return err
	})
}

// execRows is exec for a write whose row count the caller needs. It takes
// its place in the same single-writer order; a driver that cannot report
// the count returns zero after a write that happened.
func (s *Store) execRows(query string, args ...any) (int, error) {
	var affected int
	err := s.writeLocked(func() error {
		res, err := s.db.Exec(query, args...)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			affected = 0
			return nil
		}
		affected = int(n)
		return nil
	})
	return affected, err
}
