package scanner

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// openStore gives a test its own database file.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// loopWith builds a loop over a fixed scan result, backed by st.
func loopWith(st Store, rows ...ports.ListeningPort) *Loop {
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(rows))
			copy(out, rows)
			return out, nil
		},
	})
	return l
}

func TestScanWithoutAStoreLeavesNamesAlone(t *testing.T) {
	l := loopWith(nil, ports.ListeningPort{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
	})
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if snap.Ports[0].Name != nil {
		t.Errorf("name = %v, want null with no store", *snap.Ports[0].Name)
	}
	if snap.Ports[0].DisplayName == "" {
		t.Error("display_name is empty; the derived name should still be there")
	}
}

func TestScanRecordsHistoryFromTheDiff(t *testing.T) {
	st := openStore(t)

	up := ports.ListeningPort{Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server"}
	var rows []ports.ListeningPort
	l := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(rows))
			copy(out, rows)
			return out, nil
		},
	})

	rows = []ports.ListeningPort{up}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	// Same key, new PID: the diff calls that a restart, not an up/down pair.
	restarted := up
	restarted.PID = 43
	rows = []ports.ListeningPort{restarted}
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	rows = nil
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}

	events, err := st.Query(nil, time.Time{}, 0)
	if err != nil {
		t.Fatalf("querying history: %v", err)
	}
	var kinds []string
	for _, e := range events {
		kinds = append(kinds, e.Kind)
	}
	want := []string{store.EventPortDown, store.EventPortRestarted, store.EventPortUp}
	if len(kinds) != len(want) {
		t.Fatalf("history kinds = %v, want %v", kinds, want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("history kinds = %v, want %v (newest first)", kinds, want)
		}
	}
	if events[0].Port != 8123 || events[0].Command == "" {
		t.Errorf("history row = %+v, want port 8123 with its command", events[0])
	}
}

func TestSeedsTheIndexFromKnownRoots(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: seeded\n")
	if err := st.AddRoot(dir); err != nil {
		t.Fatal(err)
	}

	l := loopWith(st)
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "seeded" {
		t.Fatalf("groups = %+v, want the seeded config's group", snap.Groups)
	}
	if snap.Groups[0].Status != "stopped" {
		t.Errorf("status = %q, want stopped for a project with nothing running", snap.Groups[0].Status)
	}
}

// A root whose directory is gone is dropped when the index is seeded. The
// store only ever gains rows (rememberRoots), so nothing else would ever
// forget a project that was deleted or moved — it would be re-seeded on every
// daemon start for as long as the database lives.
func TestDropsVanishedRootsOnSeed(t *testing.T) {
	st := openStore(t)
	live := t.TempDir()
	writeConfig(t, live, "name: live\n")
	// A directory that existed when its root was recorded and does not now —
	// the moved checkout, or a test's own temp dir.
	gone := filepath.Join(t.TempDir(), "moved-away")
	for _, dir := range []string{live, gone} {
		if err := st.AddRoot(dir); err != nil {
			t.Fatal(err)
		}
	}

	l := loopWith(st)
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	roots, err := st.Roots()
	if err != nil {
		t.Fatal(err)
	}
	// Matched on the base name, not the whole path: the same directory can sit
	// in the table under two spellings (AddRoot keeps what it was handed, while
	// rememberRoots stores the symlink-resolved one — /var against /private/var
	// on macOS). That duplication is its own problem and not what this asserts.
	for _, root := range roots {
		if filepath.Base(root) == "moved-away" {
			t.Fatalf("known roots = %v, want the vanished directory dropped", roots)
		}
	}
	if len(roots) == 0 {
		t.Fatalf("known roots = %v, want the surviving project kept", roots)
	}
	// The project itself is untouched: forgetting the path is not forgetting
	// the config.
	snap, err := l.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "live" {
		t.Fatalf("groups = %+v, want the surviving project", snap.Groups)
	}
}

func TestRemembersNewlySeenRoots(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: observed\n")

	l := loopWith(st, ports.ListeningPort{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server", Cwd: dir,
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	roots, err := st.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != resolved(t, dir) {
		t.Fatalf("known roots = %v, want [%s]", roots, resolved(t, dir))
	}
}

func TestInvalidateForcesTheNextReadToRescan(t *testing.T) {
	scans := 0
	l := New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			scans++
			return nil, nil
		},
	})
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if scans != 1 {
		t.Fatalf("scans = %d, want the second read served from the cache", scans)
	}
	l.Invalidate()
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if scans != 2 {
		t.Fatalf("scans = %d, want a rescan after Invalidate", scans)
	}
}

func TestSetStoreInstallsOneAfterConstruction(t *testing.T) {
	st := openStore(t)
	dir := t.TempDir()
	l := loopWith(nil, ports.ListeningPort{Port: 8123, PID: 42, Process: "python3", Cwd: dir})

	// The daemon hands the loop its store after the loop is already scanning,
	// and from then on a `oberth.yaml` it sees is remembered.
	l.SetStore(st)
	writeConfig(t, dir, "name: shop\n")
	if _, err := l.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if roots, err := st.Roots(); err != nil || len(roots) != 1 {
		t.Errorf("roots = %v (%v), want the newly seen config's directory recorded", roots, err)
	}
}

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".oberth.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	return filepath.Clean(abs)
}
