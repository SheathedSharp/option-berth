package runtimesvc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// QueueSize is the capacity of a connection's outbound queue. A client that
// stops reading fills the queue and is disconnected instead of blocking the
// scanner (spec, "Subscriptions").
const QueueSize = 256

// MaxMessageBytes is the framing limit the spec states under "Transport
// details".
const MaxMessageBytes = 4 << 20

// Conn is a single client connection: the read loop that dispatches its
// requests, and the write loop that drains its bounded outbound queue.
type Conn struct {
	nc  net.Conn
	id  uint64
	srv *Server

	writerWG  sync.WaitGroup
	closeOnce sync.Once
	closed    chan struct{}
	out       chan []byte

	// Guarded by srv.subsMu: the publisher reads these while fanning out, and
	// state.subscribe has to register them atomically with its own reply.
	events     bool
	subscribed bool
	scope      state.Scope
	include    scanner.Include

	// Guarded by mu. Everything here describes this one connection and is read
	// only by its own goroutines, which is why it does not share subsMu.
	mu             sync.Mutex
	helloDone      bool
	keepalive      bool
	clientVersion  string
	client         string
	streams        map[string]context.CancelFunc
	shutdownOnIdle bool
}

// newConn builds a connection with its queues ready. The outbound queue is
// bounded right here rather than by the caller: a client that stops reading
// must not be able to grow the runtime service's memory one message at a time.
func newConn(id uint64, srv *Server, nc net.Conn) *Conn {
	return &Conn{
		nc:      nc,
		id:      id,
		srv:     srv,
		streams: map[string]context.CancelFunc{},
		out:     make(chan []byte, QueueSize),
		closed:  make(chan struct{}),
	}
}

// ID is the runtime service-assigned identifier of this connection, used in log lines.
func (c *Conn) ID() uint64 { return c.id }

// Client is the {name, version, keepalive} triple sent with runtime.service.hello.
func (c *Conn) Client() (name, version string, keepalive bool) {
	c.mu.Lock()
	name, version, keepalive = c.client, c.clientVersion, c.keepalive
	c.mu.Unlock()
	return name, version, keepalive
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
	// The keepalive count is what decides whether an idle runtime service may stop, so it
	// is recomputed after every hello rather than derived on demand.
	c.srv.recountKeepalive()
}

// Keepalive reports whether this client asked the runtime service to stay up.
func (c *Conn) Keepalive() bool {
	c.mu.Lock()
	keepalive := c.keepalive
	c.mu.Unlock()
	return keepalive
}

// RegisterStream records the cancel function of a streaming subscription, so
// both stream.cancel and a disconnect can stop it.
func (c *Conn) RegisterStream(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.streams[id] = cancel
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

// requestShutdown records that this connection asked the runtime service to stop, so the
// stop happens after the reply has been queued rather than before. runtime.service.shutdown
// relies on that ordering to answer at all.
func (c *Conn) requestShutdown() {
	c.mu.Lock()
	c.shutdownOnIdle = true
	c.mu.Unlock()
}

// shutdownRequested reports whether this connection asked for a shutdown. The
// request is sticky: once set it stays set for the rest of the connection's
// life.
func (c *Conn) shutdownRequested() bool {
	c.mu.Lock()
	requested := c.shutdownOnIdle
	c.mu.Unlock()
	return requested
}

// enqueue puts one pre-marshalled message on the outbound queue without ever
// blocking. A full queue means the client is not reading, so the connection is
// closed instead (spec, "Subscriptions"); the report says whether the message
// was accepted.
func (c *Conn) enqueue(msg []byte) bool {
	if c.closing() {
		return false
	}
	select {
	case c.out <- msg:
		return true
	default:
	}
	c.abandonSlowClient()
	return false
}

// closing reports whether the connection has already been closed.
func (c *Conn) closing() bool {
	select {
	case <-c.closed:
		return true
	default:
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
		return errors.New("runtime_service: connection closed")
	}
	return nil
}

// marshalNotification frames one notification for the wire: params first, then
// the envelope that names the method. It is the only place the runtime service builds
// that envelope, so a payload that cannot be encoded reaches every caller as an
// error — to be dropped, not sent as a frame whose params are absent — instead
// of each call site deciding for itself. It is a variable because the fan-out's
// "one encoding per view" rule is only observable by counting calls.
var marshalNotification = func(method string, params any) ([]byte, error) {
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(rpc.Notification{JSONRPC: rpc.Version, Method: method, Params: encoded})
}

// closeWithReason shuts the connection down once, logging why.
func (c *Conn) closeWithReason(reason string) {
	c.closeOnce.Do(func() { c.teardown(reason) })
}

// teardown releases a connection in the one order that is safe: the closed
// channel stops both loops first, streams are cancelled next, then the socket
// goes, and only then does the connection leave the registry. Removing it last
// is what stops a broadcast written during teardown from targeting a socket
// that is already gone.
func (c *Conn) teardown(reason string) {
	close(c.closed)
	c.cancelAllStreams()
	c.nc.Close()
	c.srv.removeConn(c, reason)
}

// Close ends this connection.
func (c *Conn) Close() { c.closeWithReason("closed") }

// writeLoop drains the outbound queue onto the socket. It exits once the
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
	decoder := rpc.NewDecoder(c.nc, MaxMessageBytes)
	for {
		msg, err := decoder.Next()
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
		// The runtime service accepts no client notifications yet. Ignoring them keeps a
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

	if err := rpc.RetiredRuntimeServiceMethodError(msg.Method); err != nil {
		c.replyError(msg.ID, err)
		return
	}
	handler, served := lookupHandler(msg.Method)
	if !served {
		c.replyError(msg.ID, rpc.NewError(rpc.CodeNotFound,
			"unknown method "+msg.Method,
			"run `oberth runtime service schema` to see the methods this runtime service serves"))
		return
	}

	request := &Request{
		Method:      msg.Method,
		ID:          msg.ID,
		Params:      msg.Params,
		Conn:        c,
		HostContext: c.srv.host,
	}
	result, err := handler(ctx, request)
	if err != nil && !errors.Is(err, ErrResponseSent) {
		c.replyError(msg.ID, errorFor(err))
	} else if err == nil {
		// A handler that sent its own reply (ErrResponseSent) must not get a
		// second one; that would be a protocol violation.
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

// replyError queues an error response. Per JSON-RPC a nil id still gets an
// answer, so a client whose request could not be parsed learns why.
func (c *Conn) replyError(id json.RawMessage, e *rpc.Error) {
	if id == nil {
		id = json.RawMessage("null")
	}
	response := rpc.Response{JSONRPC: rpc.Version, ID: id, Error: e}
	msg, err := json.Marshal(response)
	if err != nil {
		return
	}
	c.enqueue(msg)
}
