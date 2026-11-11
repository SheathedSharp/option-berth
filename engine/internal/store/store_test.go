package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// openTemp opens a store in a fresh temp directory and closes it when the test
// ends.
func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// pragma reads one text-valued pragma from the open store.
func pragma(t *testing.T, s *Store, name string) string {
	t.Helper()
	var value string
	if err := s.DB().QueryRow(`PRAGMA ` + name).Scan(&value); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

func pragmaInt(t *testing.T, s *Store, name string) int {
	t.Helper()
	var value int
	if err := s.DB().QueryRow(`PRAGMA ` + name).Scan(&value); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return value
}

// A new database is created ready to use: owner-only permissions, WAL, and a
// busy timeout so a second writer waits instead of failing outright.
func TestANewDatabaseIsCreatedReadyToUse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "option-berth.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	if s.DBPath() != path {
		t.Errorf("DBPath = %q, want %q", s.DBPath(), path)
	}
	if s.Reset() != nil {
		t.Error("a fresh database reported a reset")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if runtime.GOOS != "windows" {
		if mode := info.Mode().Perm(); mode != 0o600 {
			t.Errorf("database mode = %o, want 600", mode)
		}
	}
	if mode := pragma(t, s, "journal_mode"); !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
	if timeout := pragmaInt(t, s, "busy_timeout"); timeout <= 0 {
		t.Errorf("busy_timeout = %d, want a positive timeout", timeout)
	}
}

// Reopening changes nothing: the version does not move, the rows are still
// there, and every migration is recorded exactly once.
func TestReopeningChangesNothing(t *testing.T) {
	root := aliasFixtureRoot(t)
	path := filepath.Join(t.TempDir(), "option-berth.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := first.SetGroupAlias(root, "shop"); err != nil {
		t.Fatalf("SetGroupAlias: %v", err)
	}
	before, err := first.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	closeStore(t, first)
	second := mustOpenPath(t, path, "the same file after a close")

	after, err := second.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if before != after {
		t.Errorf("the version moved on reopen: %d -> %d", before, after)
	}
	if aliases, err := second.GroupAliases(); err != nil || aliases[root] != "shop" {
		t.Errorf("GroupAliases after reopen = %v (%v), want %q -> shop", aliases, err, root)
	}
	var recorded int
	if err := second.DB().QueryRow(`SELECT count(*) FROM schema_version`).Scan(&recorded); err != nil {
		t.Fatalf("counting schema_version: %v", err)
	}
	if want := len(registry.all()); recorded != want {
		t.Errorf("schema_version has %d rows, want one per registered migration (%d)", recorded, want)
	}
}

func TestPathHonoursTheEnvironmentOverride(t *testing.T) {
	t.Setenv("BERTH_DB", "/tmp/custom/option-berth.db")
	if got := Path(); got != "/tmp/custom/option-berth.db" {
		t.Errorf("Path() = %q, want the BERTH_DB override", got)
	}

	t.Setenv("BERTH_DB", "")
	got := Path()
	if filepath.Base(got) != "option-berth.db" {
		t.Errorf("Path() = %q, want it to end in option-berth.db", got)
	}
	if want := filepath.Join("option-berth", "option-berth.db"); !strings.HasSuffix(got, want) {
		t.Errorf("Path() = %q, want it inside the oberth config directory", got)
	}
}

