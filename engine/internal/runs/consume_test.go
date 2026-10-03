package runs

import (
	"os"
	"testing"
	"time"
)

func TestConsumeLegacySerializesConcurrentWriter(t *testing.T) {
	t.Setenv("BERTH_HOME", t.TempDir())
	old := Entry{PID: os.Getpid(), ID: "old", StartedAt: "2026-10-02T08:00:00Z"}
	if err := Add(old); err != nil { t.Fatal(err) }
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- ConsumeLegacy(func(entries []Entry) ([]Entry, error) {
			if len(entries) != 1 || entries[0].ID != old.ID { t.Errorf("transaction saw %+v", entries) }
			close(entered)
			<-release
			return nil, nil
		})
	}()
	select { case <-entered: case <-time.After(5*time.Second): t.Fatal("consume callback did not start") }
	writerDone := make(chan error, 1)
	go func() { writerDone <- Add(Entry{PID: os.Getpid(), ID: "new", StartedAt: "2026-10-02T08:00:01Z"}) }()
	select { case err := <-writerDone: t.Fatalf("concurrent writer bypassed consume lock: %v", err); case <-time.After(100*time.Millisecond): }
	close(release)
	if err := <-done; err != nil { t.Fatal(err) }
	if err := <-writerDone; err != nil { t.Fatal(err) }
	got := Load()
	if e, ok := got.LookupByPID(os.Getpid()); !ok || e.ID != "new" { t.Fatalf("concurrent writer was lost: %+v", got.Runs) }
}
