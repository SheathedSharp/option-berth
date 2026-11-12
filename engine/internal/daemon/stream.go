package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// streamSeq numbers subscriptions for the process's whole life. Ids are unique
// across connections, so a log line can name one without naming a client too.
var streamSeq atomic.Uint64

// nextStreamID allocates the id carried by a stream's chunks.
func nextStreamID() string {
	n := streamSeq.Add(1)
	return fmt.Sprintf("sub-%d", n)
}

// Stream is a running streaming method: the daemon answers the call with a
// subscription id, pushes `stream.chunk {id, data}` notifications, and closes
// with `stream.end {id, data?, error?}`.
//
// A stream ends when its producer returns, when the client sends
// `stream.cancel {id}`, or when the connection drops — the latter two both
// cancel the producer's context, so honouring ctx is all a producer needs.
type Stream struct {
	conn *Conn
	id   string

	cancel context.CancelFunc
	ctx    context.Context

	once sync.Once
}

// ID is the subscription id handed to the client.
func (s *Stream) ID() string { return s.id }

// Context is cancelled by stream.cancel, by the client disconnecting and by a
// daemon shutdown. Producers must select on it.
func (s *Stream) Context() context.Context { return s.ctx }

// Done is closed once the stream is cancelled.
func (s *Stream) Done() <-chan struct{} { return s.ctx.Done() }

// Send pushes one chunk. Once the stream is over it reports the cancellation
// cause, so a producer that ignores Context still stops at the first Send.
func (s *Stream) Send(data any) error {
	err := s.ctx.Err()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("marshalling stream chunk: %w", err)
	}
	return s.conn.Notify(rpc.MethodStreamChunk, rpc.StreamChunk{ID: s.id, Data: encoded})
}

// finish deregisters the stream and sends stream.end exactly once. A cancelled
// stream still gets its end notification, without an error: the client asked
// for the cancellation, so it is not a failure.
func (s *Stream) finish(data any, err error) {
	s.once.Do(func() { s.sendEnd(data, err) })
}

func (s *Stream) sendEnd(data any, failure error) {
	s.conn.CancelStream(s.id)
	ending := rpc.StreamEnd{ID: s.id}
	if data != nil {
		if encoded, mErr := json.Marshal(data); mErr == nil {
			ending.Data = encoded
		}
	}
	if failure != nil && !errors.Is(failure, context.Canceled) {
		ending.Error = errorFor(failure)
	}
	_ = s.conn.Notify(rpc.MethodStreamEnd, ending)
}

// StreamFunc produces a stream's chunks. Its returned payload becomes
// stream.end's body (nil for a stream with no final result), and its error
// becomes stream.end's error object. Returning ctx.Err() after a cancellation
// is not an error: it is reported as a clean end.
type StreamFunc func(ctx context.Context, s *Stream) (any, error)

// StartStream turns a handler into a streaming method. It allocates a
// subscription id, replies to the call with `initial` plus that id, registers
// the stream so stream.cancel and a disconnect can stop it, and finally runs
// produce on its own goroutine.
//
// initial has to marshal to a JSON object; nil stands for a bare
// {subscription_id} reply. The reply is queued before produce starts, so no
// chunk can overtake the id it belongs to.
//
// A handler returns its two values directly: the error is always
// ErrResponseSent.
func StartStream(ctx context.Context, req *Request, initial any, produce StreamFunc) (any, error) {
	id := nextStreamID()
	reply, err := withSubscriptionID(initial, id)
	if err != nil {
		return nil, err
	}

	sctx, cancel := context.WithCancel(ctx)
	s := &Stream{conn: req.Conn, id: id, ctx: sctx, cancel: cancel}

	req.Conn.RegisterStream(id, cancel)
	req.Conn.replyResult(req.ID, reply)

	// A producer outlives the handler that started it and the connection entry
	// removeConn deletes, so it is counted on the server: it writes run and exit
	// facts, and the store closes as soon as that wait returns.
	srv := req.Conn.srv
	if srv != nil {
		srv.producers.Add(1)
	}
	go func() {
		if srv != nil {
			defer srv.producers.Done()
		}
		defer cancel()
		payload, produceErr := produce(sctx, s)
		s.finish(payload, produceErr)
	}()
	return nil, ErrResponseSent
}

// withSubscriptionID merges the subscription id into a streaming method's
// initial result. An initial payload that is not an object is a handler's
// programming error — no client can provoke it — so it surfaces as an internal
// error instead of invalid params.
func withSubscriptionID(initial any, id string) (json.RawMessage, error) {
	if initial == nil {
		return json.Marshal(rpc.StreamStart{SubscriptionID: id})
	}
	encoded, err := json.Marshal(initial)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal,
			"marshalling stream reply: "+err.Error(), "")
	}
	object := map[string]json.RawMessage{}
	err = json.Unmarshal(encoded, &object)
	if err != nil {
		return nil, rpc.NewError(rpc.CodeInternal,
			fmt.Sprintf("a streaming method must reply with a JSON object, got %s", encoded), "")
	}
	object["subscription_id"], _ = json.Marshal(id)
	return json.Marshal(object)
}