// A file that is not a database is parked beside itself, reported, and replaced
// by one that works. The parked file keeps the original bytes, because it is the
// only copy of whatever was in there.
func TestAnUnusableFileIsParkedAndReplaced(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	junk := []byte(strings.Repeat("this is definitely not a sqlite database\n", 200))
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatalf("seeding an unusable file: %v", err)
	}

	var reported []ResetInfo
	s, err := OpenWith(path, Options{OnReset: func(info ResetInfo) { reported = append(reported, info) }})
	if err != nil {
		t.Fatalf("OpenWith on an unusable file: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	reset := s.Reset()
	if reset == nil {
		t.Fatal("Reset() = nil after opening an unusable file")
	}
	if len(reported) != 1 {
		t.Fatalf("OnReset ran %d times, want 1", len(reported))
	}
	if reported[0].MovedTo != reset.MovedTo {
		t.Errorf("OnReset path %q != Reset() path %q", reported[0].MovedTo, reset.MovedTo)
	}
	if !strings.HasPrefix(filepath.Base(reset.MovedTo), "option-berth.db.corrupt-") {
		t.Errorf("parked as %q, want option-berth.db.corrupt-<timestamp>", filepath.Base(reset.MovedTo))
	}
	if reset.Reason == "" {
		t.Error("Reset().Reason is empty")
	}

	parked, err := os.ReadFile(reset.MovedTo)
	if err != nil {
		t.Fatalf("reading the parked file: %v", err)
	}
	if string(parked) != string(junk) {
		t.Error("the parked file is not the original bytes")
	}

	if err := s.SetGroupAlias("/code/shop", "shop"); err != nil {
		t.Fatalf("SetGroupAlias on the replacement database: %v", err)
	}
	if version, err := s.Version(); err != nil || version != LatestVersion() {
		t.Errorf("version after the reset = %d (%v), want %d", version, err, LatestVersion())
	}
}

// A permission problem is not corruption: replacing the file would not fix it,
// so the error reaches the caller instead of costing them the database.
func TestANonCorruptionFailureIsReported(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permissions do not gate file creation on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	locked := filepath.Join(t.TempDir(), "locked")
	if err := os.Mkdir(locked, 0o500); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if _, err := Open(filepath.Join(locked, "option-berth.db")); err == nil {
		t.Fatal("Open into an unwritable directory succeeded, want an error")
	}
}

// Zero is the only busy timeout that means the default. A nonzero value is
// used as given, even one too small to survive integer milliseconds.
func TestBusyTimeoutZeroMeansDefault(t *testing.T) {
	if got := dsnFor("option-berth.db", time.Nanosecond); !strings.Contains(got, "busy_timeout(0)") {
		t.Errorf("one nanosecond busy timeout = %q, want it used as zero milliseconds", got)
	}
	for _, busy := range []time.Duration{0, -time.Second} {
		if got := dsnFor("option-berth.db", busy); !strings.Contains(got, "busy_timeout(5000)") {
			t.Errorf("busy timeout %v = %q, want the 5s default", busy, got)
		}
	}
}

// A nil error is not corruption: the caller asks this only after a failure,
// and treating success as corruption would throw away a good database.
func TestIsCorruptionRejectsNoError(t *testing.T) {
	if isCorruption(nil) {
		t.Fatal("a nil error was called corruption")
	}
}

// SQLite does not always attach a driver code to the error the opener sees; the
// message remains the documented fallback. An unrelated failure stays a
// failure rather than becoming a reset.
func TestIsCorruptionRecognizesSQLitePhrases(t *testing.T) {
	for _, phrase := range corruptionPhrases {
		if !isCorruption(errors.New("query: " + phrase)) {
			t.Errorf("%q was not recognized as corruption", phrase)
		}
	}
	if isCorruption(errors.New("disk I/O error")) {
		t.Error("an unrelated failure was called corruption")
	}
}

// The owner-only permission check is not best-effort: when chmod refuses, the
// store must close and report rather than keep a handle it could not secure.
func TestOpenRefusesWhenThePermissionCheckFails(t *testing.T) {
	previous := chmodFile
	t.Cleanup(func() { chmodFile = previous })
	chmodFile = func(string, os.FileMode) error { return os.ErrPermission }

	s, err := Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err == nil {
		_ = s.Close()
		t.Fatal("Open succeeded although the permission check failed")
	}
	if !strings.Contains(err.Error(), "securing") {
		t.Fatalf("Open error = %v, want the securing failure", err)
	}
}

// Close is nil-safe, so a store that was never opened can still be released
// by the same cleanup path as an open one.
func TestCloseIsSafeOnANilStore(t *testing.T) {
	var s *Store
	if err := s.Close(); err != nil {
		t.Fatalf("Close on a nil store: %v", err)
	}
}

// A one-column read returns every row in the query's order.
func TestTextColumnReturnsTheRowsItRead(t *testing.T) {
	s := storeWithConnector(t, fixedConn{})

	values, err := s.textColumn(`SELECT 'one' UNION ALL SELECT 'two' ORDER BY 1`)
	if err != nil {
		t.Fatalf("textColumn: %v", err)
	}
	if want := []string{"one", "two"}; !slices.Equal(values, want) {
		t.Fatalf("values = %q, want %q", values, want)
	}
}
