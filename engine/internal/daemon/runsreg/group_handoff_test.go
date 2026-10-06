package runsreg

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

func TestGroupReleaseHandoffWaitsWithoutBlockingReads(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		r := New()
		r.Mirror = false
		r.mirrorMu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
		defer cancel()
		done := make(chan error, 1)
		called := false
		go func() {
			n, err := r.WithNoGroupRunsWait(ctx, "demo", "", func() (int, error) {
				called = true
				if r.mirrorMu.TryLock() {
					r.mirrorMu.Unlock()
					t.Error("mutation exclusion lost")
				}
				if _, exists := r.Lookup(424242); exists {
					t.Error("unexpected record")
				}
				return 1, nil
			})
			if err == nil && n != 1 {
				t.Error("release result lost")
			}
			done <- err
		}()
		synctest.Wait() // No wall-clock sleep; the waiter is actually blocked.
		if called {
			t.Fatal("release ran before mirror I/O finished")
		}
		if _, exists := r.Lookup(424242); exists {
			t.Fatal("read unavailable while waiting")
		}
		r.mirrorMu.Unlock()
		if err := <-done; err != nil || !called {
			t.Fatalf("handoff: %v called=%v", err, called)
		}
	})
}

func TestGroupReleaseHandoffRechecksRunsAndCancellation(t *testing.T) {
	for _, mode := range []string{"replacement", "rename", "cancel", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				r := New()
				r.Mirror = false
				r.mirrorMu.Lock()
				ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
				defer cancel()
				done := make(chan error, 1)
				go func() {
					_, err := r.WithNoGroupRunsWait(ctx, "demo", "/fixture/oberth.yaml", func() (int, error) { t.Error("unsafe mutation ran"); return 1, nil })
					done <- err
				}()
				synctest.Wait()
				switch mode {
				case "replacement", "rename":
					rec := Record{PID: 424242, Group: "demo"}
					if mode == "rename" {
						rec.Group = "renamed"
						rec.ConfigPath = "/fixture/oberth.yaml"
					}
					// Mirror exclusion is already held by this simulated writer.
					r.mu.Lock()
					r.runs[rec.PID] = rec
					r.mu.Unlock()
					r.mirrorMu.Unlock()
					if err := <-done; err == nil {
						t.Fatal("new raw run was ignored")
					}
				case "cancel", "deadline":
					if mode == "cancel" {
						cancel()
					} else {
						time.Sleep(250 * time.Millisecond)
					}
					err := <-done
					if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("cancellation not returned: %v", err)
					}
					r.mirrorMu.Unlock()
				}
				if !r.mirrorMu.TryLock() {
					t.Fatal("completed/canceled handoff retained lock or waiter")
				}
				r.mirrorMu.Unlock()
			})
		})
	}
}

func TestGroupReleaseHandoffRequiresBoundAndPropagatesStoreFailure(t *testing.T) {
	r := New()
	r.Mirror = false
	fail := errors.New("store fixture")
	mutate := func() (int, error) { return 0, fail }
	if _, err := r.WithNoGroupRunsWait(context.Background(), "demo", "", mutate); err == nil {
		t.Fatal("unbounded handoff accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.WithNoGroupRunsWait(ctx, "demo", "", mutate); !errors.Is(err, fail) {
		t.Fatalf("store failure suppressed: %v", err)
	}
	if !r.mirrorMu.TryLock() {
		t.Fatal("store failure leaked lock")
	}
	r.mirrorMu.Unlock()
}
