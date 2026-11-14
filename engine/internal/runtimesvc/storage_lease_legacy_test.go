package runtimesvc

import (
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The legacy owner is represented by its real advisory-lock path and the
// unchanged production store.Open implementation, not by a started service.
// Everything, including the SQLite database, is private to t.TempDir. No
// listener, child process, scan, model, or user state is involved.
func TestStorageLeaseRejectsUnknownLegacyEndpointWriter(t *testing.T) {
	stateDir := t.TempDir()
	dbPath := filepath.Join(stateDir, "option-berth.db")
	legacyEndpointDir := t.TempDir() // not the state, DB or new endpoint directory
	legacyLock, err := AcquireLock(filepath.Join(legacyEndpointDir, "daemon.lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer legacyLock.Release()
	legacyStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	// Close raw SQL handles: no need to switch journal modes while another
	// synthetic owner still has the same disposable database open.
	defer legacyStore.DB().Close()
	if _, err := legacyStore.DB().Exec(`CREATE TABLE ownership_probe (owner TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	lease, err := acquireStorageLease(stateDir, dbPath, filepath.Join(t.TempDir(), "runtime-service.sock"), nil)
	if err != nil {
		return
	} // a valid existing writer must be refused
	defer lease.Release()
	candidateStore, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer candidateStore.DB().Close()
	for _, writer := range []struct {
		name string
		db   *store.Store
	}{{"legacy", legacyStore}, {"candidate", candidateStore}} {
		if _, err := writer.db.DB().Exec(`INSERT INTO ownership_probe(owner) VALUES (?)`, writer.name); err != nil {
			t.Fatalf("%s production store write: %v", writer.name, err)
		}
	}
	var count int
	if err := candidateStore.DB().QueryRow(`SELECT COUNT(DISTINCT owner) FROM ownership_probe`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("bad counterexample fixture: owners=%d", count)
	}
	t.Fatalf("unsafe storage ownership: legacy external-endpoint lock stayed held, candidate lease succeeded, and both production Store handles wrote the same private SQLite DB (distinct owners=%d)", count)
}
