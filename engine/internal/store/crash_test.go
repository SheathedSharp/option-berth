package store

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The crash-recovery pair. The child keeps committing batches of history and
// is killed without closing the database; the parent reopens the same file and
// checks what survived. SQLite's WAL is what makes this safe, and the rules
// this pins are the storage module's acceptance: a crash must not corrupt the
// database, every committed row must be there, and a transaction must never be
// visible half-applied.

const (
	crashChildEnv      = "BERTH_STORE_CRASH_CHILD"
	crashChildReadyEnv = "BERTH_STORE_CRASH_CHILD_READY"
	// crashBatchSize is how many events one transaction carries; every visible
	// batch must be this size in full.
	crashBatchSize = 5
	// crashBatches is how many batches the child commits before parking,
	// comfortably below the history ring's 10 000-row cap so no eviction
	// breaks the whole-batch invariant.
	crashBatches = 300
	// crashSentinelPort marks the batch committed before the ready file, so
	// the parent knows at least this one transaction must survive.
	crashSentinelPort = 60001
	// crashBatchBase namespaces the batch ports.
	crashBatchBase = 62000
)

// TestCrashRecoveryChild is the child half: it commits one sentinel batch,
// signals readiness, then commits batches until it is killed. It only acts
// when the parent sets the environment; in a normal run it skips.
func TestCrashRecoveryChild(t *testing.T) {
	dbPath := os.Getenv(crashChildEnv)
	if dbPath == "" {
		t.Skip("not the crash child")
	}
	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("child open: %v", err)
	}
	defer st.Close()

	base := time.Now().UTC()
	if err := st.AppendBatch([]HistoryEvent{{
		At: base, Kind: EventPortUp, Port: crashSentinelPort, PID: 7001, DisplayName: "sentinel",
	}}); err != nil {
		t.Fatalf("child sentinel batch: %v", err)
	}
	if ready := os.Getenv(crashChildReadyEnv); ready != "" {
		if err := os.WriteFile(ready, []byte("ready"), 0o600); err != nil {
			t.Fatalf("child ready marker: %v", err)
		}
	}

	for i := 0; i < crashBatches; i++ {
		batch := make([]HistoryEvent, crashBatchSize)
		for j := range batch {
			batch[j] = HistoryEvent{
				At:   base.Add(time.Duration(i*crashBatchSize+j) * time.Millisecond),
				Kind: EventPortUp, Port: crashBatchBase + i, PID: 8000 + i, DisplayName: "worker",
			}
		}
		if err := st.AppendBatch(batch); err != nil {
			t.Fatalf("child batch %d: %v", i, err)
		}
	}
	// Park with the database open: the parent's kill is the crash. A bare
	// select{} would deadlock-panic the runtime before the parent gets there.
	for {
		time.Sleep(time.Hour)
	}
}

// TestCrashRecoveryKeepsWholeCommittedBatches kills a writer without Close and
// reopens the file. The database must open cleanly (no corruption), the
// sentinel committed before the crash must be there, and every batch the file
// shows must be complete — a torn transaction would show a partial batch.
func TestCrashRecoveryKeepsWholeCommittedBatches(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "option-berth.db")
	ready := filepath.Join(dir, "ready")

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashRecoveryChild$", "-test.timeout=120s")
	cmd.Env = append(os.Environ(),
		crashChildEnv+"="+dbPath,
		crashChildReadyEnv+"="+ready,
	)
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("start the crash child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never became ready; output:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Wait until the child is demonstrably mid-stream — several committed
	// batches, read through the WAL while it keeps writing — so the kill
	// lands during the write loop rather than right after the sentinel.
	deadline = time.Now().Add(10 * time.Second)
	for {
		if ro, err := OpenReadOnly(dbPath); err == nil {
			n, cerr := ro.HistoryCount()
			_ = ro.Close()
			if cerr == nil && n >= 1+3*crashBatchSize {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never accumulated batches; output:\n%s", out.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill the child: %v", err)
	}
	_, _ = cmd.Process.Wait()

	st, err := Open(dbPath)
	if err != nil {
		t.Fatalf("reopen after the crash: %v; child output:\n%s", err, out.String())
	}
	defer st.Close()

	rows, err := st.Query(nil, time.Time{}, crashBatches*crashBatchSize+10)
	if err != nil {
		t.Fatalf("query after the crash: %v", err)
	}

	perPort := map[int]int{}
	sentinel := 0
	for _, row := range rows {
		switch {
		case row.Port == crashSentinelPort:
			sentinel++
		case row.Port >= crashBatchBase:
			perPort[row.Port]++
		}
	}
	if sentinel != 1 {
		t.Fatalf("sentinel batch rows = %d, want 1: committed data did not survive the crash", sentinel)
	}
	if len(perPort) < 3 {
		t.Fatalf("batch rows survived = %d, want at least the three read before the kill; child output:\n%s",
			len(perPort), out.String())
	}
	for port, n := range perPort {
		if n != crashBatchSize {
			t.Fatalf("port %d shows %d/%d rows: a transaction became visible half-applied", port, n, crashBatchSize)
		}
	}
}
