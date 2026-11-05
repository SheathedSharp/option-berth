// Package client is the Go client for the oberth daemon. The CLI, the agent
// adapter and every other in-process consumer dial through it, so autostart,
// the protocol check and subscription plumbing exist in one place.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
)

// AutostartTimeout bounds how long a client waits for the daemon it launched
// to accept connections.
const AutostartTimeout = 3 * time.Second

// ClientInfo names the caller in daemon.hello and chooses the connection.
type ClientInfo struct {
	// Name is the caller kind: cli, app, mcp or tray.
	Name string
	// Version is the caller's own version.
	Version string
	// Keepalive disables the daemon's idle timeout for this connection.
	Keepalive bool
	// Socket overrides daemon.SocketPath.
	Socket string
	// NoAutostart fails Connect instead of running `oberth serve -d`.
	NoAutostart bool
	// BinaryPath overrides the executable autostart runs.
	BinaryPath string
}

// ErrNotRunning means no daemon is listening and autostart was disabled or
// failed.
var ErrNotRunning = errors.New("oberth daemon is not running")

// ProtocolMismatchError reports a daemon whose major protocol version differs
// from the one this client was built for.
type ProtocolMismatchError struct {
	Daemon string
	Client string
}

func (e *ProtocolMismatchError) Error() string {
	return fmt.Sprintf(
		"oberth daemon speaks protocol v%s, this client needs v%s. Restart it? (`oberth daemon restart`)",
		e.Daemon, e.Client)
}

// Client is one connection to the daemon. It is safe for concurrent use.
type Client struct {
	conn io.ReadWriteCloser

	// Wire and identity.
	enc    *rpc.Encoder
	socket string
	hello  rpc.DaemonHelloResult

	// The request book and the notification registry.
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan rpc.Message
	subs    []*Subscription
	streams map[string]*Stream

	// Connection state, guarded by mu.
	closed   bool
	closeErr error

	// done is closed by shutdown.
	done chan struct{}
}

// Connect dials the daemon, starting one with `oberth serve --detach` when
// nothing is listening, then performs the daemon.hello handshake.
func Connect(ctx context.Context, info ClientInfo) (*Client, error) {
	socket := socketFor(info)
	conn, err := daemon.Dial(socket)
	if err != nil {
		conn, err = startAndDial(ctx, socket, info)
		if err != nil {
			return nil, err
		}
	}
	return handshake(ctx, conn, socket, info)
}

func socketFor(info ClientInfo) string {
	if info.Socket != "" {
		return info.Socket
	}
	return daemon.SocketPath()
}

// startAndDial runs autostart and retries the dial once.
func startAndDial(ctx context.Context, socket string, info ClientInfo) (net.Conn, error) {
	if info.NoAutostart {
		return nil, fmt.Errorf("%w (socket %s)", ErrNotRunning, socket)
	}
	if err := Autostart(ctx, info.BinaryPath, socket); err != nil {
		return nil, err
	}
	conn, err := daemon.Dial(socket)
	if err != nil {
		return nil, fmt.Errorf("%w: started it but could not connect to %s: %v",
			ErrNotRunning, socket, err)
	}
	return conn, nil
}

// Dial connects to a daemon that is already running and never starts one.
func Dial(ctx context.Context, info ClientInfo) (*Client, error) {
	info.NoAutostart = true
	return Connect(ctx, info)
}

// Attach speaks the protocol over an already-open stream instead of a socket
// this package dialled. Calls, subscriptions and streams behave exactly as on
// the local socket.
//
// label names the connection in errors and in Socket(); it is never dialled.
// Closing the Client closes rwc.
func Attach(ctx context.Context, rwc io.ReadWriteCloser, label string, info ClientInfo) (*Client, error) {
	return handshake(ctx, rwc, label, info)
}

func handshake(ctx context.Context, conn io.ReadWriteCloser, socket string, info ClientInfo) (*Client, error) {
	client := &Client{conn: conn, socket: socket, done: make(chan struct{})}
	client.enc = rpc.NewEncoder(conn)
	client.pending = map[string]chan rpc.Message{}
	client.streams = map[string]*Stream{}
	go client.readLoop()

	var hello rpc.DaemonHelloResult
	params := rpc.DaemonHelloParams{
		Client:        info.Name,
		ClientVersion: info.Version,
		Keepalive:     info.Keepalive,
	}
	if err := client.Call(ctx, "daemon.hello", params, &hello); err != nil {
		client.Close()
		return nil, err
	}
	if err := CheckProtocol(hello.ProtocolVersion); err != nil {
		client.Close()
		return nil, err
	}
	client.hello = hello
	return client, nil
}

