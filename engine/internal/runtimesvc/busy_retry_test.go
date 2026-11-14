package runtimesvc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// A store contention is retried rather than reported: the hold is one history
// insert long, and `down` failing on it left port reservations behind.
func TestRetryingBusyRetriesAContention(t *testing.T) {
	calls := 0
	err := retryingBusy(context.Background(), func() error {
		calls++
		if calls < 3 {
			return store.ErrClaimsBusy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retryingBusy = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("work ran %d times, want 3", calls)
	}
}

// Any other failure is reported at once, not retried.
func TestRetryingBusyPassesOtherFailuresThrough(t *testing.T) {
	boom := errors.New("disk on fire")
	calls := 0
	err := retryingBusy(context.Background(), func() error {
		calls++
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("retryingBusy = %v, want the original failure", err)
	}
	if calls != 1 {
		t.Fatalf("work ran %d times, want 1", calls)
	}
}

// A writer that never lets go ends the call at the caller's deadline instead of
// holding it open, and the reason names the cancellation.
func TestRetryingBusyStopsAtTheDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := retryingBusy(ctx, func() error { return store.ErrClaimsBusy })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("retryingBusy = %v, want the deadline", err)
	}
}
