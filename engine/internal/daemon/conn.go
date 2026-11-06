package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// QueueSize bounds a connection's outbound queue. A client that stops reading
// fills it and is disconnected rather than blocking the scanner (spec,
// "Subscriptions").
const QueueSize = 256

// MaxMessageBytes is the framing limit from the spec's "Transport details".
const MaxMessageBytes = 4 << 20

// Conn is one client connection: a read loop that dispatches requests and a
// write loop that drains a bounded outbound queue.
type Conn struct {
	id  uint64
	srv *Server
	nc  net.Conn

	out       chan []byte
	closeOnce sync.Once
	closed    chan struct{}
	writerWG  sync.WaitGroup

	// Guarded by srv.subsMu, because the publisher reads them while fanning
	// out and state.subscribe must register atomically with its own reply.
	subscribed bool
	include    scanner.Include
	events     bool
	scope      state.Scope

	// Guarded by mu. Everything here describes this one connection and is read
	// only by its own goroutines, which is why it does not share subsMu.
	mu             sync.Mutex
	client         string
	clientVersion  string
	keepalive      bool
	helloDone      bool
	streams        map[string]context.CancelFunc
	shutdownOnIdle bool
}

// newConn builds a connection with its queues ready. The outbound queue is
// bounded here rather than by the caller: a client that stops reading must not
// be able to grow the daemon's memory one message at a time.
func newConn(id uint64, srv *Server, nc net.Conn) *Conn {
	return &Conn{
		id:      id,
		srv:     srv,
		nc:      nc,
		out:     make(chan []byte, QueueSize),
		closed:  make(chan struct{}),
		streams: map[string]context.CancelFunc{},
	}
}

// ID is the connection's daemon-assigned identifier, used in log lines.
func (c *Conn) ID() uint64 { return c.id }

// Client is the {name, version, keepalive} the client sent with daemon.hello.
func (c *Conn) Client() (name, version string, keepalive bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client, c.clientVersion, c.keepalive
}

// setHello records the client identity. A second hello on the same connection
// simply overwrites it.
func (c *Conn) setHello(name, version string, keepalive bool) {
	c.mu.Lock()
	c.client = name
	c.clientVersion = version
	c.keepalive = keepalive
	c.helloDone = true
	c.mu.Unlock()
	// The keepalive count is what decides whether an idle daemon may stop, so it
	// is recomputed after every hello rather than derived on demand.
	c.srv.recountKeepalive()
}

// Keepalive reports whether this client asked the daemon to stay up.
func (c *Conn) Keepalive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.keepalive
}

// RegisterStream records a cancel function for a streaming subscription so
// stream.cancel and disconnect can both stop it.
func (c *Conn) RegisterStream(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	c.streams[id] = cancel
	c.mu.Unlock()
}

// CancelStream stops one stream. It reports whether the id was known.
func (c *Conn) CancelStream(id string) bool {
	c.mu.Lock()
	cancel, known := c.streams[id]
	delete(c.streams, id)
	c.mu.Unlock()

	if !known {
		return false
	}
	// Cancelled after the unlock, for the same reason cancelAllStreams cancels
	// its batch there: the stream unwinds into code that may want c.mu again.
	cancel()
	return true
}

// cancelAllStreams stops every stream on this connection. Disconnecting
// cancels all of a connection's streams.
func (c *Conn) cancelAllStreams() {
	for _, cancel := range c.replaceStreams(nil) {
		cancel()
	}
}

// replaceStreams installs next as the stream registry and returns the previous
// one, so the caller can cancel after the lock is released. Cancelling under
// c.mu would deadlock: a stream being torn down unwinds into code that may want
// the connection again. A nil next means an empty registry.
func (c *Conn) replaceStreams(next map[string]context.CancelFunc) map[string]context.CancelFunc {
	if next == nil {
		next = map[string]context.CancelFunc{}
	}
	c.mu.Lock()
	previous := c.streams
	c.streams = next
	c.mu.Unlock()
	return previous
}

