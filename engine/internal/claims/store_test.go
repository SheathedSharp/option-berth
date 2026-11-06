package claims_test

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The reservations table is created by migration 006, registered from this
// package. These cases cover the table itself: the migration that creates it,
// the one-holder-per-port rule, and the lazy expiry every read sweeps. A store
// test binary that does not link this package must not grow the table, which is
// why the migration lives here and not in internal/store.

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func claimedRow(port int, key, project, worktree string, at time.Time, lease time.Duration) store.ClaimRow {
	return store.ClaimRow{
		Port: port, Key: key, Project: project, Worktree: worktree,
		Since: at, Until: at.Add(lease),
	}
}

// The migration reaches the newest registered version and really creates the
// table. Registration from this package is what makes 006 present at all.
func TestTheMigrationCreatesTheReservationsTable(t *testing.T) {
	st := openStore(t)

	version, err := st.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != store.LatestVersion() {
		t.Fatalf("version = %d, want the latest registered %d", version, store.LatestVersion())
	}

	if !recorded(t, st, `SELECT 1 FROM schema_version WHERE version = ?`, store.VersionClaims) {
		t.Fatalf("schema_version does not record claims' migration %d", store.VersionClaims)
	}
	if !recorded(t, st, `SELECT 1 FROM sqlite_master WHERE type = 'table' AND name = ?`, "claims") {
		t.Fatal("the claims table is missing")
	}
}

// recorded reports whether a one-column probe query returns a row.
func recorded(t *testing.T, st *store.Store, query string, arg any) bool {
	t.Helper()
	var found int
	err := st.DB().QueryRow(query, arg).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return false
	}
	if err != nil {
		t.Fatalf("probe %q: %v", query, err)
	}
	return true
}

func TestAStoredReservationReadsBackUnchanged(t *testing.T) {
	c := openStore(t).Claims()
	at := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)

	if err := c.Put(
		claimedRow(10001, "a/main/api", "a", "main", at, time.Hour),
		claimedRow(10002, "a/main/api", "a", "main", at, time.Hour),
		claimedRow(20001, "b/main/api", "b", "main", at, 2*time.Hour),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := c.Get("a/main/api")
	if err != nil || len(got) != 2 || got[0].Port != 10001 || got[1].Port != 10002 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if !got[0].Since.Equal(at) || !got[0].Until.Equal(at.Add(time.Hour)) {
		t.Errorf("times did not round trip: %+v", got[0])
	}

	if all, err := c.List(); err != nil || len(all) != 3 {
		t.Fatalf("List = %+v, %v", all, err)
	}

	if n, err := c.Delete("a/main/api"); err != nil || n != 2 {
		t.Fatalf("Delete = %d, %v; want 2", n, err)
	}
	if n, err := c.Delete("nobody/main/api"); err != nil || n != 0 {
		t.Fatalf("Delete of an unheld key = %d, %v; want 0, nil", n, err)
	}
}

// One port has one holder. A competing key is refused and nothing of its set is
// written, so a half-taken block can never exist.
func TestOnePortHasOneHolder(t *testing.T) {
	c := openStore(t).Claims()
	now := time.Now()
	if err := c.Put(claimedRow(10001, "a/main/api", "a", "main", now, time.Hour)); err != nil {
		t.Fatalf("Put: %v", err)
	}

	err := c.Put(
		claimedRow(10005, "b/main/api", "b", "main", now, time.Hour),
		claimedRow(10001, "b/main/api", "b", "main", now, time.Hour),
	)
	if !errors.Is(err, store.ErrClaimed) {
		t.Fatalf("Put over a foreign reservation = %v, want ErrClaimed", err)
	}
	if rows, err := c.Get("b/main/api"); err != nil || len(rows) != 0 {
		t.Fatalf("rows = %+v, %v; want the whole Put rolled back", rows, err)
	}
}

// A renewal moves the expiry and keeps the moment the service first took the
// port, so "how long has this address been held" survives every start.
func TestARenewalKeepsTheOriginalStart(t *testing.T) {
	c := openStore(t).Claims()
	first := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	later := first.Add(time.Hour)

	row := claimedRow(10001, "a/main/api", "a", "main", first, time.Hour)
	if err := c.Put(row); err != nil {
		t.Fatalf("Put: %v", err)
	}
	row.Since, row.Until = later, later.Add(time.Hour)
	if err := c.Put(row); err != nil {
		t.Fatalf("renewal: %v", err)
	}

	got, err := c.Get("a/main/api")
	if err != nil || len(got) != 1 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if !got[0].Since.Equal(first) {
		t.Errorf("created_at = %v, want the original %v", got[0].Since, first)
	}
	if !got[0].Until.Equal(later.Add(time.Hour)) {
		t.Errorf("expires_at = %v, want the renewed one", got[0].Until)
	}
}

func TestExpiryDropsOnlyTheReservationsThatLapsed(t *testing.T) {
	c := openStore(t).Claims()
	now := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	if err := c.Put(
		claimedRow(10001, "gone/main/api", "gone", "main", now, time.Minute),
		claimedRow(10002, "live/main/api", "live", "main", now, time.Hour),
	); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if n, err := c.Expire(now.Add(2 * time.Minute)); err != nil || n != 1 {
		t.Fatalf("Expire = %d, %v; want 1", n, err)
	}
	rows, err := c.List()
	if err != nil || len(rows) != 1 || rows[0].Key != "live/main/api" {
		t.Fatalf("rows = %+v, %v; want only the live reservation", rows, err)
	}
}

// A row a reader could not interpret is refused before anything is written: no
// port outside the range, no nameless holder, no reservation without both ends.
func TestARowThatCouldNotBeStoredIsRefused(t *testing.T) {
	c := openStore(t).Claims()
	at := time.Date(2026, 9, 6, 9, 0, 0, 0, time.UTC)
	for _, refused := range []struct {
		what string
		row  store.ClaimRow
	}{
		{"port 0", store.ClaimRow{Port: 0, Key: "a/main/api", Since: at, Until: at.Add(time.Hour)}},
		{"an empty key", store.ClaimRow{Port: 10001, Key: "  ", Since: at, Until: at.Add(time.Hour)}},
		{"no start", store.ClaimRow{Port: 10002, Key: "a/main/api", Until: at.Add(time.Hour)}},
		{"no expiry", store.ClaimRow{Port: 10003, Key: "a/main/api", Since: at}},
	} {
		if err := c.Put(refused.row); err == nil {
			t.Errorf("%s was accepted", refused.what)
		}
	}
	if rows, err := c.List(); err != nil || len(rows) != 0 {
		t.Fatalf("a refused row was partly written: %+v, %v", rows, err)
	}
	if err := c.Put(); err != nil {
		t.Errorf("Put with no rows = %v, want nil", err)
	}
}
