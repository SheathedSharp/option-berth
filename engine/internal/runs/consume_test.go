package runs

import (
	"os"
	"testing"
	"time"
)

func TestConsumeLegacySerializesConcurrentWriter(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	old := Entry{PID: os.Getpid(), ID: "old", StartedAt: "2026-10-02T08:00:00Z"}
	if err := Add(old); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- ConsumeLegacy(func(entries []Entry) ([]Entry, error) {
			if len(entries) != 1 || entries[0].ID != old.ID {
				t.Errorf("transaction saw %+v", entries)
			}
			close(entered)
			<-release
			return nil, nil
		})
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("consume callback did not start")
	}
	writerDone := make(chan error, 1)
	go func() { writerDone <- Add(Entry{PID: os.Getpid(), ID: "new", StartedAt: "2026-10-02T08:00:01Z"}) }()
	select {
	case err := <-writerDone:
		t.Fatalf("concurrent writer bypassed consume lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := <-writerDone; err != nil {
		t.Fatal(err)
	}
	got := Load()
	if e, ok := got.LookupByPID(os.Getpid()); !ok || e.ID != "new" {
		t.Fatalf("concurrent writer was lost: %+v", got.Runs)
	}
}

// The recovery transaction must see the dead entries too: they are the runs
// whose terminal outcome only this transaction can still record. Deleting them
// before the callback would silently lose that outcome.
func TestConsumeLegacyHandsDeadEntriesToTheRecoveryTransaction(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	live := Entry{PID: os.Getpid(), ID: "live", StartedAt: "2026-10-02T08:00:00Z"}
	dead := Entry{PID: 999999999, ID: "dead", StartedAt: "2026-10-02T07:00:00Z"}
	if err := Add(live); err != nil {
		t.Fatal(err)
	}
	if err := Add(dead); err != nil {
		t.Fatal(err)
	}
	var seen []string
	err := ConsumeLegacy(func(entries []Entry) ([]Entry, error) {
		for _, e := range entries {
			seen = append(seen, e.ID)
		}
		return []Entry{live}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != live.ID || seen[1] != dead.ID {
		t.Fatalf("transaction saw %v, want both live and dead entries", seen)
	}
	if _, ok := Load().LookupByPID(dead.PID); ok {
		t.Fatal("the transaction's replacement kept the dead entry")
	}
}