// CheckProtocol compares the daemon's protocol version with this binary's.
// Only the major has to match: additive changes bump the minor.
func CheckProtocol(daemonVersion string) error {
	daemonMajor, err := majorOf(daemonVersion)
	if err != nil {
		return fmt.Errorf("daemon reported an unparseable protocol version %q", daemonVersion)
	}
	clientMajor, err := majorOf(rpc.ProtocolVersion)
	if err != nil {
		return err
	}
	if daemonMajor != clientMajor {
		return &ProtocolMismatchError{Daemon: daemonVersion, Client: rpc.ProtocolVersion}
	}
	return nil
}

func majorOf(version string) (int, error) {
	trimmed := strings.TrimPrefix(strings.TrimSpace(version), "v")
	major, _, _ := strings.Cut(trimmed, ".")
	if major == "" {
		return 0, fmt.Errorf("empty version")
	}
	return strconv.Atoi(major)
}

// Hello is the handshake result: versions, pid, socket and capabilities.
func (c *Client) Hello() rpc.DaemonHelloResult { return c.hello }

// Socket is the address this client is connected to.
func (c *Client) Socket() string { return c.socket }

// Call sends a request and decodes the result into out; out may be nil.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	raw, err := marshalParams(params)
	if err != nil {
		return err
	}

	id, reply, err := c.registerCall()
	if err != nil {
		return err
	}
	defer c.forgetCall(id)

	request := rpc.Request{
		JSONRPC: rpc.Version,
		ID:      json.RawMessage(id),
		Method:  method,
		Params:  raw,
	}
	if err := c.enc.Encode(request); err != nil {
		return fmt.Errorf("sending %s: %w", method, err)
	}
	return c.awaitReply(ctx, reply, out)
}

// registerCall reserves an id and its reply channel, or reports a closed
// connection.
func (c *Client) registerCall() (string, chan rpc.Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return "", nil, c.closeErr
	}
	c.nextID++
	id := strconv.FormatInt(c.nextID, 10)
	reply := make(chan rpc.Message, 1)
	c.pending[id] = reply
	return id, reply, nil
}

func (c *Client) forgetCall(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

func (c *Client) awaitReply(ctx context.Context, reply chan rpc.Message, out any) error {
	select {
	case msg := <-reply:
		return decodeReply(msg, out)
	case <-c.done:
		return c.closeErr
	case <-ctx.Done():
		return ctx.Err()
	}
}

func decodeReply(msg rpc.Message, out any) error {
	switch {
	case msg.Error != nil:
		return msg.Error
	case out == nil, len(msg.Result) == 0:
		return nil
	}
	return json.Unmarshal(msg.Result, out)
}

func marshalParams(params any) (json.RawMessage, error) {
	if params == nil {
		return json.RawMessage("{}"), nil
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("marshalling params: %w", err)
	}
	return encoded, nil
}

// readLoop routes responses to waiting calls and notifications to their
// subscribers. It is the only reader of the connection.
func (c *Client) readLoop() {
	decoder := rpc.NewDecoder(c.conn, daemon.MaxMessageBytes)
	for {
		msg, err := decoder.Next()
		if err != nil {
			c.shutdown(fmt.Errorf("daemon connection closed: %w", err))
			return
		}
		if msg.IsResponse() {
			c.mu.Lock()
			reply, waiting := c.pending[string(msg.ID)]
			c.mu.Unlock()
			if waiting {
				reply <- msg
			}
			continue
		}
		if msg.IsNotification() {
			c.dispatchNotification(msg)
		}
	}
}

func (c *Client) dispatchNotification(msg rpc.Message) {
	if c.dispatchStream(msg) {
		return
	}
	c.mu.Lock()
	listeners := append([]*Subscription{}, c.subs...)
	c.mu.Unlock()
	for _, listener := range listeners {
		listener.deliver(msg)
	}
}

// shutdown closes the connection once and fails every waiting call.
func (c *Client) shutdown(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	c.closeErr = err
	listeners := c.subs
	c.subs = nil
	c.mu.Unlock()

	close(c.done)
	_ = c.conn.Close()
	for _, listener := range listeners {
		listener.close()
	}
	c.closeStreams(err)
}

// Done is closed when the connection drops.
func (c *Client) Done() <-chan struct{} { return c.done }

// Close disconnects from the daemon. It is safe to call more than once.
func (c *Client) Close() error {
	c.shutdown(errors.New("client closed"))
	return nil
}

// Err reports why the connection closed, or nil while it is open.
func (c *Client) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closeErr
}
