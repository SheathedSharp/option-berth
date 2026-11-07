package store

import (
	"database/sql"
	"embed"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration is one forward-only schema step. It runs inside a transaction
// together with the schema_version row that records it, so a failed step leaves
// the database on the version it had.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// The versions this package ships, plus the ones held for packages that own
// their own table. A reserved number is filled in by the package that owns it,
// from its own init — which is why migration state is a set of applied versions
// rather than one high-water mark. See the note on migrate.
const (
	VersionCore     = 1 // renames, group_pins, port_events + ring trigger
	VersionIndexes  = 2 // history indexes, known_roots
	VersionShares   = 3 // reserved: shares
	VersionProxies  = 4 // reserved: proxies
	VersionSessions = 5 // reserved: sessions
	VersionClaims   = 6 // reserved: claims

	VersionGroupAliases = 7 // group_aliases: project names from groups.rename
)

// ReservedVersions names the feature that owns each held version, so a
// duplicate registration can say whose number was taken.
var ReservedVersions = map[int]string{
	// Sibling packages register these from their own init; the label is what a
	// duplicate-registration panic reports.
	VersionShares:   "shares",
	VersionProxies:  "proxies",
	VersionSessions: "sessions",
	VersionClaims:   "claims",
}

// migrationSet is the registry. It keeps its versions sorted and its newest
// version at hand, so registering costs one insertion and reading the newest
// version costs nothing.
type migrationSet struct {
	mu        sync.Mutex
	byVersion map[int]Migration
	ordered   []Migration
}

var registry = &migrationSet{byVersion: map[int]Migration{}}

// RegisterMigration adds a migration to the global set. It is meant to be
// called from a package init() — including from a package outside this one,
// which is how a reserved version gets filled in — so it panics instead of
// returning an error: a duplicate version or an empty statement is a
// programming mistake that must never reach a user's database.
func RegisterMigration(version int, name string, stmts string) {
	switch {
	case version <= 0:
		panic(fmt.Sprintf("store: migration version must be positive, got %d", version))
	case strings.TrimSpace(name) == "":
		panic(fmt.Sprintf("store: migration %03d needs a name", version))
	case strings.TrimSpace(stmts) == "":
		panic(fmt.Sprintf("store: migration %03d (%s) is empty", version, name))
	}
	registry.add(Migration{Version: version, Name: name, SQL: stmts})
}

// add inserts one migration, keeping the registry ordered by version.
func (s *migrationSet) add(m Migration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if prev, taken := s.byVersion[m.Version]; taken {
		panic(fmt.Sprintf("store: migration %03d already registered as %q, refusing %q",
			m.Version, prev.Name, m.Name))
	}
	s.byVersion[m.Version] = m
	at := sort.Search(len(s.ordered), func(i int) bool { return s.ordered[i].Version >= m.Version })
	s.ordered = append(s.ordered, Migration{})
	copy(s.ordered[at+1:], s.ordered[at:])
	s.ordered[at] = m
}

// all returns every registered migration in version order.
func (s *migrationSet) all() []Migration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Migration, len(s.ordered))
	copy(out, s.ordered)
	return out
}

// newest is the highest registered version, 0 when nothing is registered.
func (s *migrationSet) newest() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.ordered) == 0 {
		return 0
	}
	return s.ordered[len(s.ordered)-1].Version
}

// LatestVersion is the highest registered migration version.
func LatestVersion() int { return registry.newest() }

func init() {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		panic("store: embedded migrations unreadable: " + err.Error())
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			panic("store: " + err.Error())
		}
		body, err := migrationFS.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			panic("store: reading " + entry.Name() + ": " + err.Error())
		}
		RegisterMigration(version, name, string(body))
	}
}

// parseMigrationName splits "002_indexes_roots.sql" into 2 and "indexes_roots".
func parseMigrationName(file string) (int, string, error) {
	base := strings.TrimSuffix(file, ".sql")
	number, name, ok := strings.Cut(base, "_")
	if !ok {
		return 0, "", fmt.Errorf("migration %q must be named <version>_<name>.sql", file)
	}
	version, err := strconv.Atoi(number)
	if err != nil {
		return 0, "", fmt.Errorf("migration %q has a non-numeric version: %w", file, err)
	}
	return version, name, nil
}

// SchemaTooNewError reports a database whose recorded schema is past what this
// build knows: an older binary opening a file a newer one already migrated. The
// file is left exactly as it was found — the older binary cannot know what the
// newer migration changed, and guessing is worse than saying so.
type SchemaTooNewError struct {
	// Database is the newest version recorded in the file.
	Database int
	// Supported is the newest version this build registers.
	Supported int
}

