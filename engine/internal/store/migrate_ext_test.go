package store_test

// A sibling package filling in one of the reserved migration versions, exactly
// as spec 3 will for shares (cross-spec contract §8): register from outside
// internal/store, then open a store and find the table there.

import (
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

const reservedSharesDDL = `
		CREATE TABLE shares (
			id         TEXT PRIMARY KEY,
			port       INTEGER NOT NULL,
			url        TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`

func TestReservedMigrationFromAnotherPackage(t *testing.T) {
	if reservedSharesAlreadyRegistered() {
		t.Skip("migration 003 is already registered in this process")
	}

	requireLatestAtLeast(t, store.VersionIndexes)
	store.RegisterMigration(store.VersionShares, "shares", reservedSharesDDL)
	if got := store.LatestVersion(); got != store.VersionShares {
		t.Fatalf("LatestVersion = %d after registering the reserved slot, want %d", got, store.VersionShares)
	}

	s := mustOpenTemp(t)
	version, err := s.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != store.VersionShares {
		t.Fatalf("opened version = %d, want the externally registered %d", version, store.VersionShares)
	}
	mustInsertShare(t, s, "t1", 3000, "https://x.example", "2026-01-01T00:00:00.000000000Z")
	mustRefuseDuplicateRegistration(t, store.VersionShares)
}

func requireLatestAtLeast(t *testing.T, floor int) {
	t.Helper()
	if got := store.LatestVersion(); got < floor {
		t.Fatalf("LatestVersion = %d before registering, want at least %d", got, floor)
	}
}

func mustOpenTemp(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func mustInsertShare(t *testing.T, s *store.Store, id string, port int, url, created string) {
	t.Helper()
	if _, err := s.DB().Exec(
		`INSERT INTO shares(id, port, url, created_at) VALUES(?, ?, ?, ?)`,
		id, port, url, created,
	); err != nil {
		t.Fatalf("writing to the externally registered table: %v", err)
	}
	var gotURL string
	if err := s.DB().QueryRow(`SELECT url FROM shares WHERE id = ?`, id).Scan(&gotURL); err != nil {
		t.Fatalf("reading the inserted share: %v", err)
	}
	if gotURL != url {
		t.Errorf("shares.url = %q, want %q", gotURL, url)
	}
}

func mustRefuseDuplicateRegistration(t *testing.T, version int) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("a second registration of the reserved version did not panic")
		}
	}()
	store.RegisterMigration(version, "shares-again", "CREATE TABLE nope(x);")
}

// reservedSharesAlreadyRegistered is true after a previous test in this
// process filled the reserved slot. The registry panics on a second
// RegisterMigration of the same version, so `go test -count=2` must skip.
func reservedSharesAlreadyRegistered() bool {
	return store.LatestVersion() >= store.VersionShares
}
