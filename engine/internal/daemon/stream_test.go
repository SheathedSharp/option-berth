package daemon

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// unregisterHandler takes a method back out of the dispatch table. Only tests
// need it: RegisterHandler panics on a duplicate, so a test that installs a
// probe of its own has to undo that when the probe is done.
func unregisterHandler(method string) {
	handlersMu.Lock()
	delete(handlers, method)
	handlersMu.Unlock()
}

// streamProbe is a streaming method that sends whatever the test pushes into
// its channel, so a test can walk the stream lifecycle one step at a time.
type streamProbe struct {
	writes   chan string
	began    chan *Stream
	finished chan error
}

// probeLine is the payload of one chunk the probe sends.
type probeLine struct {
	Line string `json:"line"`
}

func newStreamProbe() *streamProbe {
	return &streamProbe{
		writes:   make(chan string, 4),
		began:    make(chan *Stream, 1),
		finished: make(chan error, 1),
	}
}

func (p *streamProbe) handler(end any) Handler {
	return func(ctx context.Context, req *Request) (any, error) {
		return StartStream(ctx, req, map[string]any{"source": "probe"},
			func(ctx context.Context, s *Stream) (any, error) {
				p.began <- s
				for {
					select {
					case <-ctx.Done():
						p.finished <- ctx.Err()
						return end, ctx.Err()
					case line, open := <-p.writes:
						if !open {
							p.finished <- nil
							return end, nil
						}
						if err := s.Send(map[string]string{"line": line}); err != nil {
							p.finished <- err
							return end, err
						}
					}
				}
			})
	}
}

// nextNotification reads until a notification carrying the wanted method
// arrives, and hands it back.
func (c *testClient) nextNotification(method string) rpc.Message {
	c.t.Helper()
	for {
		msg := c.read()
		if msg.IsNotification() && msg.Method == method {
			return msg
		}
	}
}

func TestStreamChunkEndAndInitialReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := newStreamProbe()
	RegisterHandler("test.streamChunks", probe.handler(map[string]any{"lines": 2}))
	t.Cleanup(func() { unregisterHandler("test.streamChunks") })

	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var start struct {
		Source         string `json:"source"`
		SubscriptionID string `json:"subscription_id"`
	}
	if failure := cl.call("test.streamChunks", rpc.Empty{}, &start); failure != nil {
		t.Fatalf("starting the stream: %v", failure)
	}
	if start.Source != "probe" {
		t.Fatalf("the initial payload was lost: %+v", start)
	}
	if start.SubscriptionID == "" {
		t.Fatal("the streaming reply carried no subscription_id")
	}

	sent := []string{"one", "two"}
	for _, line := range sent {
		probe.writes <- line
	}

	for _, want := range sent {
		msg := cl.nextNotification(rpc.MethodStreamChunk)
		var chunk rpc.StreamChunk
		if err := json.Unmarshal(msg.Params, &chunk); err != nil {
			t.Fatalf("decoding the chunk: %v", err)
		}
		if chunk.ID != start.SubscriptionID {
			t.Fatalf("chunk id = %q, want %q", chunk.ID, start.SubscriptionID)
		}
		var payload probeLine
		if err := json.Unmarshal(chunk.Data, &payload); err != nil {
			t.Fatalf("decoding the chunk payload: %v", err)
		}
		if payload.Line != want {
			t.Fatalf("chunk line = %q, want %q", payload.Line, want)
		}
	}

	close(probe.writes)

	msg := cl.nextNotification(rpc.MethodStreamEnd)
	var ending rpc.StreamEnd
	if err := json.Unmarshal(msg.Params, &ending); err != nil {
		t.Fatalf("decoding stream.end: %v", err)
	}
	if ending.ID != start.SubscriptionID {
		t.Fatalf("stream.end id = %q, want %q", ending.ID, start.SubscriptionID)
	}
	if ending.Error != nil {
		t.Fatalf("stream.end carried an error: %v", ending.Error)
	}
	if string(ending.Data) != `{"lines":2}` {
		t.Fatalf("stream.end data = %s, want the producer's final payload", ending.Data)
	}
}

func TestStreamCancelStopsTheProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := newStreamProbe()
	RegisterHandler("test.streamCancel", probe.handler(nil))
	t.Cleanup(func() { unregisterHandler("test.streamCancel") })

	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var start rpc.StreamStart
	if failure := cl.call("test.streamCancel", rpc.Empty{}, &start); failure != nil {
		t.Fatalf("starting the stream: %v", failure)
	}
	<-probe.began

	ending := cl.cancelStreamAndAwaitEnd(start.SubscriptionID)

	select {
	case err := <-probe.finished:
		if err == nil {
			t.Fatal("the producer stopped without a cancellation error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelling the stream left the producer running")
	}

	if ending.Error != nil {
		t.Fatalf("a cancelled stream must end cleanly, got %v", ending.Error)
	}

	// The id is gone: a second cancel is a not_found, not another cancel.
	if e := cl.call("stream.cancel", rpc.StreamCancel{ID: start.SubscriptionID}, nil); e == nil {
		t.Fatal("cancelling an ended stream should be not_found")
	}
}

