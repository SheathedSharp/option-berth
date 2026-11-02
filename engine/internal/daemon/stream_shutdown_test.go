package daemon

import (
	"context"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// A stream producer runs on its own goroutine after the handler has returned,
// so no connection waitgroup covers it. Shutdown drains connections, waits,
// and only then closes the store. A producer still inside a start or a prepare
// at that moment would write run rows to a closed database and lose them
// silently, so the wait has to include producers.
func TestShutdownWaitCoversStreamProducers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	producerRunning := make(chan struct{})
	release := make(chan struct{})
	RegisterHandler("test.longStream", func(ctx context.Context, req *Request) (any, error) {
		return StartStream(ctx, req, map[string]any{"source": "long"},
			func(ctx context.Context, s *Stream) (any, error) {
				close(producerRunning)
				<-release
				return nil, ctx.Err()
			})
	})
	t.Cleanup(func() { unregisterHandler("test.longStream") })

	h := newHarness(t, ctx)
	c := h.dial(ctx)
	if e := c.call("test.longStream", rpc.Empty{}, nil); e != nil {
		t.Fatalf("starting stream: %v", e)
	}
	select {
	case <-producerRunning:
	case <-time.After(5 * time.Second):
		t.Fatal("producer never started")
	}

	// What Serve does on the way out before it closes the store.
	h.srv.closeAllConns("test over")
	waited := make(chan struct{})
	go func() {
		defer close(waited)
		h.srv.wg.Wait()
		h.srv.waitProducers()
	}()

	select {
	case <-waited:
		t.Fatal("the shutdown wait returned while a stream producer was still running")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("the shutdown wait did not finish after the producer returned")
	}
}