// requestShutdown records that this connection asked the daemon to stop, so the
// stop happens after the reply has been queued rather than before. daemon.shutdown
// relies on that ordering to answer at all.
func (c *Conn) requestShutdown() {
	c.mu.Lock()
	c.shutdownOnIdle = true
	c.mu.Unlock()
}

// shutdownRequested reports whether this connection asked for a shutdown. The
// request is sticky: once set, it stays set for the connection's life.
func (c *Conn) shutdownRequested() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdownOnIdle
}

// enqueue puts one pre-marshalled message on the outbound queue. It never
// blocks: a full queue means the client is not reading, and the connection is
// closed instead (spec, "Subscriptions"). It reports whether the message was
// accepted.
func (c *Conn) enqueue(msg []byte) bool {
	select {
	case <-c.closed:
		return false
	default:
	}
	select {
	case c.out <- msg:
		return true
	default:
		c.abandonSlowClient()
		return false
	}
}

// abandonSlowClient disconnects a client that has stopped reading. Its queue is
// full, and waiting for it to drain would let one stalled client block whoever
// is publishing. The close runs on its own goroutine because it takes the
// connection registry lock, which a caller of enqueue may already hold.
func (c *Conn) abandonSlowClient() {
	c.srv.logger.Warn("disconnecting client: outbound queue full",
		"conn", c.id, "queue", QueueSize)
	go c.closeWithReason("outbound queue overflow")
}

// Notify sends a notification to this client.
func (c *Conn) Notify(method string, params any) error {
	framed, err := marshalNotification(method, params)
	if err != nil {
		return err
	}
	if !c.enqueue(framed) {
		return errors.New("daemon: connection closed")
	}
	return nil
}

// marshalNotification frames one notification for the wire: params first, then
// the envelope that names the method.
func marshalNotification(method string, params any) ([]byte, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rpc.Notification{JSONRPC: rpc.Version, Method: method, Params: encoded})
}

// closeWithReason shuts the connection down once, logging why.
func (c *Conn) closeWithReason(reason string) {
	c.closeOnce.Do(func() {
		// The order matters: the closed channel stops both loops first, then
		// streams are cancelled, then the socket goes, and only then is the
		// connection removed from the registry — removing it last is what keeps
		// a broadcast written during the teardown from targeting a socket that
		// is already gone.
		close(c.closed)
		c.cancelAllStreams()
		_ = c.nc.Close()
		c.srv.removeConn(c, reason)
	})
}

// Close ends the connection.
func (c *Conn) Close() { c.closeWithReason("closed") }

// writeLoop drains the outbound queue onto the socket. It exits when the
// connection closes; a write error closes the connection.
func (c *Conn) writeLoop() {
	defer c.writerWG.Done()
	out := bufio.NewWriter(c.nc)
	for {
		select {
		case <-c.closed:
			// Best effort: push out whatever is already queued, then stop.
			writeQueued(out, c.out)
			_ = out.Flush()
			return
		case msg := <-c.out:
			if err := writeFramed(out, msg); err != nil {
				c.closeWithReason("write error: " + err.Error())
				return
			}
			// Coalesce whatever else is already queued into one flush, so a
			// burst costs one syscall rather than one per message.
			writeQueued(out, c.out)
			if err := out.Flush(); err != nil {
				c.closeWithReason("write error: " + err.Error())
				return
			}
		}
	}
}

// writeFramed writes one message and its newline terminator.
func writeFramed(out *bufio.Writer, msg []byte) error {
	if _, err := out.Write(msg); err != nil {
		return err
	}
	_, err := out.WriteString("\n")
	return err
}

// writeQueued writes everything already sitting in the queue, without waiting
// for more. Errors are ignored: the caller flushes next, and a real write
// failure surfaces there.
func writeQueued(out *bufio.Writer, queue <-chan []byte) {
	for {
		select {
		case msg := <-queue:
			_ = writeFramed(out, msg)
		default:
			return
		}
	}
}

// readLoop decodes and dispatches messages until the client disconnects.
// Requests are served one at a time, in order, so a subscribe reply can never
// overtake the deltas that follow it.
func (c *Conn) readLoop(ctx context.Context) {
	dec := rpc.NewDecoder(c.nc, MaxMessageBytes)
	for {
		msg, err := dec.Next()
		if err != nil {
			if !c.survivesDecodeError(err) {
				return
			}
			continue
		}
		c.dispatch(ctx, msg)

		select {
		case <-c.closed:
			return
		default:
		}
	}
}

