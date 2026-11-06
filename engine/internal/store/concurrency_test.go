package store

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
)

// failureLog collects errors from goroutines that cannot call t.Fatalf
// themselves.
type failureLog struct {
	mu   sync.Mutex
	errs []error
}

func (l *failureLog) add(err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.errs = append(l.errs, err)
}

// report fails the test once for each error any goroutine saw.
func (l *failureLog) report(t *testing.T) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, err := range l.errs {
		t.Errorf("concurrent access failed: %v", err)
	}
}

// recordOne writes a single event through the ring's one write path.
func recordOne(s *Store, port, pid int) error {
	return s.AppendBatch([]HistoryEvent{{
		Kind: EventPortUp, Port: port, PID: pid, DisplayName: "api", Group: "shop",
	}})
}

// WAL means a reader never waits behind the writer. This drives both at once
// and fails on the first error either side sees.
func TestReadersDoNotWaitForTheWriter(t *testing.T) {
	s := openTemp(t)
	ctx, cancel := context.WithCancel(context.Background())
	var (
		wg       sync.WaitGroup
		problems failureLog
		mu       sync.Mutex
		reads    int
		writes   int
	)
	const (
		writerIterations = 300
		readers          = 4
	)

	wg.Add(1)
	go func() {
		defer wg.Done()
		defer cancel()
		for i := range writerIterations {
			if err := recordOne(s, 3000+i%5, i); err != nil {
				problems.add(err)
				return
			}
			if err := s.SetGroupAlias(filepath.Join("/code", "shop"), "shop"); err != nil {
				problems.add(err)
				return
			}
			if err := s.AddRoot(filepath.Join("/code", "p")); err != nil {
				problems.add(err)
				return
			}
			mu.Lock()
			writes++
			mu.Unlock()
		}
	}()

	for range readers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if _, err := s.History(HistoryFilter{Limit: 20}); err != nil {
					problems.add(err)
					return
				}
				if _, err := s.GroupAliases(); err != nil {
					problems.add(err)
					return
				}
				if _, err := s.Roots(); err != nil {
					problems.add(err)
					return
				}
				mu.Lock()
				reads++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	problems.report(t)

	if writes != writerIterations {
		t.Errorf("the writer completed %d iterations, want %d", writes, writerIterations)
	}
	if reads == 0 {
		t.Error("not one read completed while the writer was running")
	}
	if count, err := s.HistoryCount(); err != nil || count != writerIterations {
		t.Errorf("the ring holds %d rows (%v), want %d", count, err, writerIterations)
	}
}

// Two Store handles on one file — the daemon plus a one-shot CLI — must neither
// deadlock nor lose writes.
func TestTwoStoresOnOneFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "option-berth.db")
	first, err := Open(path)
	if err != nil {
		t.Fatalf("Open the first: %v", err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := Open(path)
	if err != nil {
		t.Fatalf("Open the second: %v", err)
	}
	t.Cleanup(func() { _ = second.Close() })

	const perStore = 50
	var (
		wg       sync.WaitGroup
		problems failureLog
	)
	for i, s := range []*Store{first, second} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range perStore {
				if err := recordOne(s, 3000+i, j); err != nil {
					problems.add(err)
					return
				}
			}
		}()
	}
	wg.Wait()
	problems.report(t)

	if count, err := first.HistoryCount(); err != nil || count != 2*perStore {
		t.Errorf("the ring holds %d rows (%v), want %d", count, err, 2*perStore)
	}
}
