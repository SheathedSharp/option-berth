package runtimesvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// withStore opens the runtime service's database at path the way Serve does and hands
// back the store. Closing it stays the harness's job (testHarness.shutdown):
// the store has to outlive the scan loop that writes history into it, so a
// cleanup registered here would run first and pull the database out from under
// that loop.
func (h *testHarness) withStore(path string) *store.Store {
	h.t.Helper()
	h.srv.opts.DBPath = path
	st, err := store.OpenOwned(path)
	if err != nil {
		h.t.Fatal(err)
	}
	h.srv.setStore(st)
	if st == nil {
		h.t.Fatalf("the runtime service opened no store at %s", path)
	}
	return st
}

func TestStatusReportsTheDatabasePath(t *testing.T) {
	// The database lives in a directory claimed before the harness exists, so
	// that directory's removal is the last cleanup to run.
	dbPath := filepath.Join(t.TempDir(), "option-berth.db")
	h := newHarness(t, t.Context())
	cl := h.dial(t.Context())

	var before rpc.RuntimeServiceStatusResult
	if e := cl.call("runtime.service.status", rpc.Empty{}, &before); e != nil {
		t.Fatalf("runtime.service.status before the store is open: %v", e)
	}
	if before.DBPath != "" {
		t.Errorf("db_path = %q with no store open, want empty", before.DBPath)
	}

	h.withStore(dbPath)

	var after rpc.RuntimeServiceStatusResult
	if e := cl.call("runtime.service.status", rpc.Empty{}, &after); e != nil {
		t.Fatalf("runtime.service.status after the store is open: %v", e)
	}
	if after.DBPath != dbPath {
		t.Errorf("db_path = %q, want %q", after.DBPath, dbPath)
	}
	if h.srv.host.DB == nil {
		t.Error("HostContext.DB is nil; the host hands extensions the owning handle")
	}
}

func TestCorruptDatabaseRefusesOwnershipAndKeepsEvidence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "option-berth.db")
	original := []byte("this is not a SQLite database")
	if err := os.WriteFile(dbPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	if lease, err := acquireStorageLease(dir, dbPath, filepath.Join(dir, "runtime-service.sock"), nil); err == nil {
		_ = lease.Release()
		t.Fatal("started ownership on an unreadable database")
	}
	after, err := os.ReadFile(dbPath)
	if err != nil || string(after) != string(original) {
		t.Fatalf("startup changed corrupt evidence: %q %v", after, err)
	}
	moved, err := filepath.Glob(dbPath + ".corrupt-*")
	if err != nil || len(moved) != 0 {
		t.Fatalf("startup parked/reset database: %v %v", moved, err)
	}
	lock, err := AcquireLock(filepath.Join(dir, "storage.lock"))
	if err != nil {
		t.Fatalf("failed startup leaked storage guard: %v", err)
	}
	_ = lock.Release()
}

// waitForEvent reads notifications until a state.event of the named kind
// arrives.
func (c *testClient) waitForEvent(t *testing.T, kind string) state.Event {
	t.Helper()
	for range 50 {
		msg := c.read()
		if !msg.IsNotification() || msg.Method != rpc.MethodStateEvent {
			continue
		}
		var event state.Event
		if err := json.Unmarshal(msg.Params, &event); err != nil {
			t.Fatalf("decoding the state.event: %v", err)
		}
		if event.Kind == kind {
			return event
		}
	}
	t.Fatalf("no state.event of kind %q arrived", kind)
	return state.Event{}
}
