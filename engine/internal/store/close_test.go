package store

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// Checkpointing needs the pool down to one connection, and that connection
// must be returned rather than left idle: the sidecars are deleted while the
// store still owns the only connection.
func TestCheckpointLeavesOneConnectionAndNoIdleConnection(t *testing.T) {
	s := openTemp(t)

	s.checkpointAndLeaveWAL()

	stats := s.DB().Stats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
	if stats.Idle != 0 {
		t.Errorf("Idle = %d, want no connection left behind", stats.Idle)
	}
	// A new connection sets WAL again through the DSN, so journal_mode alone
	// cannot say whether the checkpoint ran. The sidecars are the durable
	// result: leaving WAL deletes both, and opening another connection does not
	// bring them back until the next write.
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(s.DBPath() + suffix); !os.IsNotExist(err) {
			t.Errorf("%s still exists after checkpoint: %v", s.DBPath()+suffix, err)
		}
	}
}

// Close must return even when a caller still holds the only connection. A
// leaked connection is a bug, not a reason to hang daemon shutdown or a test's
// temp-directory cleanup forever.
func TestCheckpointGivesUpOnALeakedConnection(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "option-berth.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.DB().Close() })

	held, err := s.DB().Conn(context.Background())
	if err != nil {
		t.Fatalf("holding the only connection: %v", err)
	}
	defer func() { _ = held.Close() }()

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.checkpointAndLeaveWALWithin(50 * time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the checkpoint waited forever on a connection the caller still holds")
	}
}

// The checkpoint runs both pragmas on the one connection it acquired.
func TestCheckpointRunsBothPragmas(t *testing.T) {
	var queries []string
	s := storeWithConnector(t, fixedConn{record: &queries})

	s.checkpointAndLeaveWAL()

	want := []string{"PRAGMA wal_checkpoint(TRUNCATE)", "PRAGMA journal_mode(DELETE)"}
	if !slices.Equal(queries, want) {
		t.Fatalf("pragmas = %q, want %q", queries, want)
	}
}
