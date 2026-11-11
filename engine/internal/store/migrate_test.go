package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// schemaObjectExists reports whether a table, trigger or index of this name is
// in the database. A migration is proven by what it left behind, not by its
// version row.
func schemaObjectExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var found int
	err := db.QueryRow(
		`SELECT count(*) FROM sqlite_master WHERE type IN ('table','trigger','index') AND name = ?`,
		name,
	).Scan(&found)
	if err != nil {
		t.Fatalf("reading sqlite_master for %q: %v", name, err)
	}
	return found > 0
}

// openRawBase opens path as a plain connection, for building a fixture the
// migration engine should then upgrade.
func openRawBase(t *testing.T, path string) *sql.DB {
	t.Helper()
	raw, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening %s raw: %v", path, err)
	}
	return raw
}

// recordVersion writes a schema_version row as if another build had.
func recordVersion(t *testing.T, db *sql.DB, version int, name string) {
	t.Helper()
	if _, err := db.Exec(
		`INSERT INTO schema_version(version, name, applied_at) VALUES(?, ?, ?)`,
		version, name, nowString()); err != nil {
		t.Fatalf("recording version %d: %v", version, err)
	}
}

func TestAFreshDatabaseGetsEveryMigration(t *testing.T) {
	s := openTemp(t)

	version, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != LatestVersion() {
		t.Errorf("version = %d, want LatestVersion %d", version, LatestVersion())
	}
	if version < VersionIndexes {
		t.Errorf("version = %d, want at least %d", version, VersionIndexes)
	}

	requireSchemaObjects(t, s.DB(),
		"schema_version", "renames", "group_pins", "port_events",
		"port_events_ring", "known_roots",
		"idx_port_events_at", "idx_port_events_port_at",
	)
}

func requireSchemaObjects(t *testing.T, db *sql.DB, names ...string) {
	t.Helper()
	var missing []string
	for _, name := range names {
		if !schemaObjectExists(t, db, name) {
			missing = append(missing, name)
		}
	}
	if len(missing) != 0 {
		t.Errorf("missing after migrating an empty database: %v", missing)
	}
}

// A database written by a build that only had migration 001 is upgraded in
// place, and its rows of the tables the current code no longer writes survive:
// an upgrade is not a rewrite of what a person already recorded.
func TestAV1DatabaseUpgradesWithoutLosingItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	fixture, err := os.ReadFile(filepath.Join("testdata", "v1_schema.sql"))
	if err != nil {
		t.Fatalf("reading the v1 fixture: %v", err)
	}
	raw := openRawBase(t, path)
	applySQL(t, raw, string(fixture), "the v1 fixture")
	closeDB(t, raw, "the v1 database")

	s := mustOpenPath(t, path, "a v1 database")

	if s.Reset() != nil {
		t.Fatal("a valid v1 database was treated as corrupt")
	}
	if version, err := s.Version(); err != nil || version != LatestVersion() {
		t.Fatalf("version after upgrade = %d (%v), want %d", version, err, LatestVersion())
	}
	if !schemaObjectExists(t, s.DB(), "known_roots") {
		t.Error("known_roots missing after the v1 -> v2 upgrade")
	}

	var rename string
	if err := s.DB().QueryRow(
		`SELECT name FROM renames WHERE key = ?`, "cwd:/home/me/code/shop:3000").Scan(&rename); err != nil || rename != "storefront" {
		t.Errorf("v1 rename after upgrade = %q (%v), want storefront", rename, err)
	}
	var pin string
	if err := s.DB().QueryRow(
		`SELECT group_name FROM group_pins WHERE key = ?`, "port:9999").Scan(&pin); err != nil || pin != "legacy" {
		t.Errorf("v1 pin after upgrade = %q (%v), want legacy", pin, err)
	}
	events, err := s.History(HistoryFilter{Limit: 10})
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(events) != 1 || events[0].DisplayName != "storefront" {
		t.Errorf("v1 history after upgrade = %+v, want the seeded storefront event", events)
	}

	// 001 is not replayed: one row per applied version.
	var rowsForV1 int
	if err := s.DB().QueryRow(`SELECT count(*) FROM schema_version WHERE version = 1`).Scan(&rowsForV1); err != nil {
		t.Fatalf("counting schema_version rows for 001: %v", err)
	}
	if rowsForV1 != 1 {
		t.Errorf("schema_version has %d rows for version 1, want 1", rowsForV1)
	}
}

