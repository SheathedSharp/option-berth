package runtimesvc

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
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
	h.srv.openStore()
	st := h.srv.runtime.Store
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
	if h.srv.runtime.DB == nil {
		t.Error("Runtime.DB is nil; the runtime hands extensions the raw handle")
	}
}

func TestCorruptDatabaseIsRecreatedAndAnnounced(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "option-berth.db")
	if err := os.WriteFile(dbPath, []byte("this is not a SQLite database"), 0o600); err != nil {
		t.Fatal(err)
	}

	h := newHarness(t, t.Context())
	cl := h.dial(t.Context())
	if e := cl.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, nil); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	h.withStore(dbPath)

	// A change gives the queued event something to ride out on.
	h.setRows(ports.ListeningPort{Port: 8123, PID: 42, Process: "python3"})
	h.loop.Wake()

	ev := cl.waitForEvent(t, "db_reset")
	if ev.Data["moved_to"] == nil {
		t.Errorf("db_reset reported no moved_to: %+v", ev.Data)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	movedAside := ""
	for _, entry := range entries {
		if rest, ok := strings.CutPrefix(entry.Name(), "option-berth.db.corrupt-"); ok && rest != "" {
			movedAside = entry.Name()
		}
	}
	if movedAside == "" {
		t.Errorf("the unusable database was not moved aside: %v", entries)
	}
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
