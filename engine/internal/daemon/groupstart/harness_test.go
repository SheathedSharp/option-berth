package groupstart

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
)

// startHarness is one isolated universe for a groups.start scenario: a throwaway
// HOME, one project directory inside it, a daemon on a private socket, and the
// listener table that daemon's scan answers from.
//
// The harness owns everything a scenario must not have to remember: the child
// processes it caused, the log dump that makes a failure readable, and the
// environment isolation that keeps the developer's own config out of the run.
type startHarness struct {
	t    *testing.T
	ctx  context.Context
	home string
	dir  string

	ports  *portTable
	client *client.Client

	// started is every pid the stream reported, so a failing scenario does not
	// leave listeners behind; chunks is everything streamed, for the log dump.
	started []int
	chunks  []rpc.GroupsStartChunk
}

// newHarness isolates the process and starts the daemon a scenario talks to.
// Every scenario starts from its own empty world: two of them never share a
// HOME, a socket, a ledger or a port claim.
func newHarness(t *testing.T) *startHarness {
	t.Helper()
	skipOnWindows(t)
	home := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("BERTH_LOG_DIR", filepath.Join(home, "logs"))
	t.Setenv(envListen, "1")

	dir := filepath.Join(home, "project")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("creating the project directory: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())

	h := &startHarness{t: t, ctx: ctx, home: home, dir: dir, ports: newPortTable(dir)}
	h.client = h.daemon()

	t.Cleanup(func() {
		cancel()
		h.killChildren()
		h.dumpLogs()
	})
	return h
}

// daemon starts the product's own server on a private socket and connects to it
// as a client would. The RPC below it is the same one the CLI and the macOS
// client call; nothing here reaches into an internal entry point.
func (h *startHarness) daemon() *client.Client {
	h.t.Helper()
	// A unix socket path is capped at ~104 bytes and t.TempDir spells the test's
	// name into the path, so the socket gets its own short directory.
	sockDir, err := os.MkdirTemp("", "oberth-start")
	if err != nil {
		h.t.Fatalf("creating the socket directory: %v", err)
	}
	h.t.Cleanup(func() { os.RemoveAll(sockDir) })
	socket := filepath.Join(sockDir, "d.sock")

	srv := daemon.New(daemon.Options{
		Socket:  socket,
		Version: "test",
		DBPath:  filepath.Join(sockDir, "option-berth.db"),
		Scanner: scanner.New(scanner.Options{
			DaemonVersion: "test",
			Scan:          h.ports.scan,
		}),
	})
	served := make(chan error, 1)
	go func() { served <- srv.Serve(h.ctx) }()
	h.t.Cleanup(func() {
		srv.Shutdown()
		<-srv.Done()
	})

	var c *client.Client
	for attempt := 0; attempt < 100; attempt++ {
		c, err = client.Dial(h.ctx, client.ClientInfo{Name: "cli", Version: "test", Socket: socket})
		if err == nil {
			h.t.Cleanup(func() { c.Close() })
			return c
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case serveErr := <-served:
		h.t.Fatalf("the test daemon did not start: %v (dial: %v)", serveErr, err)
	default:
	}
	h.t.Fatalf("connecting to the test daemon: %v", err)
	return nil
}

// manifest writes the project's manifest and returns its path.
func (h *startHarness) manifest(body string) string {
	h.t.Helper()
	return writeManifest(h.t, h.dir, body)
}

// writeManifest is the file write itself, for the tests that only need a
// manifest on disk to read back through the product's own loader.
func writeManifest(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("writing the manifest: %v", err)
	}
	return path
}

// service is the `cmd:` a scenario writes for a service that actually comes up.
func (h *startHarness) service() string {
	h.t.Helper()
	self, err := os.Executable()
	if err != nil {
		h.t.Fatalf("resolving the test binary: %v", err)
	}
	// Single-quoted for YAML, double-quoted for the argv splitter: the value is
	// one scalar that splits into [binary, -test.run=...].
	return fmt.Sprintf("'%q -test.run=TestNothing'", self)
}

// freePort reserves a port nothing is listening on and has the scan watch it, so
// a service that binds it becomes an observed listener.
func (h *startHarness) freePort() int {
	h.t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatalf("finding a free port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	h.ports.watch(port, os.Getpid())
	return port
}

// occupiedPort is a port something else is already listening on, standing in for
// the second `oberth up` of a service that is already up.
func (h *startHarness) occupiedPort() (int, net.Listener) {
	h.t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		h.t.Fatalf("occupying a port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	h.ports.watch(port, os.Getpid())
	h.t.Cleanup(func() { listener.Close() })
	return port, listener
}

// start runs one groups.start and drains its stream: the reply the daemon gave,
// every chunk in the order it arrived, and the end payload.
func (h *startHarness) start(params rpc.GroupsStartParams) (rpc.GroupsStartResult, []rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	h.t.Helper()
	var reply rpc.GroupsStartResult
	stream, err := h.client.Stream(h.ctx, "groups.start", params, &reply)
	if err != nil {
		h.t.Fatalf("groups.start: %v", err)
	}
	defer stream.Close()
	chunks, end := h.drain(stream)
	return reply, chunks, end
}

// startChunks is start for the scenarios that only read what the stream carried.
func (h *startHarness) startChunks(params rpc.GroupsStartParams) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	h.t.Helper()
	_, chunks, end := h.start(params)
	return chunks, end
}

// open starts a groups.start and hands back the stream itself, for the
// scenarios that drive it — cancel it, read a snapshot while it runs — instead
// of waiting for the end.
func (h *startHarness) open(params rpc.GroupsStartParams) (*client.Stream, rpc.GroupsStartResult) {
	h.t.Helper()
	var reply rpc.GroupsStartResult
	stream, err := h.client.Stream(h.ctx, "groups.start", params, &reply)
	if err != nil {
		h.t.Fatalf("groups.start: %v", err)
	}
	return stream, reply
}

// startError is the call a client makes when it expects the daemon to refuse:
// the error is the answer, and there is no stream to drain.
func (h *startHarness) startError(params rpc.GroupsStartParams) error {
	h.t.Helper()
	stream, err := h.client.Stream(h.ctx, "groups.start", params, nil)
	if err == nil {
		stream.Close()
	}
	return err
}

// drain reads a stream to its end. Chunks are decoded in arrival order because
// that order is part of the contract these scenarios assert on.
func (h *startHarness) drain(stream *client.Stream) ([]rpc.GroupsStartChunk, rpc.GroupsStartEnd) {
	h.t.Helper()
	var chunks []rpc.GroupsStartChunk
	done := make(chan struct{})
	go func() {
		defer close(done)
		for raw := range stream.Chunks() {
			var chunk rpc.GroupsStartChunk
			if err := json.Unmarshal(raw, &chunk); err != nil {
				h.t.Errorf("decoding a chunk: %v", err)
				continue
			}
			h.observe(chunk)
			chunks = append(chunks, chunk)
		}
	}()
	select {
	case end := <-stream.End():
		<-done
		if end.Err != nil {
			h.t.Fatalf("stream ended with an error: %v", end.Err)
		}
		var summary rpc.GroupsStartEnd
		if err := end.Decode(&summary); err != nil {
			h.t.Fatalf("decoding the end payload: %v", err)
		}
		return chunks, summary
	case <-time.After(60 * time.Second):
		h.t.Fatal("groups.start never ended")
		return nil, rpc.GroupsStartEnd{}
	}
}

// startResult is what a background drain produced. Draining on a goroutine
// keeps t.Fatal out of it while the scenario polls state.snapshot.
type startResult struct {
	chunks []rpc.GroupsStartChunk
	end    rpc.GroupsStartEnd
	err    error
}

// drainAsync is drain on a goroutine, for the scenarios that inspect the daemon
// while the start is still running. The chunks still go through observe, so the
// harness knows every process and port this call produced.
func (h *startHarness) drainAsync(stream *client.Stream) <-chan startResult {
	out := make(chan startResult, 1)
	go func() {
		var chunks []rpc.GroupsStartChunk
		for raw := range stream.Chunks() {
			var chunk rpc.GroupsStartChunk
			if err := json.Unmarshal(raw, &chunk); err != nil {
				out <- startResult{chunks: chunks, err: err}
				return
			}
			h.observe(chunk)
			chunks = append(chunks, chunk)
		}
		end := <-stream.End()
		if end.Err != nil {
			out <- startResult{chunks: chunks, err: end.Err}
			return
		}
		var summary rpc.GroupsStartEnd
		err := end.Decode(&summary)
		out <- startResult{chunks: chunks, end: summary, err: err}
	}()
	return out
}

// observe applies one chunk as it arrives. A started chunk is the daemon saying
// "this process runs on this port", which is exactly the listener a real host
// would show on its next scan — so the harness feeds it back into the table
// while the start is still running, instead of after it ends. That is what lets
// a dependent service wait on a `port: auto` dependency inside one call.
func (h *startHarness) observe(chunk rpc.GroupsStartChunk) {
	h.chunks = append(h.chunks, chunk)
	if chunk.PID > 0 {
		h.started = append(h.started, chunk.PID)
	}
	if chunk.Port > 0 && chunk.PID > 0 {
		h.ports.watch(chunk.Port, chunk.PID)
	}
}

// call is one ordinary RPC, for the reads a scenario makes beside the start.
func (h *startHarness) call(method string, params, out any) error {
	h.t.Helper()
	return h.client.Call(h.ctx, method, params, out)
}

// logPath is where the daemon appends one service's output. A service whose
// prepare failed has no started chunk to name it, so that path is built here.
func (h *startHarness) logPath(group, service string) string {
	return filepath.Join(os.Getenv("BERTH_LOG_DIR"), group, service+".log")
}

// waitForLog waits until a service's log contains every line a scenario expects.
// The child prints its environment and its listening line before it accepts, so
// the log is where those facts surface; polling it beats racing the write.
func (h *startHarness) waitForLog(path string, want ...string) {
	h.t.Helper()
	h.eventually(10*time.Second, "the service log", func() error {
		written, _ := os.ReadFile(path)
		for _, line := range want {
			if !strings.Contains(string(written), line) {
				return fmt.Errorf("missing %q in:\n%s", line, written)
			}
		}
		return nil
	})
}

// eventually retries a check until it holds or the deadline passes. Readiness in
// this package is asynchronous by design — the scanner publishes on its own
// beat — so a scenario waits for the fact rather than sleeping a fixed time.
func (h *startHarness) eventually(timeout time.Duration, what string, check func() error) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("waiting for %s: %v", what, err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// killChildren stops the processes this scenario caused. It runs on every exit
// path, so a failure cannot leave a listener holding a port for the next test.
func (h *startHarness) killChildren() {
	for _, pid := range h.started {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}
}

// dumpLogs is the failure diagnostic: what the services wrote, so a test says
// why a child did not come up instead of only that it did not.
func (h *startHarness) dumpLogs() {
	if !h.t.Failed() {
		return
	}
	for _, chunk := range h.chunks {
		if chunk.LogPath == "" {
			continue
		}
		written, err := os.ReadFile(chunk.LogPath)
		if err != nil {
			continue
		}
		h.t.Logf("%s log:\n%s", chunk.Service, written)
	}
}

// portTable is the listener table the harness's scan publishes. A scenario adds
// the ports it declared or occupied; every service the stream reports adds its
// own port with the pid the daemon spawned.
type portTable struct {
	mu   sync.Mutex
	dir  string
	pids map[int]int // port -> pid that owns it
}

func newPortTable(dir string) *portTable {
	return &portTable{dir: dir, pids: map[int]int{}}
}

func (p *portTable) watch(port, pid int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pids[port] = pid
}

// scan dials every watched port and reports the ones that answer, attributed to
// the owner recorded for it. Real sockets, deterministic result: what a service
// does to this table is what it would do to a real host's listener list.
func (p *portTable) scan(scanner.Include) ([]ports.ListeningPort, error) {
	p.mu.Lock()
	watched := make(map[int]int, len(p.pids))
	for port, pid := range p.pids {
		watched[port] = pid
	}
	p.mu.Unlock()

	var listening []ports.ListeningPort
	for port, pid := range watched {
		if !dialableWithin(port, 100*time.Millisecond) {
			continue
		}
		listening = append(listening, ports.ListeningPort{
			Port: port, BindAddress: "127.0.0.1", IPVersion: "ipv4",
			PID: pid, Process: "listener", Cwd: p.dir,
		})
	}
	return listening, nil
}

// chunkFor is the one chunk a service produced. Every scenario here reports one
// chunk per service, so anything else is a broken contract, not a variant.
func chunkFor(t *testing.T, chunks []rpc.GroupsStartChunk, service string) rpc.GroupsStartChunk {
	t.Helper()
	var found []rpc.GroupsStartChunk
	for _, chunk := range chunks {
		if chunk.Service == service {
			found = append(found, chunk)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s produced %d chunks, want exactly one: %+v", service, len(found), chunks)
	}
	return found[0]
}

// order is the services in the sequence the stream reported them, which is the
// dependency order the manifest asked for.
func order(chunks []rpc.GroupsStartChunk) []string {
	reported := make([]string, 0, len(chunks))
	for _, chunk := range chunks {
		reported = append(reported, chunk.Service)
	}
	return reported
}

// rpcCode is the stable code a client reads from a refused call, or "" when the
// error did not come from the RPC layer.
func rpcCode(err error) string {
	var refused *rpc.Error
	if errors.As(err, &refused) {
		return refused.Data.Code
	}
	return ""
}
