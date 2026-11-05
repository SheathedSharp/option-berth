package claims_test

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Migration 006 and the claims table it creates are tested here because this
// package registers the migration: a store test binary that does not link
// claims must not grow the table.

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func claimRow(port int, key string, createdAt, expiresAt time.Time) store.ClaimRow {
	project, worktree, _ := strings.Cut(key, "/")
	return store.ClaimRow{
		Port: port, Key: key, Project: project, Worktree: worktree,
		CreatedAt: createdAt, ExpiresAt: expiresAt,
	}
}

func TestMigrationSixIsAppliedAndCreatesTheTable(t *testing.T) {
	st := openStore(t)

	version, err := st.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != store.LatestVersion() {
		t.Fatalf("version = %d, want the latest registered %d", version, store.LatestVersion())
	}

	var applied []int
	cursor, err := st.DB().Query(`SELECT version FROM schema_version ORDER BY version`)
	if err != nil {
		t.Fatalf("reading schema_version: %v", err)
	}
	defer func() { _ = cursor.Close() }()
	for cursor.Next() {
		var recorded int
		if err := cursor.Scan(&recorded); err != nil {
			t.Fatal(err)
		}
		applied = append(applied, recorded)
	}
	found := false
	for _, recorded := range applied {
		found = found || recorded == store.VersionClaims
	}
	if len(applied) < 3 || !found {
		t.Fatalf("applied versions = %v, want them to include %d", applied, store.VersionClaims)
	}

	var table string
	if err := st.DB().QueryRow(
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'claims'`).Scan(&table); err != nil {
		t.Fatalf("the claims table is missing: %v", err)
	}
}

func TestClaimsTableRoundTrip(t *testing.T) {
	table := openStore(t).Claims()
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	rows := []store.ClaimRow{
		claimRow(10001, "a/main", now, now.Add(time.Hour)),
		claimRow(10002, "a/main", now, now.Add(time.Hour)),
		claimRow(20001, "b/main", now, now.Add(2*time.Hour)),
	}
	if err := table.Put(rows...); err != nil {
		t.Fatalf("Put: %v", err)
	}

	held, err := table.Get("a/main")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(held) != 2 || held[0].Port != 10001 || held[1].Port != 10002 {
		t.Fatalf("Get = %+v, want the two a/main ports", held)
	}
	if !held[0].CreatedAt.Equal(now) || !held[0].ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Errorf("timestamps did not round trip: %+v", held[0])
	}

	if all, err := table.List(); err != nil || len(all) != 3 {
		t.Fatalf("List = %+v, %v", all, err)
	}
	if removed, err := table.Delete("a/main"); err != nil || removed != 2 {
		t.Fatalf("Delete = %d, %v; want 2", removed, err)
	}
	if removed, err := table.Delete("nobody/main"); err != nil || removed != 0 {
		t.Fatalf("Delete of an unheld key = %d, %v; want 0, nil", removed, err)
	}
}

func TestPutRejectsAForeignPortAndRollsBackTheBatch(t *testing.T) {
	table := openStore(t).Claims()
	now := time.Now()
	if err := table.Put(claimRow(10001, "a/main", now, now.Add(time.Hour))); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := table.Put(
		claimRow(10005, "b/main", now, now.Add(time.Hour)),
		claimRow(10001, "b/main", now, now.Add(time.Hour)),
	)
	if !errors.Is(err, store.ErrClaimed) {
		t.Fatalf("Put over a foreign claim = %v, want ErrClaimed", err)
	}
	if rows, err := table.Get("b/main"); err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, %v; want the whole batch rolled back", rows, err)
	}
}

func TestRefreshKeepsCreatedAtAndMovesExpiry(t *testing.T) {
	table := openStore(t).Claims()
	first := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)

	row := claimRow(10001, "a/main", first, first.Add(time.Hour))
	if err := table.Put(row); err != nil {
		t.Fatalf("Put: %v", err)
	}
	row.CreatedAt = later
	row.ExpiresAt = later.Add(time.Hour)
	if err := table.Put(row); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	held, err := table.Get("a/main")
	if err != nil || len(held) != 1 {
		t.Fatalf("Get = %+v, %v", held, err)
	}
	if !held[0].CreatedAt.Equal(first) {
		t.Errorf("created_at = %v, want the original %v", held[0].CreatedAt, first)
	}
	if !held[0].ExpiresAt.Equal(later.Add(time.Hour)) {
		t.Errorf("expires_at = %v, want the refreshed value", held[0].ExpiresAt)
	}
}

func TestExpireDropsOnlyFinishedClaims(t *testing.T) {
	table := openStore(t).Claims()
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	if err := table.Put(
		claimRow(10001, "gone/main", now, now.Add(time.Minute)),
		claimRow(10002, "live/main", now, now.Add(time.Hour)),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}

	removed, err := table.Expire(now.Add(2 * time.Minute))
	if err != nil || removed != 1 {
		t.Fatalf("Expire = %d, %v; want 1", removed, err)
	}
	rows, err := table.List()
	if err != nil || len(rows) != 1 || rows[0].Key != "live/main" {
		t.Fatalf("rows = %+v, %v; want only the live claim", rows, err)
	}
}

func TestPutRejectsInvalidRows(t *testing.T) {
	table := openStore(t).Claims()
	if err := table.Put(store.ClaimRow{Port: 0, Key: "a/main"}); err == nil {
		t.Error("port 0 was accepted")
	}
	if err := table.Put(store.ClaimRow{Port: 10001, Key: "  "}); err == nil {
		t.Error("a blank key was accepted")
	}
	if err := table.Put(); err != nil {
		t.Errorf("Put with no rows = %v, want nil", err)
	}
}
