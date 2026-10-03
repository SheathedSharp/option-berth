package runsreg

import (
	"bytes"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/runs"
)

func importFixture(t *testing.T) (*Registry, runs.Entry, []byte) {
	t.Helper()
	t.Setenv("BERTH_HOME", t.TempDir())
	entry := runs.Entry{PID: os.Getpid(), ID: "recovered", Group: "fixture", Name: "api",
		StartedAt: time.Now().Format(time.RFC3339Nano)}
	if err := runs.Add(entry); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(runs.Path())
	if err != nil {
		t.Fatal(err)
	}
	return New(), entry, before
}

func TestImportLegacyReportsBusyAndRecovers(t *testing.T) {
	r, entry, before := importFixture(t)
	// Fresh dead-owner sidecar: the compatibility protocol must wait instead
	// of treating a failed import as a successful empty registry.
	lock := runs.Path() + ".lock"
	if err := os.WriteFile(lock, []byte("999999999\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	n, err := r.ImportLegacy()
	if err == nil || n != 0 {
		t.Fatalf("busy import = %d/%v, want an explicit failure", n, err)
	}
	if _, ok := r.Lookup(entry.PID); ok {
		t.Fatal("failed import changed memory")
	}
	after, readErr := os.ReadFile(runs.Path())
	if readErr != nil || !bytes.Equal(before, after) {
		t.Fatal("busy import changed evidence")
	}
	if err := os.Remove(lock); err != nil {
		t.Fatal(err)
	}
	if n, err := r.ImportLegacy(); err != nil || n != 1 {
		t.Fatalf("recovery after failure = %d/%v", n, err)
	}
	if rec, ok := r.Lookup(entry.PID); !ok || rec.ID != entry.ID {
		t.Fatalf("successful recovery = %+v, %v", rec, ok)
	}
}

func TestImportLegacyPublishesOnlyAfterFileCommit(t *testing.T) {
	r, entry, before := importFixture(t)
	path, saved := runs.Path(), runs.Path()+".fixture"
	n, err := r.importLegacy(func(transform func([]runs.Entry) ([]runs.Entry, error)) error {
		return runs.ConsumeLegacy(func(entries []runs.Entry) ([]runs.Entry, error) {
			next, err := transform(entries)
			if err != nil {
				return nil, err
			}
			if _, visible := r.Lookup(entry.PID); visible {
				t.Fatal("uncommitted import visible to a reader")
			}
			// A real rename failure at the existing file transaction boundary,
			// not an error fabricated after an already successful commit.
			if err := os.Rename(path, saved); err != nil {
				return nil, err
			}
			if err := os.Mkdir(path, 0o700); err != nil {
				return nil, err
			}
			return next, nil
		})
	})
	if err == nil || n != 0 {
		t.Fatalf("commit failure = %d/%v", n, err)
	}
	if _, ok := r.Lookup(entry.PID); ok {
		t.Fatal("failed file commit published imported memory")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(saved, path); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, before) {
		t.Fatal("original evidence was not preserved by the fixture")
	}
	if n, err := r.ImportLegacy(); err != nil || n != 1 {
		t.Fatalf("retry after failed commit = %d/%v", n, err)
	}
}

func TestImportLegacyPreservesLiveRegistrationAndStopping(t *testing.T) {
	r, old, _ := importFixture(t)
	r.Mirror = false
	newer := r.Register(Record{PID: old.PID, ID: "current", Group: "current", Name: "api"})
	r.Mirror = true
	// Stopping is a serialized mutation now, not a callback that may re-enter
	// import's mirrorMu. Preserve existing flags and separately queue a real
	// concurrent mutation behind the import, rather than deadlocking the test.
	r.Stopping([]int{newer.PID})
	stopped := make(chan struct{})
	n, err := r.importLegacy(func(transform func([]runs.Entry) ([]runs.Entry, error)) error {
		return runs.ConsumeLegacy(func(entries []runs.Entry) ([]runs.Entry, error) {
			next, err := transform(entries)
			go func() { r.StoppingWithReason([]int{newer.PID}, ReasonReadyTimeout); close(stopped) }()
			if r.mirrorMu.TryLock() {
				r.mirrorMu.Unlock()
				t.Error("import released mutation lock before commit")
			}
			return next, err
		})
	})
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("queued stopping did not complete after import")
	}
	if err != nil || n != 0 {
		t.Fatalf("duplicate import = %d/%v", n, err)
	}
	rec, ok := r.Lookup(newer.PID)
	if !ok || rec.ID != newer.ID || !rec.stopping || rec.stoppingReason != ReasonReadyTimeout {
		t.Fatalf("recovery overwrote live state: %+v", rec)
	}
	disk, ok := runs.Load().LookupByPID(newer.PID)
	if !ok || disk.ID != newer.ID {
		t.Fatalf("disk did not preserve authoritative run: %+v", disk)
	}
}

func TestImportLegacyMutationOrderingAndReadableMemory(t *testing.T) {
	r, old, _ := importFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	finished := make(chan struct{})
	var registered chan struct{}
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	go func() {
		defer close(finished)
		_, err := r.importLegacy(func(transform func([]runs.Entry) ([]runs.Entry, error)) error {
			return runs.ConsumeLegacy(func(entries []runs.Entry) ([]runs.Entry, error) {
				next, err := transform(entries)
				close(entered)
				<-release
				return next, err
			})
		})
		done <- err
	}()
	t.Cleanup(func() {
		unblock()
		<-finished
		if registered != nil {
			<-registered
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("recovery transaction did not enter")
	}
	read := make(chan bool, 1)
	go func() { _, ok := r.Lookup(old.PID); read <- ok }()
	select {
	case visible := <-read:
		if visible {
			t.Fatal("staged import visible")
		}
	case <-time.After(time.Second):
		t.Fatal("reader queued behind disk transaction")
	}
	registered = make(chan struct{})
	go func() {
		r.Register(Record{PID: old.PID, ID: "replacement", Group: "fixture", Name: "api"})
		close(registered)
	}()
	// The actual barrier for the writer is mirrorMu; it must remain held
	// while the file transaction is paused, without blocking the read above.
	if r.mirrorMu.TryLock() {
		r.mirrorMu.Unlock()
		t.Fatal("import released mutation ordering before commit")
	}
	unblock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	<-registered
	rec, ok := r.Lookup(old.PID)
	disk, found := runs.Load().LookupByPID(old.PID)
	if !ok || !found || rec.ID != "replacement" || disk.ID != rec.ID {
		t.Fatalf("memory/disk diverged: %+v / %+v", rec, disk)
	}
}

func TestImportLegacyPreservesErrorIdentity(t *testing.T) {
	r := New()
	want := errors.New("transaction failed")
	n, err := r.importLegacy(func(func([]runs.Entry) ([]runs.Entry, error)) error { return want })
	if n != 0 || !errors.Is(err, want) {
		t.Fatalf("recovery lost original error: %d/%v", n, err)
	}
}
