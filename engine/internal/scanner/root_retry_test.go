package scanner

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// Exercise the scanner/store boundary with a real isolated SQLite store. The
// wrapper fails one operation before delegation; later calls use real SQL.
type retryRootStore struct {
	*store.Store
	failReads, failAdds, failRemoves int
	reads, adds, removes             int
	extraRoots                       []string
}

func (s *retryRootStore) Roots() ([]string, error) {
	s.reads++
	if s.failReads > 0 {
		s.failReads--
		return nil, errors.New("temporary root read failure")
	}
	roots, err := s.Store.Roots()
	return append(roots, s.extraRoots...), err
}
func (s *retryRootStore) AddRoot(path string) error {
	s.adds++
	if s.failAdds > 0 {
		s.failAdds--
		return errors.New("temporary root write failure")
	}
	return s.Store.AddRoot(path)
}
func (s *retryRootStore) RemoveRoot(path string) error {
	s.removes++
	if s.failRemoves > 0 {
		s.failRemoves--
		return errors.New("temporary root delete failure")
	}
	return s.Store.RemoveRoot(path)
}

func TestRootSeedRetriesReadFailure(t *testing.T) {
	db := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: recovered\n")
	if err := db.AddRoot(dir); err != nil {
		t.Fatal(err)
	}
	st := &retryRootStore{Store: db, failReads: 1}
	l := loopWith(st)
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	snap, err := l.Rescan(Include{})
	if err != nil {
		t.Fatal(err)
	}
	if st.reads != 2 || len(snap.Groups) != 1 || snap.Groups[0].Name != "recovered" {
		t.Fatal("failed seed was acknowledged instead of retried")
	}
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	if st.reads != 2 {
		t.Fatal("successful seed was needlessly repeated")
	}
}

func TestRootRememberRetriesWriteFailure(t *testing.T) {
	db := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: recovered\n")
	st := &retryRootStore{Store: db, failAdds: 1}
	l := loopWith(st, ports.ListeningPort{Port: 8123, PID: 42, Cwd: dir})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	if roots, err := db.Roots(); err != nil || len(roots) != 0 {
		t.Fatal("fixture write should have failed", roots, err)
	}
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	roots, err := db.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if st.adds != 2 || len(roots) != 1 || roots[0] != resolved(t, dir) {
		t.Fatal("failed root write was cached as persisted")
	}
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	if st.adds != 2 {
		t.Fatal("successful root was written again")
	}
}

func TestRootSeedRetriesDeleteFailure(t *testing.T) {
	db := openStore(t)
	gone := filepath.Join(t.TempDir(), "missing")
	if err := db.AddRoot(gone); err != nil {
		t.Fatal(err)
	}
	st := &retryRootStore{Store: db, failRemoves: 1}
	l := loopWith(st)
	for i := 0; i < 2; i++ {
		if _, err := l.Rescan(Include{}); err != nil {
			t.Fatal(err)
		}
	}
	roots, err := db.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if st.removes != 2 || len(roots) != 0 {
		t.Fatal("failed root deletion was not retried")
	}
}

func TestRootSeedKeepsUnobservablePaths(t *testing.T) {
	st := &retryRootStore{Store: openStore(t), extraRoots: []string{"\x00unobservable"}}
	l := loopWith(st)
	for i := 0; i < 2; i++ {
		if _, err := l.Rescan(Include{}); err != nil {
			t.Fatal(err)
		}
	}
	// An invalid path returns an observation error, not proof of nonexistence.
	// Permission and transient I/O failures use this same conservative branch.
	if st.removes != 0 {
		t.Fatal("unobservable root was destructively forgotten")
	}
	if st.reads != 2 {
		t.Fatal("unobservable root was not reconsidered")
	}
}

func TestRootPersistenceMemoBelongsToItsStore(t *testing.T) {
	first := openStore(t)
	second := openStore(t)
	dir := t.TempDir()
	writeConfig(t, dir, "name: persistent\n")
	l := loopWith(first, ports.ListeningPort{Port: 8123, PID: 42, Cwd: dir})
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	l.SetStore(second)
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	roots, err := second.Roots()
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 1 || roots[0] != resolved(t, dir) {
		t.Fatal("old store acknowledgement hid a write to the new store")
	}
}
