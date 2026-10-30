package ports

import (
	"context"
	"errors"
	"testing"
)

func TestProbeListenerObservation(t *testing.T) {
	t.Run("sees its own listener", func(t *testing.T) {
		got := probeListenerObservation(context.Background(), func(_ context.Context, port, pid int) ([]ListeningPort, error) {
			return []ListeningPort{{Port: port, PID: pid, BindAddress: "127.0.0.1"}}, nil
		})
		if got.State != ObservationObservable || got.Code != "" {
			t.Fatalf("probe = %+v", got)
		}
	})

	t.Run("bound but the table did not show it", func(t *testing.T) {
		got := probeListenerObservation(context.Background(), func(context.Context, int, int) ([]ListeningPort, error) {
			return nil, nil
		})
		if got.State != ObservationBlind || got.Code != CodeObservationBlind || got.Hint == "" {
			t.Fatalf("probe = %+v", got)
		}
	})

	t.Run("port seen without this process as owner", func(t *testing.T) {
		got := probeListenerObservation(context.Background(), func(_ context.Context, port, pid int) ([]ListeningPort, error) {
			return []ListeningPort{{Port: port, PID: pid + 1}}, nil
		})
		if got.State != ObservationBlind || got.Code != CodeObservationBlind {
			t.Fatalf("probe = %+v", got)
		}
	})

	t.Run("collector failure is blindness, not an empty machine", func(t *testing.T) {
		collector := &ObservationError{Code: CodeObservationEmpty, Detail: "no table"}
		got := probeListenerObservation(context.Background(), func(context.Context, int, int) ([]ListeningPort, error) {
			return nil, collector
		})
		if got.State != ObservationBlind || !errors.Is(got.Err, collector) {
			t.Fatalf("probe = %+v", got)
		}
	})

	t.Run("cancellation stays cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		got := probeListenerObservation(ctx, func(context.Context, int, int) ([]ListeningPort, error) {
			t.Fatal("scan must not run after cancellation")
			return nil, nil
		})
		if !errors.Is(got.Err, context.Canceled) {
			t.Fatalf("probe = %+v", got)
		}
	})
}
