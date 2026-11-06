package sessions_test

// The sessions table and its migration. They are tested here rather than in
// internal/store because migration 005 belongs to this package: the store
// reserves the version and the owning package fills the slot in, so only a
// binary that links sessions ever creates the table.

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/store"

	_ "modernc.org/sqlite"
)

func newStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sessionFixture(id string, seen time.Time) store.SessionRow {
	return store.SessionRow{
		ID:        id,
		Tool:      sessions.ToolClaudeCode,
		Label:     "build the thing",
		Worktree:  "feature-x",
		Branch:    "feature/x",
		Detected:  true,
		FirstSeen: seen,
		LastSeen:  seen,
	}
}

// migrationRecorded reports whether this database recorded one migration
// version. Later versions sit above 005, so the highest version alone cannot
// prove that 005 ran.
func migrationRecorded(t *testing.T, s *store.Store, version int) bool {
	t.Helper()
	var rows int
	err := s.DB().QueryRow(
		`SELECT count(*) FROM schema_version WHERE version = ?`, version).Scan(&rows)
	if err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	return rows == 1
}

func TestSessionsMigrationTakesItsReservedVersion(t *testing.T) {
	s := newStore(t)
	version, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != store.LatestVersion() || !migrationRecorded(t, s, store.VersionSessions) {
		t.Errorf("version = %d, want %d and migration %d recorded",
			version, store.LatestVersion(), store.VersionSessions)
	}

	var objects int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM sqlite_master
		 WHERE name IN ('sessions', 'idx_sessions_last_seen')`).Scan(&objects); err != nil {
		t.Fatalf("reading sqlite_master: %v", err)
	}
	if objects != 2 {
		t.Errorf("found %d of the two objects migration 005 creates", objects)
	}
}

// A database written by a build that only knew migrations 001 and 002 must
// reach the current version without losing what it stored.
func TestUpgradeFromVersionTwoKeepsItsRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	seedVersionTwo(t, path)

	s := openMigrated(t, path)

	if version, err := s.Version(); err != nil || version != store.LatestVersion() ||
		!migrationRecorded(t, s, store.VersionSessions) {
		t.Fatalf("version after upgrade = %d (%v)", version, err)
	}

	var renamed string
	if err := s.DB().QueryRow(
		`SELECT name FROM renames WHERE key = ?`, "cwd:/home/me/code/shop:3000").Scan(&renamed); err != nil ||
		renamed != "storefront" {
		t.Errorf("the v2 rename did not survive: %q (%v)", renamed, err)
	}
	if roots, err := s.Roots(); err != nil || len(roots) != 1 {
		t.Errorf("the v2 known_roots row did not survive: %v, %v", roots, err)
	}
	if err := s.Sessions().Upsert(sessionFixture("s1", time.Now())); err != nil {
		t.Errorf("writing to the upgraded sessions table: %v", err)
	}
}

// openRaw opens a database without the store's migration machinery, which is
// how the tests lay down a historical schema first.
func openRaw(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatalf("opening raw sqlite: %v", err)
	}
	return db
}

func mustRun(t *testing.T, db *sql.DB, statement string) {
	t.Helper()
	if _, err := db.Exec(statement); err != nil {
		t.Fatalf("exec: %v", err)
	}
}

func mustClose(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := db.Close(); err != nil {
		t.Fatalf("closing raw sqlite: %v", err)
	}
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %q: %v", name, err)
	}
	return string(content)
}

func seedVersionTwo(t *testing.T, path string) {
	t.Helper()
	db := openRaw(t, path)
	mustRun(t, db, readFixture(t, "v2_schema.sql"))
	mustClose(t, db)
}

func mustList(t *testing.T, table store.SessionStore) []store.SessionRow {
	t.Helper()
	rows, err := table.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return rows
}

// openMigrated opens an existing database and insists it was upgraded rather
// than discarded as corrupt.
func openMigrated(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if s.Reset() != nil {
		t.Fatalf("the valid database %s was reset", path)
	}
	return s
}

func TestUpsertRefreshesButKeepsFirstSeen(t *testing.T) {
	table := newStore(t).Sessions()
	first := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	if err := table.Upsert(sessionFixture("s1", first)); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	later := first.Add(2 * time.Hour)
	changed := sessionFixture("s1", later)
	changed.Branch = "main"
	changed.Detected = false
	if err := table.Upsert(changed); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}

	rows := mustList(t, table)
	if len(rows) != 1 {
		t.Fatalf("List returned %d rows, want 1", len(rows))
	}
	row := rows[0]
	if !row.FirstSeen.Equal(first) {
		t.Errorf("first_seen = %v, want %v", row.FirstSeen, first)
	}
	if !row.LastSeen.Equal(later) {
		t.Errorf("last_seen = %v, want %v", row.LastSeen, later)
	}
	if row.Branch != "main" || row.Detected {
		t.Errorf("the second write did not refresh the row: %+v", row)
	}
	if row.Tool != sessions.ToolClaudeCode || row.Worktree != "feature-x" || row.Label == "" {
		t.Errorf("round trip lost fields: %+v", row)
	}

	touchedAt := later.Add(time.Hour)
	if err := table.Touch("s1", touchedAt); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	rows = mustList(t, table)
	if !rows[0].LastSeen.Equal(touchedAt) {
		t.Errorf("Touch left last_seen at %v", rows[0].LastSeen)
	}

	// Touching an unknown id creates nothing.
	if err := table.Touch("ghost", touchedAt); err != nil {
		t.Fatalf("Touch on an unknown session: %v", err)
	}
	if len(mustList(t, table)) != 1 {
		t.Error("Touch created a row")
	}
}

func TestUpsertRejectsARowWithoutAnID(t *testing.T) {
	if err := newStore(t).Sessions().Upsert(store.SessionRow{}); err == nil {
		t.Error("an id-less session was accepted")
	}
}

func TestListIsNewestFirstAndGetFindsOne(t *testing.T) {
	table := newStore(t).Sessions()
	base := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)
	for offset, id := range []string{"old", "new"} {
		if err := table.Upsert(sessionFixture(id, base.Add(time.Duration(offset)*time.Hour))); err != nil {
			t.Fatal(err)
		}
	}
	rows := mustList(t, table)
	if len(rows) != 2 {
		t.Fatalf("List returned %d rows, want 2", len(rows))
	}
	if rows[0].ID != "new" {
		t.Errorf("List order = %q, %q; want newest first", rows[0].ID, rows[1].ID)
	}

	found, ok, err := table.Get("old")
	if err != nil || !ok || found.ID != "old" {
		t.Errorf("Get(old) = %+v, %v, %v", found, ok, err)
	}
	if _, ok, _ := table.Get("missing"); ok {
		t.Error("Get found a session that was never recorded")
	}
}

func TestPruneDropsOnlySessionsOutsideTheWindow(t *testing.T) {
	table := newStore(t).Sessions()
	now := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	for _, row := range []store.SessionRow{
		sessionFixture("stale", now.Add(-8*24*time.Hour)),
		sessionFixture("recent", now.Add(-6*24*time.Hour)),
	} {
		if err := table.Upsert(row); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := table.Prune(now.Add(-sessions.Retention))
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("Prune removed %d rows, want 1", removed)
	}
	rows := mustList(t, table)
	if len(rows) != 1 || rows[0].ID != "recent" {
		t.Errorf("after prune: %+v", rows)
	}
}

// A database that reached version 006 in a build without sessions must still
// receive 005 when a build that owns sessions opens it. This is why the store
// applies every registered version without a schema_version row, rather than
// everything above the highest one.
func TestMigrationFiveLandsOnADatabaseAlreadyAtSix(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	seedVersionTwo(t, path)

	raw := openRaw(t, path)
	mustRun(t, raw, `CREATE TABLE claims (port INTEGER PRIMARY KEY, key TEXT NOT NULL);
		INSERT INTO schema_version(version, name, applied_at)
		VALUES (6, 'claims', '2026-09-05T00:00:00.000000000Z');`)
	mustClose(t, raw)

	s := openMigrated(t, path)

	if err := s.Sessions().Upsert(sessionFixture("s1", time.Now())); err != nil {
		t.Fatalf("migration 005 did not run on a v6 database: %v", err)
	}
	var recorded int
	if err := s.DB().QueryRow(
		`SELECT count(*) FROM schema_version WHERE version = ?`,
		store.VersionSessions).Scan(&recorded); err != nil {
		t.Fatalf("counting the 005 row: %v", err)
	}
	if recorded != 1 {
		t.Errorf("schema_version has %d rows for version %d, want 1",
			recorded, store.VersionSessions)
	}
	if _, err := s.DB().Exec(`INSERT INTO claims(port, key) VALUES(3000, 'shop/main')`); err != nil {
		t.Errorf("the sibling package's table did not survive: %v", err)
	}
}

// A row written by a build with a different timestamp layout keeps its
// identity; only the timestamps degrade to unknown.
func TestUnreadableTimestampsDoNotLoseTheRow(t *testing.T) {
	s := newStore(t)
	if _, err := s.DB().Exec(
		`INSERT INTO sessions(id, tool, label, worktree, branch, detected, first_seen, last_seen)
		 VALUES('s1', 'tool', '', '/code/shop', 'main', 0, 'not-a-time', 'also-not')`); err != nil {
		t.Fatalf("raw insert: %v", err)
	}
	row, ok, err := s.Sessions().Get("s1")
	if err != nil || !ok {
		t.Fatalf("Get = %+v/%v/%v", row, ok, err)
	}
	if !row.FirstSeen.IsZero() || !row.LastSeen.IsZero() {
		t.Errorf("unreadable timestamps = %v/%v, want zero times", row.FirstSeen, row.LastSeen)
	}
	if row.Branch != "main" {
		t.Errorf("branch = %q, want the row kept intact", row.Branch)
	}
}