// survivesDecodeError decides what a decode failure means for the connection,
// and reports whether the loop should keep reading.
//
// A clean EOF or an unreadable stream ends the connection. A message that was
// merely too large or malformed gets an error reply and the loop continues: the
// decoder has already resynchronised on the next line, and hanging up on a
// client that sent one bad message helps nobody.
func (c *Conn) survivesDecodeError(err error) bool {
	switch {
	case errors.Is(err, io.EOF):
		c.closeWithReason("client disconnected")
		return false
	case errors.Is(err, rpc.ErrOversize):
		c.replyError(nil, rpc.NewError(rpc.CodeInvalidParams,
			"message exceeds the 4 MiB limit", ""))
		return true
	}
	var protocolError *rpc.Error
	if errors.As(err, &protocolError) {
		c.replyError(nil, protocolError)
		return true
	}
	c.closeWithReason("read error: " + err.Error())
	return false
}

// dispatch acts on one decoded message.
func (c *Conn) dispatch(ctx context.Context, msg rpc.Message) {
	switch {
	case msg.IsRequest():
		c.serve(ctx, msg)
	case msg.IsNotification():
		// The daemon accepts no client notifications yet. Ignoring them keeps a
		// future client compatible instead of getting it disconnected.
		c.srv.logger.Debug("ignoring client notification", "conn", c.id, "method", msg.Method)
	default:
		c.replyError(msg.ID, rpc.NewError(rpc.CodeInvalidParams,
			"expected a request with both id and method", ""))
	}
}

// serve runs one request through the dispatcher and writes its reply.
func (c *Conn) serve(ctx context.Context, msg rpc.Message) {
	c.srv.touch()

	handler, served := lookupHandler(msg.Method)
	if !served {
		c.replyError(msg.ID, rpc.NewError(rpc.CodeNotFound,
			"unknown method "+msg.Method,
			"run `oberth daemon schema` to see the methods this daemon serves"))
		return
	}

	request := &Request{
		Method:  msg.Method,
		ID:      msg.ID,
		Params:  msg.Params,
		Conn:    c,
		Runtime: c.srv.runtime,
	}
	result, err := handler(ctx, request)
	switch {
	case errors.Is(err, ErrResponseSent):
		// The handler already wrote its own reply; adding one would be a
		// protocol violation.
	case err != nil:
		c.replyError(msg.ID, errorFor(err))
	default:
		c.replyResult(msg.ID, result)
	}

	if c.shutdownRequested() {
		go c.srv.Shutdown()
	}
}

// sendResponse is the one place a response is framed and queued, so a success
// and a failure cannot drift apart in how they are addressed.
func (c *Conn) sendResponse(id json.RawMessage, result json.RawMessage, failure *rpc.Error) {
	if id == nil {
		// A request that could not be parsed has no id, and JSON-RPC still
		// requires a reply: null is the address for it.
		id = json.RawMessage("null")
	}
	payload, err := json.Marshal(rpc.Response{
		JSONRPC: rpc.Version,
		ID:      id,
		Result:  result,
		Error:   failure,
	})
	if err != nil {
		// Nowhere left to report this; dropping it beats recursing.
		return
	}
	c.enqueue(payload)
}

// replyResult marshals and queues a successful response.
func (c *Conn) replyResult(id json.RawMessage, result any) {
	encoded, err := json.Marshal(result)
	if err != nil {
		c.replyError(id, rpc.NewError(rpc.CodeInternal, "marshalling result: "+err.Error(), ""))
		return
	}
	c.sendResponse(id, encoded, nil)
}

// replyError queues an error response. A nil id still gets a response, per
// JSON-RPC, so a client that sent an unparseable request learns why.
func (c *Conn) replyError(id json.RawMessage, e *rpc.Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	msg, err := json.Marshal(rpc.Response{JSONRPC: rpc.Version, ID: id, Error: e})
	if err != nil {
		return
	}
	c.enqueue(msg)
}