func TestStreamDisconnectCancelsTheProducer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	probe := newStreamProbe()
	RegisterHandler("test.streamDisconnect", probe.handler(nil))
	t.Cleanup(func() { unregisterHandler("test.streamDisconnect") })

	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var start rpc.StreamStart
	if failure := cl.call("test.streamDisconnect", rpc.Empty{}, &start); failure != nil {
		t.Fatalf("starting the stream: %v", failure)
	}
	live := <-probe.began

	cl.conn.Close()

	select {
	case <-probe.finished:
	case <-time.After(2 * time.Second):
		t.Fatal("disconnecting left the producer running")
	}
	if live.Context().Err() == nil {
		t.Fatal("the stream context is still live after a disconnect")
	}
	if live.conn.CancelStream(live.ID()) {
		t.Fatal("the stream is still registered after a disconnect")
	}
}

func TestStreamSendFailsAfterCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)
	hn.srv.subsMu.RLock()
	var conn *Conn
	for _, candidate := range hn.srv.conns {
		conn = candidate
	}
	hn.srv.subsMu.RUnlock()
	_ = cl

	streamCtx, stop := context.WithCancel(ctx)
	s := &Stream{id: "sub-test", conn: conn, ctx: streamCtx, cancel: stop}
	stop()
	if err := s.Send(map[string]string{"line": "x"}); err == nil {
		t.Fatal("Send on a cancelled stream should fail")
	}
}

// TestSubscribeRepliesWithoutWaitingForAScan covers an open follow-up:
// `state.subscribe {include: ["stats"]}` used to rescan before it replied, so
// on a machine where collecting stats is slow the first snapshot turned up
// seconds late. The reply now comes from the cache — stats may be null in it —
// and the tick the subscription wakes carries them in the first delta.
func TestSubscribeRepliesWithoutWaitingForAScan(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	const scanDelay = 700 * time.Millisecond
	hn := &testHarness{t: t}
	hn.rows = []ports.ListeningPort{{
		Port: 8123, PID: 42, Process: "python3", Command: "python3 -m http.server",
	}}
	hn.loop = scanner.New(scanner.Options{
		DaemonVersion: "test",
		Scan: func(inc scanner.Include) ([]ports.ListeningPort, error) {
			time.Sleep(scanDelay)
			hn.mu.Lock()
			defer hn.mu.Unlock()
			rows := append([]ports.ListeningPort{}, hn.rows...)
			if inc.Stats {
				for i := range rows {
					rows[i].CPUPercent, rows[i].MemoryRSS = 12.5, 1<<20
				}
			}
			return rows, nil
		},
	})
	hn.srv = New(Options{Socket: "/test/option-berth.sock", Version: "test", Scanner: hn.loop})
	go hn.loop.Run(ctx)
	cl := hn.dial(ctx)

	started := time.Now()
	var snap state.Snapshot
	if e := cl.call("state.subscribe", rpc.StateSubscribeParams{
		Include: rpc.Include{"stats"},
	}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}
	if took := time.Since(started); took > 100*time.Millisecond {
		t.Fatalf("state.subscribe replied after %v against a %v scan, want under 100ms", took, scanDelay)
	}

	// The woken tick carries the fields the subscriber asked for.
	delta := cl.nextDelta()
	if len(delta.Ports.Added) != 1 {
		t.Fatalf("first delta = %+v, want the port", delta.Ports)
	}
	if stats := delta.Ports.Added[0].Stats; stats == nil || stats.CPUPercent != 12.5 {
		t.Fatalf("first delta carries stats %+v, want the collected ones", stats)
	}
	// Seq semantics are unchanged: the delta continues from the snapshot that
	// the subscribe reply carried.
	if delta.Seq != snap.Seq+1 {
		t.Errorf("delta seq = %d, want the snapshot's %d plus one", delta.Seq, snap.Seq)
	}
}

// A producer may finish before the cancel handler's acknowledgment is queued.
// c.call deliberately skips notifications, so this helper has to collect BOTH
// frames rather than assume a response-before-notification ordering.
func (c *testClient) cancelStreamAndAwaitEnd(subscription string) rpc.StreamEnd {
	c.t.Helper()
	request := c.send("stream.cancel", rpc.StreamCancel{ID: subscription})
	replied, ended := false, false
	var ending rpc.StreamEnd
	for !replied || !ended {
		msg := c.read()
		switch {
		case msg.IsResponse() && string(msg.ID) == request:
			if msg.Error != nil {
				c.t.Fatalf("stream.cancel: %v", msg.Error)
			}
			var ack rpc.OKResult
			if err := json.Unmarshal(msg.Result, &ack); err != nil || !ack.OK {
				c.t.Fatalf("invalid cancellation acknowledgment: %v", err)
			}
			if replied {
				c.t.Fatal("duplicate cancellation acknowledgment")
			}
			replied = true
		case msg.IsNotification() && msg.Method == rpc.MethodStreamEnd:
			var next rpc.StreamEnd
			if err := json.Unmarshal(msg.Params, &next); err != nil {
				c.t.Fatal(err)
			}
			if next.ID != subscription {
				continue
			}
			if ended {
				c.t.Fatal("duplicate stream end")
			}
			ending, ended = next, true
		}
	}
	return ending
}