func (e *SchemaTooNewError) Error() string {
	return fmt.Sprintf("database schema is version %d, but this build supports up to %d; upgrade option-berth",
		e.Database, e.Supported)
}

const schemaVersionDDL = `
CREATE TABLE IF NOT EXISTS schema_version(
	version INTEGER PRIMARY KEY,
	name TEXT NOT NULL,
	applied_at TEXT NOT NULL
)`

// migrate brings db to the newest registered version.
//
// The applied set is read version by version, not as MAX(version), and each
// registered version missing from it is applied in ascending order. That
// difference is load-bearing: reserved versions are registered by the packages
// that own them, so a build that links claims but not sessions reaches 6
// without ever recording 5 — and a build that later links sessions must still
// apply 5. Comparing against the highest applied version would have skipped it
// for good.
func migrate(db *sql.DB) error {
	if err := ensureSchemaTable(db); err != nil {
		return err
	}
	applied, err := appliedVersions(db)
	if err != nil {
		return err
	}
	if err := refuseNewerSchema(applied); err != nil {
		return err
	}
	for _, m := range registry.all() {
		if applied[m.Version] {
			continue
		}
		if err := applyMigration(db, m); err != nil {
			return err
		}
	}
	return nil
}

// ensureSchemaTable creates the ledger if this is a fresh file. It runs before
// any version is read, so an empty database and an old one follow one path.
func ensureSchemaTable(db *sql.DB) error {
	if _, err := db.Exec(schemaVersionDDL); err != nil {
		return fmt.Errorf("creating schema_version: %w", err)
	}
	return nil
}

// refuseNewerSchema enforces the forward-only boundary on the whole applied
// set, not on MAX alone: a reserved version recorded by a sibling build still
// only counts when this build knows it.
func refuseNewerSchema(applied map[int]bool) error {
	recorded, supported := 0, LatestVersion()
	for version := range applied {
		if version > recorded {
			recorded = version
		}
	}
	if recorded > supported {
		return &SchemaTooNewError{Database: recorded, Supported: supported}
	}
	return nil
}

// appliedVersions is the set of versions this database has already run. A
// version in it is never applied again; one that is registered but missing is
// applied on the next open, wherever it sits in the order.
func appliedVersions(db *sql.DB) (map[int]bool, error) {
	rows, err := db.Query(`SELECT version FROM schema_version`)
	if err != nil {
		return nil, fmt.Errorf("reading schema_version: %w", err)
	}
	defer func() { _ = rows.Close() }()
	return scanVersions(rows)
}

// scanVersions turns the ledger rows into a set. The caller owns the rows and
// closes them; this function only decodes.
func scanVersions(rows *sql.Rows) (map[int]bool, error) {
	applied := map[int]bool{}
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("reading schema_version: %w", err)
		}
		applied[version] = true
	}
	return applied, rows.Err()
}

// applyMigration runs one migration and records it in the same transaction:
// either the schema step and its version row both exist, or neither does.
func applyMigration(db *sql.DB, m Migration) error {
	tx, err := db.Begin()
	if err != nil {
		return migrationError(m, "beginning", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.Exec(m.SQL); err != nil {
		return migrationError(m, "applying", err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_version(version, name, applied_at) VALUES(?, ?, ?)`,
		m.Version, m.Name, nowString(),
	); err != nil {
		return migrationError(m, "recording version", err)
	}
	if err := tx.Commit(); err != nil {
		return migrationError(m, "committing", err)
	}
	return nil
}

// migrationError names the step that failed, so a report says which migration
// and what it was doing.
func migrationError(m Migration, step string, err error) error {
	return fmt.Errorf("migration %03d (%s) %s: %w", m.Version, m.Name, step, err)
}

// schemaVersion is the newest applied version, 0 for a fresh database. It is
// what Version reports; migrate works from the whole applied set, not this
// number.
func schemaVersion(db *sql.DB) (int, error) {
	var newest sql.NullInt64
	if err := db.QueryRow(`SELECT MAX(version) FROM schema_version`).Scan(&newest); err != nil {
		return 0, fmt.Errorf("reading schema_version: %w", err)
	}
	if !newest.Valid {
		return 0, nil
	}
	return int(newest.Int64), nil
}

// Version reports the schema version of the open database.
func (s *Store) Version() (int, error) { return schemaVersion(s.db) }
