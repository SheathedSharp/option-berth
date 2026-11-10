package store

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
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
		reads    atomic.Int64
		writes   atomic.Int64
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
			writes.Add(1)
		}
	}()

	startReaders(ctx, &wg, readers, func() error {
		if _, err := s.History(HistoryFilter{Limit: 20}); err != nil {
			return err
		}
		if _, err := s.GroupAliases(); err != nil {
			return err
		}
		if _, err := s.Roots(); err != nil {
			return err
		}
		reads.Add(1)
		return nil
	}, &problems)
	wg.Wait()
	problems.report(t)

	if got := writes.Load(); got != writerIterations {
		t.Errorf("the writer completed %d iterations, want %d", got, writerIterations)
	}
	if reads.Load() == 0 {
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

// startReaders keeps issuing read until the context is cancelled. Any error
// stops that reader and is collected for report.
func startReaders(ctx context.Context, wg *sync.WaitGroup, n int, read func() error, problems *failureLog) {
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				if err := read(); err != nil {
					problems.add(err)
					return
				}
			}
		}()
	}
}
