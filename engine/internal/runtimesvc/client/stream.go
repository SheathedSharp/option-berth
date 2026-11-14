package client

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// StreamBuffer is how many chunks a stream holds before its reader must catch
// up. A full buffer is not discarded the way a state subscription's is: log
// lines and readiness events cannot be resent, so the reader is made to wait.
const StreamBuffer = 256

// End is a stream's final payload and, when it failed, the runtime service's error.
type End struct {
	// Data is stream.end's payload; nil for a method with no final result.
	Data json.RawMessage
	// Err is the runtime service's error, when the stream ended in failure.
	Err *rpc.Error
}

// Decode unmarshals the end payload into out.
func (end End) Decode(out any) error {
	if len(end.Data) == 0 {
		return nil
	}
	return json.Unmarshal(end.Data, out)
}

// Stream is the client half of the streaming convention: the subscription id
// the runtime service assigned, the chunks that follow, and the end.
type Stream struct {
	client *Client
	id     string

	chunks chan json.RawMessage
	end    chan End

	// Pump coordination.
	nudge     chan struct{}
	abandoned chan struct{}
	closeOnce sync.Once

	mu       sync.Mutex
	buffered []json.RawMessage
	finished bool
	final    End

	// handedOver is guarded by the client's mutex: it belongs to the registry,
	// not to the stream itself.
	handedOver bool
}

func (s *Stream) finishedAlready() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished
}

// ID is the subscription id the runtime service assigned.
func (s *Stream) ID() string { return s.id }

// Chunks carries every stream.chunk payload and closes when the stream ends.
func (s *Stream) Chunks() <-chan json.RawMessage { return s.chunks }

// End carries the single stream.end, then closes. It also closes when the
// connection drops without one.
func (s *Stream) End() <-chan End { return s.end }

// Cancel asks the runtime service to stop the stream. The runtime service still sends a final
// stream.end, so a draining caller keeps reading until End closes.
func (s *Stream) Cancel(ctx context.Context) error {
	err := s.client.Call(ctx, rpc.MethodStreamCancel, rpc.StreamCancel{ID: s.id}, nil)
	// A stream that already ended on its own is not a failure to cancel.
	var rpcErr *rpc.Error
	if errors.As(err, &rpcErr) && rpcErr.Code == rpc.CodeNotFound {
		return nil
	}
	return err
}

// Stream calls a streaming method, decodes the reply into result (which may be
// nil) and returns the Stream carrying the chunks that follow. The runtime service
// queues the reply before the first chunk, and chunks that arrive while the
// reply is being decoded wait under the subscription id, so none are lost.
func (c *Client) Stream(ctx context.Context, method string, params, result any) (*Stream, error) {
	raw, stream, err := c.CallStream(ctx, method, params)
	if err != nil {
		return nil, err
	}
	if stream == nil {
		return nil, errors.New("runtime_service: " + method + " did not return a subscription_id")
	}
	if result != nil {
		if err := json.Unmarshal(raw, result); err != nil {
			return nil, err
		}
	}
	return stream, nil
}

// CallStream calls a method without knowing whether it streams. It returns the
// raw reply and, when that reply named a subscription id, the stream its
// chunks arrive on; a plain method answers with a nil stream.
//
// The stream is registered before the reply is inspected, so a chunk that
// overtakes the caller is not lost: claiming the id picks up whatever the read
// loop already buffered under it.
func (c *Client) CallStream(ctx context.Context, method string, params any) (json.RawMessage, *Stream, error) {
	var raw json.RawMessage
	if err := c.Call(ctx, method, params, &raw); err != nil {
		return nil, nil, err
	}

	var reply struct {
		SubscriptionID string `json:"subscription_id"`
	}
	// A reply that is not an object cannot be a stream reply, which is normal:
	// plenty of methods answer with a bare value.
	if err := json.Unmarshal(raw, &reply); err != nil || reply.SubscriptionID == "" {
		return raw, nil, nil
	}

	stream := c.streamWithID(reply.SubscriptionID)
	stream.client, stream.id = c, reply.SubscriptionID
	c.handOver(reply.SubscriptionID)
	return raw, stream, nil
}

// handOver marks a stream as given to its caller. A stream that has already
// ended can now leave the registry; one that has not stays, so the chunks still
// to come can find it.
func (c *Client) handOver(id string) {
	c.mu.Lock()
	if stream, exists := c.streams[id]; exists {
		stream.handedOver = true
	}
	c.mu.Unlock()
	c.releaseIfDone(id)
}

