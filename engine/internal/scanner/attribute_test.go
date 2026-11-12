package scanner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// openStore gives a test its own database file.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "option-berth.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// loopWith builds a loop over a fixed scan result, backed by st.
func loopWith(st Store, rows ...ports.ListeningPort) *Loop {
	fixed := append([]ports.ListeningPort(nil), rows...)
	return New(Options{
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(fixed))
			copy(out, fixed)
			return out, nil
		},
	})
}

func TestScanWithoutAStoreLeavesNamesAlone(t *testing.T) {
	loop := loopWith(nil, ports.ListeningPort{
		Port: 8234, Process: "node", PID: 42, Command: "node server.js",
	})
	snap, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	// The row is the precondition for reading its fields: a scan that published
	// nothing is an assertion failure here, not an index panic three lines on.
	if len(snap.Ports) != 1 {
		t.Fatalf("ports = %+v, want the scanned row", snap.Ports)
	}
	if snap.Ports[0].Name != nil {
		t.Errorf("name = %v, want null while no store is installed", *snap.Ports[0].Name)
	}
	if snap.Ports[0].DisplayName == "" {
		t.Error("display_name is empty; the derived name should still be there")
	}
}

func TestScanRecordsHistoryFromTheDiff(t *testing.T) {
	st := openStore(t)

	listener := ports.ListeningPort{Port: 8234, PID: 42, Process: "node", Command: "node server.js"}
	var feed []ports.ListeningPort
	loop := New(Options{
		DaemonVersion: "test",
		Store:         st,
		Scan: func(Include) ([]ports.ListeningPort, error) {
			out := make([]ports.ListeningPort, len(feed))
			copy(out, feed)
			return out, nil
		},
	})

	feed = []ports.ListeningPort{listener}
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	// Same key, new PID: the diff calls that a restart, not an up/down pair.
	rebound := listener
	rebound.PID = 43
	feed = []ports.ListeningPort{rebound}
	loop.Invalidate()
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	feed = nil
	loop.Invalidate()
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}

	events, err := st.History(store.HistoryFilter{})
	if err != nil {
		t.Fatalf("querying history: %v", err)
	}
	var kinds []string
	for _, event := range events {
		kinds = append(kinds, event.Kind)
	}
	want := []string{store.EventPortDown, store.EventPortRestarted, store.EventPortUp}
	if len(kinds) != len(want) {
		t.Fatalf("history kinds = %v, want %v", kinds, want)
	}
	for i, kind := range kinds {
		if kind != want[i] {
			t.Fatalf("history kind %d = %q, want %q (newest first)", i, kind, want[i])
		}
	}
	if events[0].Port != 8234 || events[0].Command == "" {
		t.Errorf("history row = %+v, want port 8234 with its command", events[0])
	}
}

func TestSeedsTheIndexFromKnownRoots(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	writeConfig(t, root, "name: seeded\n")
	if err := st.AddRoot(root); err != nil {
		t.Fatal(err)
	}

	loop := loopWith(st)
	snap, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "seeded" {
		t.Fatalf("groups = %+v, want the seeded config's group", snap.Groups)
	}
	if status := snap.Groups[0].Status; status != "stopped" {
		t.Errorf("status = %q, want stopped for a project with nothing running", status)
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

	loop := loopWith(st)
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot with a vanished root recorded: %v", err)
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
	snap, err := loop.Snapshot(Include{})
	if err != nil {
		t.Fatalf("Snapshot after the seed: %v", err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "live" {
		t.Fatalf("groups = %+v, want the surviving project", snap.Groups)
	}
}

func TestRemembersNewlySeenRoots(t *testing.T) {
	st := openStore(t)
	root := t.TempDir()
	writeConfig(t, root, "name: observed\n")

	loop := loopWith(st, ports.ListeningPort{
		Port: 8234, Process: "node", PID: 42, Command: "node server.js", Cwd: root,
	})
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatalf("Snapshot after the first scan: %v", err)
	}

	roots, err := st.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{resolved(t, root)}; len(roots) != 1 || roots[0] != want[0] {
		t.Fatalf("known roots = %v, want %v", roots, want)
	}
}

func TestInvalidateForcesTheNextReadToRescan(t *testing.T) {
	calls := 0
	loop := New(Options{
		DaemonVersion: "test",
		Scan: func(Include) ([]ports.ListeningPort, error) {
			calls++
			return nil, nil
		},
	})
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("collector calls = %d, want the second read served from the cache", calls)
	}
	loop.Invalidate()
	if _, err := loop.Snapshot(Include{}); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("collector calls = %d, want a rescan after Invalidate", calls)
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
	path := filepath.Join(dir, ".oberth.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

func resolved(t *testing.T, dir string) string {
	t.Helper()
	full, err := filepath.Abs(dir)
	if err != nil {
		t.Fatalf("resolving %s: %v", dir, err)
	}
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return filepath.Clean(full)
	}
	return filepath.Clean(real)
}

// cancellingRoots withdraws the caller while the store is answering, which is
// the boundary the attribution stages check between each other.
type cancellingRoots struct {
	identityLedger
	cancel func()
}

func (c *cancellingRoots) Roots() ([]string, error) {
	c.cancel()
	return nil, nil
}

// TestAttributionStopsBetweenStagesWhenTheCallerLeaves: a scan that is
// withdrawn while the store is answering must not go on to attribute, probe or
// publish. The check between stages is what makes cancellation not success.
func TestAttributionStopsBetweenStagesWhenTheCallerLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := New(Options{Store: &cancellingRoots{cancel: cancel}})

	rows, groups, err := l.attributeContext(ctx, []ports.ListeningPort{{Port: 8123, PID: 42}}, false)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the caller's cancellation", err)
	}
	if rows != nil || groups != nil {
		t.Fatalf("rows=%v groups=%v, want nothing attributed for a withdrawn caller", rows, groups)
	}
}
