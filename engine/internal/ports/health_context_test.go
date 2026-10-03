package ports

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// The fallback channel makes cleanup safe even when a cancellation assertion
// fails: the fixture never leaves its HTTP handlers or sockets behind.
func cancellationServer(t *testing.T, handler func(http.ResponseWriter, *http.Request, <-chan struct{})) (*httptest.Server, int) {
	t.Helper()
	stop := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler(w, r, stop) }))
	t.Cleanup(func() { close(stop); srv.Close() })
	_, rawPort, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil {
		t.Fatal(err)
	}
	return srv, port
}

func TestProbeHealthContextPreCancelled(t *testing.T) {
	var calls atomic.Int32
	_, port := cancellationServer(t, func(http.ResponseWriter, *http.Request, <-chan struct{}) { calls.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := ProbeHealthContext(ctx, "127.0.0.1", port, "/", time.Second)
	if got != (HealthResult{}) || calls.Load() != 0 {
		t.Fatalf("result=%+v requests=%d", got, calls.Load())
	}
}

func TestProbeHealthContextCancelsInFlight(t *testing.T) {
	entered, exited := make(chan struct{}, 1), make(chan struct{}, 1)
	_, port := cancellationServer(t, func(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) {
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			exited <- struct{}{}
		case <-stop:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan HealthResult, 1)
	go func() { done <- ProbeHealthContext(ctx, "127.0.0.1", port, "/", 10*time.Second) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("probe did not reach fixture")
	}
	cancel()
	select {
	case got := <-done:
		if got != (HealthResult{}) {
			t.Fatalf("cancellation became verdict: %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("probe did not cancel")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not observe request cancellation")
	}
}

func TestEnrichHealthContextStopsAdmissionAndJoins(t *testing.T) {
	entered, exited := make(chan struct{}, MaxProbes*4), make(chan struct{}, MaxProbes*4)
	var calls atomic.Int32
	_, port := cancellationServer(t, func(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-r.Context().Done():
			exited <- struct{}{}
		case <-stop:
		}
	})
	pp := make([]ListeningPort, MaxProbes*4)
	for i := range pp {
		pp[i] = ListeningPort{Port: port, BindAddress: "127.0.0.1", HealthStatus: "previous"}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { EnrichHealthContext(ctx, pp, 10*time.Second, 0); close(done) }()
	for range MaxProbes {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("first admission wave did not start")
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("enrichment did not join probes")
	}
	if got := calls.Load(); got != MaxProbes {
		t.Fatalf("requests=%d, want first wave %d", got, MaxProbes)
	}
	for range MaxProbes {
		select {
		case <-exited:
		case <-time.After(2 * time.Second):
			t.Fatal("owned handler still active")
		}
	}
	for i, p := range pp {
		if p.HealthStatus != "previous" || p.HealthObservedAt != "" {
			t.Fatalf("row %d changed on cancellation: %+v", i, p)
		}
	}
}

func TestProbeHealthContextTimeoutStillHasVerdict(t *testing.T) {
	_, port := cancellationServer(t, func(w http.ResponseWriter, r *http.Request, stop <-chan struct{}) {
		select {
		case <-r.Context().Done():
		case <-stop:
		}
	})
	got := ProbeHealthContext(context.Background(), "127.0.0.1", port, "/", 20*time.Millisecond)
	if got.Status != "timeout" || got.ObservedAt.IsZero() {
		t.Fatalf("probe timeout lost verdict: %+v", got)
	}
}