// releaseIfDone forgets a stream once it has both ended and been handed over.
//
// Both halves matter. Releasing on the end alone loses a stream that finished
// before its own reply was decoded (`groups.start` on an already-running group
// answers in microseconds), and the caller would receive a fresh empty stream
// that never ends. Releasing on the handover alone would orphan chunks still
// in flight.
func (c *Client) releaseIfDone(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stream, exists := c.streams[id]
	if exists && stream.handedOver && stream.finishedAlready() {
		delete(c.streams, id)
	}
}

// streamWithID returns the stream registered under id, creating it when the
// first notification has not arrived yet — or arrived before the reply was
// decoded.
func (c *Client) streamWithID(id string) *Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.streams == nil {
		c.streams = map[string]*Stream{}
	}
	if stream, exists := c.streams[id]; exists {
		return stream
	}
	stream := &Stream{
		id:        id,
		chunks:    make(chan json.RawMessage, StreamBuffer),
		end:       make(chan End, 1),
		nudge:     make(chan struct{}, 1),
		abandoned: make(chan struct{}),
	}
	c.streams[id] = stream
	go stream.drain()
	return stream
}

// dispatchStream routes one stream.chunk or stream.end notification and
// reports whether the message belonged to a stream at all.
func (c *Client) dispatchStream(msg rpc.Message) bool {
	switch msg.Method {
	case rpc.MethodStreamChunk:
		return c.acceptChunk(msg.Params)
	case rpc.MethodStreamEnd:
		return c.acceptEnd(msg.Params)
	}
	return false
}

func (c *Client) acceptChunk(raw json.RawMessage) bool {
	var chunk rpc.StreamChunk
	if err := json.Unmarshal(raw, &chunk); err != nil || chunk.ID == "" {
		return true
	}
	c.streamWithID(chunk.ID).bufferChunk(chunk.Data)
	return true
}

func (c *Client) acceptEnd(raw json.RawMessage) bool {
	var end rpc.StreamEnd
	if err := json.Unmarshal(raw, &end); err != nil || end.ID == "" {
		return true
	}
	stream := c.streamWithID(end.ID)
	stream.conclude(End{Data: end.Data, Err: end.Error})
	c.releaseIfDone(end.ID)
	return true
}

// bufferChunk queues one chunk. It never blocks the connection's read loop: a
// slow consumer would otherwise stall every other call on the socket,
// including the stream.cancel that could free it. Chunks are queued rather
// than dropped because a log line the runtime service already read cannot be resent.
func (s *Stream) bufferChunk(data json.RawMessage) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.buffered = append(s.buffered, data)
	s.mu.Unlock()
	s.nudgePump()
}

// conclude records the end. The pump delivers it after the buffer drains, so a
// chunk is never lost to a fast end.
func (s *Stream) conclude(e End) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished, s.final = true, e
	s.mu.Unlock()
	s.nudgePump()
}

func (s *Stream) nudgePump() {
	select {
	case s.nudge <- struct{}{}:
	default:
	}
}

// Close abandons a stream the caller will not drain further and releases the
// pump. Cancelling is the polite form; this one only cleans up locally.
func (s *Stream) Close() {
	s.closeOnce.Do(func() { close(s.abandoned) })
}

// drain moves buffered chunks onto the channel and delivers the end last.
func (s *Stream) drain() {
	defer s.Close()
	for {
		s.mu.Lock()
		if len(s.buffered) > 0 {
			item := s.buffered[0]
			s.buffered = s.buffered[1:]
			s.mu.Unlock()
			select {
			case s.chunks <- item:
			case <-s.abandoned:
				return
			}
			continue
		}
		finished, final := s.finished, s.final
		s.mu.Unlock()
		if finished {
			close(s.chunks)
			s.end <- final
			close(s.end)
			return
		}
		select {
		case <-s.nudge:
		case <-s.abandoned:
			return
		}
	}
}

// closeStreams ends every open stream when the connection drops.
func (c *Client) closeStreams(err error) {
	c.mu.Lock()
	open := c.streams
	c.streams = nil
	c.mu.Unlock()
	for _, stream := range open {
		stream.conclude(End{Err: rpc.NewError(rpc.CodeInternal, err.Error(), "")})
	}
}
