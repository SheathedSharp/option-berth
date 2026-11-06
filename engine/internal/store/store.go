// Package store is the daemon's SQLite database: renames, group pins, the
// port-event history ring, the set of known oberth.yaml roots, and the tables
// sibling packages register for themselves.
//
// The file lives beside config.yaml (~/.option-berth/option-berth.db) and is
// created on first use. modernc.org/sqlite is the driver — pure Go, so
// CGO_ENABLED=0 release builds still work.
//
// The daemon normally owns the store. The CLI's no-daemon path may open it
// read-only to keep durable run-exit evidence while the daemon is down; see
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
// was not one SQLite could read. The daemon reports it as a db_reset event, so
// a person can find the parked file.
type ResetInfo struct {
	Path    string // the database that was reset
	MovedTo string // where the unusable file went
	Reason  string // the driver error that triggered it
}

// Options tunes Open. The zero value is what Open uses.
type Options struct {
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

	wmu sync.Mutex

	// reservationsRevision invalidates an in-flight release observation when a
	// reservation is written through this Store. Guarded by wmu; it is a
	// counter, not a second copy of the table.
	reservationsRevision uint64

	reset *ResetInfo
}

// Path is the default database location: option-berth.db in the directory
// internal/paths owns, next to config.yaml. BERTH_DB overrides it, which is what
// the tests and `oberth serve` with a temp root use.
func Path() string { return paths.DB() }

// Open opens the database at path, creating it if needed and migrating it to
// the newest registered version. An empty path means Path().
//
// A file that is not a usable SQLite database is parked as
// <path>.corrupt-<timestamp> and replaced by a fresh one: Open still succeeds,
// and ResetHappened says it happened.
func Open(path string) (*Store, error) { return OpenWith(path, Options{}) }

// OpenReadOnly opens an existing database without running migrations or
// changing journal mode. It is for local readers that need durable evidence
// while the daemon is stopped; callers must close the returned store.
func OpenReadOnly(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = Path()
	}
	if _, err := os.Stat(path); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	uriPath := filepath.ToSlash(abs)
	if !strings.HasPrefix(uriPath, "/") {
		uriPath = "/" + uriPath
	}
	uri := url.URL{Scheme: "file", Path: uriPath, RawQuery: "mode=ro&_pragma=busy_timeout(1000)"}
	dsn := uri.String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("opening %s read-only: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&n); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening %s read-only: %w", path, err)
	}
	return &Store{db: db, path: path, readOnly: true}, nil
}

// OpenWith is Open with options.
//
// Corruption is the one failure it recovers from: the unusable file is parked
// beside itself and a fresh database is created in its place. Every other
// failure is returned, because a permission error or a full disk would recur on
// the second attempt and losing the file would not help.
func OpenWith(path string, opts Options) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		path = Path()
	}
	if err := prepareFile(path); err != nil {
		return nil, err
	}
	store, err := openAt(path, opts)
	if err == nil || !isCorruption(err) {
		return store, err
	}
	return recreate(path, opts, err)
}

// recreate parks an unusable database and opens a fresh one in its place,
// recording what happened so the daemon can report a db_reset event.
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
	db, err := sql.Open("sqlite", dsnFor(path, opts.BusyTimeout))
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	// Writes are serialised in Go and readers are short, so a small pool is
	// enough; the idle timeout keeps a quiet daemon from holding connections.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)

	// Touch the schema first: a file that is not a database fails here, where
	// the caller can still recover, rather than on the first real query.
	var tables int
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master`).Scan(&tables); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	// The database holds a person's service history; keep it owner-only.
	if err := os.Chmod(path, 0o600); err != nil && !os.IsNotExist(err) {
		_ = db.Close()
		return nil, fmt.Errorf("securing %s: %w", path, err)
	}
	return &Store{db: db, path: path}, nil
}

// defaultBusyTimeout is how long one statement waits for a competing writer.
const defaultBusyTimeout = 5 * time.Second

// dsnFor is the connection string: WAL so readers proceed during a write, and
// foreign keys on so a migration's constraints are real.
func dsnFor(path string, busy time.Duration) string {
	if busy <= 0 {
		busy = defaultBusyTimeout
	}
	return fmt.Sprintf(
		"file:%s?_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(1)",
		path, busy.Milliseconds(),
	)
}

// prepareFile makes sure the parent directory exists (0700) and that the
// database file, when it has to be created, is created 0600 rather than with
// whatever the process umask would give it.
func prepareFile(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil // already there; SQLite takes it from here
		}
		return fmt.Errorf("creating %s: %w", path, err)
	}
	// A zero-length file is a valid empty SQLite database.
	return f.Close()
}

// moveAside parks an unusable database (and its WAL sidecars) next to itself
// and returns the new path of the main file.
func moveAside(path string) (string, error) {
	stamp := time.Now().UTC().Format("20060102-150405")
	target := fmt.Sprintf("%s.corrupt-%s", path, stamp)
	for i := 1; ; i++ {
		if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
			break
		}
		target = fmt.Sprintf("%s.corrupt-%s-%d", path, stamp, i)
	}
	if err := os.Rename(path, target); err != nil {
		return "", err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		_ = os.Rename(path+suffix, target+suffix)
	}
	return target, nil
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
// as the caller can tell. A daemon that has stopped, or a test removing its
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

// checkpointAndLeaveWAL folds the write-ahead log into the database and takes
// the file out of WAL mode, both of which delete the two sidecars. It needs the
// pool down to one connection: journal_mode only changes when one connection is
// left to change it.
func (s *Store) checkpointAndLeaveWAL() {
	s.db.SetMaxIdleConns(0)
	s.db.SetMaxOpenConns(1)
	conn, err := s.db.Conn(context.Background())
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	_, _ = conn.ExecContext(context.Background(), `PRAGMA wal_checkpoint(TRUNCATE)`)
	_, _ = conn.ExecContext(context.Background(), `PRAGMA journal_mode(DELETE)`)
}

// DBPath is the file this store is backed by.
func (s *Store) DBPath() string { return s.path }

// DB exposes the raw handle. The cross-spec contract hands this to daemon
// extensions through Runtime; nothing inside option-berth should reach for it.
func (s *Store) DB() *sql.DB { return s.db }

// ResetHappened reports whether Open had to throw the previous database away.
// The daemon calls it right after Open and emits state.event{kind:"db_reset"}.
func (s *Store) ResetHappened() bool { return s != nil && s.reset != nil }

// Reset returns the details of that reset, or nil if none happened.
func (s *Store) Reset() *ResetInfo {
	if s == nil || s.reset == nil {
		return nil
	}
	cp := *s.reset
	return &cp
}

// exec runs one write statement with the writer lock held.
func (s *Store) exec(query string, args ...any) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	_, err := s.db.Exec(query, args...)
	return err
}

// execRows is exec for a write whose row count the caller needs. It holds the
// same writer lock, so it takes its place in the single-writer order; a driver
// that cannot report the count returns zero after a write that happened.
func (s *Store) execRows(query string, args ...any) (int, error) {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	res, err := s.db.Exec(query, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, nil
	}
	return int(n), nil
}
