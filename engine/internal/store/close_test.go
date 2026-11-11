package store

import (
	"slices"
	"testing"
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