// A reserved version registered by a package this build does not link leaves a
// gap: a database can reach 6 without 5. Reopening with 5 available must fill it
// in, which is why migrate compares against the applied set and not MAX.
func TestAMigrationBelowTheHighestAppliedVersionIsStillApplied(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	recordVersion(t, s.DB(), VersionClaims, "later")
	removeAppliedVersion(t, s, VersionIndexes)
	closeStore(t, s)

	reopened := mustOpenPath(t, path, "a database with a gap below MAX")

	applied, err := appliedVersions(reopened.DB())
	if err != nil {
		t.Fatalf("appliedVersions: %v", err)
	}
	if !applied[VersionIndexes] {
		t.Errorf("migration %03d was not applied below a higher version: %v", VersionIndexes, applied)
	}
	if !applied[VersionClaims] {
		t.Errorf("the pre-existing version row was lost: %v", applied)
	}
	if !schemaObjectExists(t, reopened.DB(), "known_roots") {
		t.Error("migration 002 did not run")
	}
}

// The forward-only boundary, made real: a file migrated by a newer build is
// refused with both versions named, and left exactly as it was found — not
// moved aside, not rewritten.
func TestADatabaseFromANewerBuildIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	future := LatestVersion() + 1
	recordVersion(t, s.DB(), future, "future")
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	_, err = Open(path)
	if err == nil {
		t.Fatal("Open accepted a database from a newer build")
	}
	var tooNew *SchemaTooNewError
	if !errors.As(err, &tooNew) || tooNew.Database != future || tooNew.Supported != LatestVersion() {
		t.Fatalf("Open error = %v, want SchemaTooNewError{database: %d, supported: %d}", err, future, LatestVersion())
	}

	if sidecars, _ := filepath.Glob(path + ".corrupt-*"); len(sidecars) != 0 {
		t.Fatalf("a newer database was parked as corrupt: %v", sidecars)
	}
	raw := openRawBase(t, path)
	defer func() { _ = raw.Close() }()
	var rows int
	if err := raw.QueryRow(`SELECT count(*) FROM schema_version WHERE version = ?`, future).Scan(&rows); err != nil {
		t.Fatalf("reading the future row: %v", err)
	}
	if rows != 1 {
		t.Fatalf("schema_version rows for the future version = %d, want the file untouched", rows)
	}
}

// Registration is a programming-time act, so its mistakes panic rather than
// return: none of them may ever reach a running user's database.
func TestRegisterMigrationPanicsOnMistakes(t *testing.T) {
	cases := []struct {
		what    string
		version int
		name    string
		stmts   string
	}{
		{"zero version", 0, "x", "CREATE TABLE x(y);"},
		{"negative version", -1, "x", "CREATE TABLE x(y);"},
		{"no name", 900, "  ", "CREATE TABLE x(y);"},
		{"no statements", 901, "x", "   "},
		{"a version already taken", VersionCore, "core-again", "CREATE TABLE nope(x);"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatalf("registering %s did not panic", c.what)
				}
			}()
			RegisterMigration(c.version, c.name, c.stmts)
		})
	}
}

func TestParseMigrationName(t *testing.T) {
	version, name, err := parseMigrationName("002_indexes_roots.sql")
	if err != nil || version != 2 || name != "indexes_roots" {
		t.Errorf("parseMigrationName = %d, %q, %v; want 2, indexes_roots, nil", version, name, err)
	}
	for _, refused := range []string{"nope.sql", "abc_thing.sql"} {
		if _, _, err := parseMigrationName(refused); err == nil {
			t.Errorf("%q was accepted", refused)
		}
	}
}

// This checks the embedded set, not the global registry: another package's test
// may legitimately have filled a reserved slot by the time this runs.
func TestTheEmbeddedMigrationsAvoidReservedVersions(t *testing.T) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		t.Fatalf("reading embedded migrations: %v", err)
	}
	for _, entry := range entries {
		version, _, err := parseMigrationName(entry.Name())
		if err != nil {
			t.Fatalf("%s: %v", entry.Name(), err)
		}
		if owner, reserved := ReservedVersions[version]; reserved {
			t.Errorf("%s takes version %03d, which is reserved for %s", entry.Name(), version, owner)
		}
	}
}

func applySQL(t *testing.T, db *sql.DB, stmt, what string) {
	t.Helper()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("applying %s: %v", what, err)
	}
}

func closeDB(t *testing.T, db *sql.DB, what string) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("closing %s: %v", what, err)
	}
}

func closeStore(t *testing.T, s *Store) {
	t.Helper()
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func mustOpenPath(t *testing.T, path, what string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on %s: %v", what, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func removeAppliedVersion(t *testing.T, s *Store, version int) {
	t.Helper()
	if _, err := s.DB().Exec(`DELETE FROM schema_version WHERE version = ?`, version); err != nil {
		t.Fatalf("removing schema_version row %d: %v", version, err)
	}
}
