package runtimesvc

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The two pipe deadlines below are hang-guards, not latency budgets. A handler
// that never answers has to fail its test rather than sit there until `go test
// -timeout` shoots the whole binary; but a handler that answers in 20 ms on a
// laptop and in four seconds on a loaded two-core CI runner is still a handler
// that answered, and calling that a bug is exactly what produced the run of
// "read pipe: i/o timeout" flakes this suite used to have at the old
// three-second bound.
//
// net.Pipe is synchronous and has no buffer — the server's write returns only
// once the test reads — so the write side needs the same headroom as the read
// side.
const (
	pipeWrite = 30 * time.Second
	pipeRead  = 30 * time.Second
	// settle bounds how long a test may wait for work the daemon schedules on
	// its own clock: a scan tick, a delta, a stream ending. The scan loop's
	// base interval is two seconds, so a bound of only a few of those would
	// measure the runner rather than the daemon.
	settle = 30 * time.Second
)

// testHarness wires a Server to an in-memory connection: the dispatcher, the
// codec, the subscription registry and the fan-out all take part, and neither
// the filesystem nor the OS scanner is touched.
//
// That last part is load-bearing, and the seams below are what enforce it:
// Scan, the process sampler and Probe are all faked, so no unit test here
// spawns netstat, lsof, ss, ps, tasklist, powershell or docker, and none opens
// a listening socket. A handler that reaches the OS behind one of those seams
// is a defect — the test would then be measuring the runner instead of the
// handler.
type testHarness struct {
	t    *testing.T
	srv  *Server
	loop *scanner.Loop

	stopScan context.CancelFunc
	scanDone chan struct{}

	mu    sync.Mutex
	rows  []ports.ListeningPort
	probe scanner.Probe

	// statsMove turns the per-process sampler from a constant answer into
	// readings that climb on every call; sampleN counts those calls.
	statsMove atomic.Bool
	sampleN   atomic.Int64
}

// newHarness builds a server whose scan loop is already running, so a change
// to the fake scan result reaches subscribers the way it does in production.
//
// A harness that opens a database has to be built *after* the t.TempDir() that
// holds it: cleanups run newest-first, and the harness must stop before the
// directory it writes into is removed.
//
// The optional tweaks adjust the scanner options before the loop is built. The
// stats cadence is what they are for: most tests only need the tick to happen
// at all, and paying the production one-second cadence in each of them costs
// wall-clock without testing anything.
type tune func(*scanner.Options)

// fastStats is the usual tweak: a cadence short enough that a test can wait
// for a tick without waiting a second for it.
func fastStats(o *scanner.Options) { o.StatsInterval = 50 * time.Millisecond }

func newHarness(t *testing.T, ctx context.Context, tunes ...tune) *testHarness {
	t.Helper()
	hn := &testHarness{t: t, scanDone: make(chan struct{})}
	hn.probe = refusedProbe

	opts := hn.scannerOptions()
	for _, tweak := range tunes {
		tweak(&opts)
	}
	hn.loop = scanner.New(opts)
	hn.srv = New(Options{Socket: "/test/option-berth.sock", Version: "test", Scanner: hn.loop})

	runCtx, stop := context.WithCancel(ctx)
	hn.stopScan = stop
	go func() {
		defer close(hn.scanDone)
		hn.loop.Run(runCtx)
	}()
	t.Cleanup(hn.shutdown)
	return hn
}

// scannerOptions is the scanner configuration the harness runs on. Every seam
// that would reach the real machine is faked, because a test that keeps a
// subscription open would otherwise fork `ps` or open a real socket once a
// second.
func (hn *testHarness) scannerOptions() scanner.Options {
	return scanner.Options{
		DaemonVersion: "test",
		// Scan answers from the rows the test installed, copied out so a caller
		// cannot mutate the harness's own slice.
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			hn.mu.Lock()
			defer hn.mu.Unlock()
			return append([]ports.ListeningPort{}, hn.rows...), nil
		},
		// Configured health goes through the injected probe and never a socket.
		Probe: func(host string, port int, path string, timeout time.Duration) ports.HealthResult {
			hn.mu.Lock()
			probe := hn.probe
			hn.mu.Unlock()
			return probe(host, port, path, timeout)
		},
		SampleStats: hn.sampleStats,
	}
}

// shutdown stops everything the harness started, in order, and does not return
// until it has stopped.
//
// Cancelling the test's context only asks the loop to stop; it does not wait
// for it, and a scan already under way goes on publishing — which records
// history through the store, into the temp directory t.TempDir is removing at
// that very moment. That race showed up as "TempDir RemoveAll cleanup:
// directory not empty", pointing at a SQLite -wal file recreated behind the
// delete. So: connections first (their handlers write too), then the loop,
// then the database.
func (hn *testHarness) shutdown() {
	hn.srv.closeAllConns("test over")
	hn.srv.wg.Wait()
	hn.stopScan()
	<-hn.scanDone
	hn.srv.closeStore()
}

// sampleStats is the harness's per-process sampler. Unless the test asked for
// movement it answers for nothing, so an ordinary harness test's stats are
// whatever its fake rows already carry.
func (hn *testHarness) sampleStats(pids []int) map[int]ports.ProcSample {
	if !hn.statsMove.Load() {
		return nil
	}
	tick := hn.sampleN.Add(1)
	answer := make(map[int]ports.ProcSample, len(pids))
	for _, pid := range pids {
		answer[pid] = ports.ProcSample{
			CPUPercent:  float64(tick),
			MemoryRSS:   int64(pid) << 10,
			ThreadCount: 3,
			State:       "running",
			Uptime:      "1s",
		}
	}
	return answer
}

// newClient starts a daemon a test only talks to, on a context that test
// cancels when it ends, and returns a connected client. Tests that reach into
// the harness use newHarness instead.
func newClient(t *testing.T) *testClient {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return newHarness(t, ctx).dial(ctx)
}

// setRows replaces what the fake scanner returns.
func (hn *testHarness) setRows(rows ...ports.ListeningPort) {
	hn.mu.Lock()
	hn.rows = rows
	hn.mu.Unlock()
}

// setProbe replaces the fake health probe.
func (hn *testHarness) setProbe(p scanner.Probe) {
	hn.mu.Lock()
	hn.probe = p
	hn.mu.Unlock()
}

// refusedProbe is the harness default: nothing really listens behind the fake
// rows, so every probe is refused — instantly and deterministically.
func refusedProbe(_ string, _ int, _ string, _ time.Duration) ports.HealthResult {
	return ports.HealthResult{Status: "refused"}
}

// dial attaches a client to the server over an in-memory pipe.
func (hn *testHarness) dial(ctx context.Context) *testClient {
	hn.t.Helper()
	clientEnd, serverEnd := net.Pipe()
	hn.srv.startConn(ctx, serverEnd)
	cl := &testClient{t: hn.t, conn: clientEnd, r: bufio.NewReader(clientEnd)}
	hn.t.Cleanup(func() { clientEnd.Close() })
	return cl
}

type testClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
	seq  int
}

// send writes one request and returns the id it was given.
func (cl *testClient) send(method string, params any) string {
	cl.t.Helper()
	cl.seq++
	id := json.RawMessage(itoa(cl.seq))
	raw, err := json.Marshal(params)
	if err != nil {
		cl.t.Fatalf("marshalling params: %v", err)
	}
	cl.conn.SetWriteDeadline(time.Now().Add(pipeWrite))
	request := rpc.Request{JSONRPC: rpc.Version, ID: id, Method: method, Params: raw}
	if err := json.NewEncoder(cl.conn).Encode(request); err != nil {
		cl.t.Fatalf("sending %s: %v", method, err)
	}
	return string(id)
}

// read returns the next message from the daemon.
func (cl *testClient) read() rpc.Message {
	cl.t.Helper()
	cl.conn.SetReadDeadline(time.Now().Add(pipeRead))
	line, err := cl.r.ReadBytes('\n')
	if err != nil {
		cl.t.Fatalf("reading from daemon: %v", err)
	}
	var msg rpc.Message
	if err := json.Unmarshal(line, &msg); err != nil {
		cl.t.Fatalf("decoding %q: %v", line, err)
	}
	return msg
}

// call sends a request and returns the response that matches it, skipping any
// notification that arrives first.
func (cl *testClient) call(method string, params, out any) *rpc.Error {
	cl.t.Helper()
	id := cl.send(method, params)
	for {
		msg := cl.read()
		if !msg.IsResponse() || string(msg.ID) != id {
			continue
		}
		if msg.Error != nil {
			return msg.Error
		}
		if out != nil && len(msg.Result) > 0 {
			if err := json.Unmarshal(msg.Result, out); err != nil {
				cl.t.Fatalf("decoding %s result: %v", method, err)
			}
		}
		return nil
	}
}

// itoa formats a request counter as a decimal string, so the client can build
// ids without dragging strconv into this file.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var digits []byte
	for i > 0 {
		digits = append([]byte{byte('0' + i%10)}, digits...)
		i /= 10
	}
	return string(digits)
}

// TestHelloOverTheCodec pushes one real request through the newline codec and
// the dispatcher, and checks the handshake the whole protocol hangs off.
func TestHelloOverTheCodec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var hello rpc.DaemonHelloResult
	if failure := cl.call("daemon.hello", rpc.DaemonHelloParams{
		Client: "cli", ClientVersion: "1.2.3",
	}, &hello); failure != nil {
		t.Fatalf("daemon.hello: %v", failure)
	}

	if hello.ProtocolVersion != rpc.ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", hello.ProtocolVersion, rpc.ProtocolVersion)
	}
	if hello.DaemonVersion != "test" {
		t.Errorf("daemon_version = %q; the harness daemon is versioned \"test\"", hello.DaemonVersion)
	}
	if hello.PID == 0 {
		t.Errorf("hello reported no pid: %+v", hello)
	}
	if hello.StartedAt == "" {
		t.Errorf("hello reported no started_at: %+v", hello)
	}
	if hello.Socket != "/test/option-berth.sock" {
		t.Errorf("socket = %q, want /test/option-berth.sock", hello.Socket)
	}
	want := Capabilities()
	if got := hello.Capabilities; len(got) != len(want) {
		t.Fatalf("capabilities = %v, want %v", got, want)
	}
	for i, capability := range hello.Capabilities {
		if capability != want[i] {
			t.Errorf("capability %d = %q, want %q", i, capability, want[i])
		}
	}
}

func TestHelloRequiresAClientName(t *testing.T) {
	cl := newClient(t)

	e := cl.call("daemon.hello", rpc.DaemonHelloParams{ClientVersion: "1"}, nil)
	if e == nil {
		t.Fatal("daemon.hello accepted a request with no client name")
	}
	if e.Code != rpc.CodeInvalidParams || e.Data.Code != "invalid_params" {
		t.Errorf("error = %+v, want invalid_params", e)
	}
}

func TestUnknownMethodIsNotFound(t *testing.T) {
	cl := newClient(t)

	e := cl.call("ports.teleport", rpc.Empty{}, nil)
	if e == nil {
		t.Fatal("an unknown method was served")
	}
	if e.Code != rpc.CodeNotFound || e.Data.Code != "not_found" {
		t.Errorf("error = %+v, want not_found", e)
	}
	if e.Data.Hint == "" {
		t.Error("error carries no hint; the contract requires one")
	}
}

func TestInvalidIncludeIsRejected(t *testing.T) {
	cl := newClient(t)

	params := map[string]any{"include": []string{"stats", "weather"}}
	if e := cl.call("state.snapshot", params, nil); e == nil {
		t.Fatal("an unknown include was accepted")
	} else if e.Code != rpc.CodeInvalidParams {
		t.Errorf("error = %+v, want invalid_params", e)
	}
}

func TestSnapshotAndStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)
	hn.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	cl := hn.dial(ctx)

	var snap state.Snapshot
	if e := cl.call("state.snapshot", rpc.StateSnapshotParams{}, &snap); e != nil {
		t.Fatalf("state.snapshot: %v", e)
	}
	if len(snap.Ports) != 1 || snap.Ports[0].Port != 8123 {
		t.Fatalf("snapshot ports = %+v, want one row on 8123", snap.Ports)
	}
	if snap.Groups == nil || snap.Sessions == nil {
		t.Error("a snapshot collection marshalled as null; the contract requires arrays")
	}

	var status rpc.DaemonStatusResult
	if e := cl.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("daemon.status: %v", e)
	}
	if got := status.Subscribers; got != 0 {
		t.Errorf("status reports %d subscribers before any subscribe, want 0", got)
	}
	if status.ScanIntervalMs == 0 || status.LastScanAt == "" {
		t.Errorf("status is missing its scanner counters: %+v", status)
	}
}

// TestSubscribeThenDelta is the core streaming contract: subscribe replies
// with the snapshot, and later changes arrive as state.delta notifications.
func TestSubscribeThenDelta(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var snap state.Snapshot
	if e := cl.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}
	if got := hn.srv.Subscribers(); got != 1 {
		t.Fatalf("Subscribers() = %d after subscribe, want 1", got)
	}

	hn.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	hn.loop.Wake()

	var delta state.Delta
	var event state.Event
	deadline := time.Now().Add(settle)
	for (len(delta.Ports.Added) == 0 || event.Kind == "") && time.Now().Before(deadline) {
		msg := cl.read()
		switch msg.Method {
		case rpc.MethodStateDelta:
			json.Unmarshal(msg.Params, &delta)
		case rpc.MethodStateEvent:
			json.Unmarshal(msg.Params, &event)
		}
	}

	if len(delta.Ports.Added) != 1 || delta.Ports.Added[0].Port != 8123 {
		t.Fatalf("delta = %+v, want one added row on 8123", delta.Ports)
	}
	if delta.Seq <= snap.Seq {
		t.Errorf("delta seq %d did not advance past snapshot seq %d", delta.Seq, snap.Seq)
	}
	if event.Kind != "port_up" {
		t.Errorf("event kind = %q, want port_up", event.Kind)
	}

	if e := cl.call("state.unsubscribe", rpc.Empty{}, nil); e != nil {
		t.Fatalf("state.unsubscribe: %v", e)
	}
	if got := hn.srv.Subscribers(); got != 0 {
		t.Errorf("Subscribers() = %d after unsubscribe, want 0", got)
	}
}

// TestSubscribeResumesFromSequence reopens the wire after a published change
// happened with no subscriber. The reconnect gets the exact cached snapshot at
// its cursor and then the missing delta/event pair, rather than a fresh state
// with no way to tell whether the transition was ever observed.
func TestSubscribeResumesFromSequence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)
	hn.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
	first := hn.dial(ctx)

	if e := first.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, nil); e != nil {
		t.Fatalf("first state.subscribe: %v", e)
	}
	baseline := first.nextDelta()
	if baseline.Seq == 0 || len(baseline.Ports.Added) != 1 || baseline.Ports.Added[0].Port != 8123 {
		t.Fatalf("first delta = %+v, want a nonzero 8123 baseline sequence", baseline)
	}
	if e := first.call("state.unsubscribe", rpc.Empty{}, nil); e != nil {
		t.Fatalf("state.unsubscribe: %v", e)
	}

	hn.setRows(
		ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"},
		ports.ListeningPort{Port: 8124, BindAddress: "127.0.0.1", PID: 43, Process: "worker"},
	)
	if _, err := hn.loop.Rescan(scanner.Include{}); err != nil {
		t.Fatalf("second scan: %v", err)
	}

	second := hn.dial(ctx)
	var resumed state.Snapshot
	if e := second.call("state.subscribe", rpc.StateSubscribeParams{
		Events: true, AfterSeq: baseline.Seq,
	}, &resumed); e != nil {
		t.Fatalf("resuming state.subscribe: %v", e)
	}
	if resumed.Seq != baseline.Seq {
		t.Fatalf("resume snapshot seq = %d, want cursor %d", resumed.Seq, baseline.Seq)
	}

	replayed := second.nextDelta()
	if replayed.Seq <= baseline.Seq || len(replayed.Ports.Added) != 1 || replayed.Ports.Added[0].Port != 8124 {
		t.Fatalf("replayed delta = %+v, want 8124 after %d", replayed, baseline.Seq)
	}
	notification := second.nextNotification(rpc.MethodStateEvent)
	var got state.Event
	if err := json.Unmarshal(notification.Params, &got); err != nil {
		t.Fatalf("decoding replayed event: %v", err)
	}
	if got.Kind != "port_up" {
		t.Fatalf("replayed event kind = %q, want port_up", got.Kind)
	}
}

// TestIncludeFiltersEnrichments checks that a subscriber which never asked for
// stats does not see them, even once another subscriber has made the scanner
// collect them.
func TestIncludeFiltersEnrichments(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hn := &testHarness{t: t}
	hn.loop = scanner.New(scanner.Options{
		DaemonVersion: "test",
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{
				Port: 9099, BindAddress: "127.0.0.1", PID: 4242, Process: "ruby",
				CPUPercent: 2.5, MemoryRSS: 2048,
			}}, nil
		},
	})
	hn.srv = New(Options{Socket: "/test/option-berth.sock", Version: "test", Scanner: hn.loop})

	plainSnap := hn.snapshotAs(ctx, t, nil)
	if plainSnap.Ports[0].Stats != nil {
		t.Errorf("stats leaked to a caller that did not ask: %+v", plainSnap.Ports[0].Stats)
	}

	statsSnap := hn.snapshotAs(ctx, t, rpc.Include{"stats"})
	if statsSnap.Ports[0].Stats == nil {
		t.Fatal("stats missing for a caller that asked for them")
	}
	if cpu := statsSnap.Ports[0].Stats.CPUPercent; cpu != 2.5 {
		t.Errorf("cpu_percent = %v, want 2.5", cpu)
	}
}

// snapshotAs opens a fresh client, asks for one state.snapshot with the given
// include set, and returns it. Each caller gets a connection of its own, so one
// test can compare what two differently-scoped subscribers are served.
func (hn *testHarness) snapshotAs(ctx context.Context, t *testing.T, include rpc.Include) state.Snapshot {
	t.Helper()
	var snapshot state.Snapshot
	if failure := hn.dial(ctx).call("state.snapshot",
		rpc.StateSnapshotParams{Include: include}, &snapshot); failure != nil {
		t.Fatalf("state.snapshot(include=%v): %v", include, failure)
	}
	return snapshot
}

// TestQueueOverflowDisconnects is the back-pressure rule: a client that stops
// reading is disconnected, never allowed to block the scanner.
func TestQueueOverflowDisconnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)

	conn := hn.subscribeWithoutReading(ctx, t)

	// The writer goroutine takes one message and then blocks on the unread
	// pipe, so everything after that piles up in the queue.
	for queued := 0; queued < QueueSize+50; queued++ {
		hn.srv.broadcastEvent(state.Event{Kind: "port_up", At: "now"})
		select {
		case <-conn.closed:
			hn.awaitDeregistered(t, queued)
			return
		default:
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("client survived %d queued notifications; the %d-slot queue did not disconnect it",
		QueueSize+50, QueueSize)
}

// subscribeWithoutReading attaches a client, marks its connection as a
// subscriber behind the server's back, and then never reads from it — which is
// exactly the misbehaviour the queue limit has to catch.
func (hn *testHarness) subscribeWithoutReading(ctx context.Context, t *testing.T) *Conn {
	t.Helper()
	clientEnd, serverEnd := net.Pipe()
	t.Cleanup(func() { clientEnd.Close() })
	hn.srv.startConn(ctx, serverEnd)

	hn.srv.subsMu.Lock()
	defer hn.srv.subsMu.Unlock()
	var subscriber *Conn
	for _, candidate := range hn.srv.conns {
		subscriber = candidate
	}
	if subscriber == nil {
		t.Fatal("the connection was not registered")
	}
	subscriber.subscribed, subscriber.events = true, true
	return subscriber
}

// awaitDeregistered waits for the overflowing connection to be taken off the
// client list. closed fires just before that step, so this gives it a moment
// rather than racing it.
func (hn *testHarness) awaitDeregistered(t *testing.T, queued int) {
	t.Helper()
	for waited := 0; waited < 100 && hn.srv.Clients() != 0; waited++ {
		time.Sleep(time.Millisecond)
	}
	if hn.srv.Clients() != 0 {
		t.Fatalf("overflowing client still registered after %d messages", queued)
	}
}

// TestShutdownAnswersBeforeClosing checks that daemon.shutdown gets its reply
// out, and daemon_stopping with it, before the socket goes away.
func TestShutdownAnswersBeforeClosing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hn := newHarness(t, ctx)
	cl := hn.dial(ctx)

	var snap state.Snapshot
	if e := cl.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
		t.Fatalf("state.subscribe: %v", e)
	}

	id := cl.send("daemon.shutdown", rpc.Empty{})

	sawOK, sawStopping := false, false
	for !sawOK || !sawStopping {
		msg := cl.read()
		switch {
		case msg.IsResponse() && string(msg.ID) == id:
			var ok rpc.OKResult
			json.Unmarshal(msg.Result, &ok)
			if !ok.OK {
				t.Fatalf("daemon.shutdown returned %+v", ok)
			}
			sawOK = true
		case msg.Method == rpc.MethodStateEvent:
			var ev state.Event
			json.Unmarshal(msg.Params, &ev)
			sawStopping = sawStopping || ev.Kind == "daemon_stopping"
		}
	}
}

// TestOversizeMessageIsRejectedNotFatal covers the 4 MiB framing limit: the
// daemon answers invalid_params and the connection stays up.
func TestOversizeMessageIsRejectedNotFatal(t *testing.T) {
	cl := newClient(t)

	go func() {
		cl.conn.SetWriteDeadline(time.Now().Add(pipeWrite))
		io.WriteString(cl.conn, `{"jsonrpc":"2.0","id":1,"method":"daemon.status","params":{"pad":"`)
		chunk := strings.Repeat("x", 64<<10)
		for written := 0; written < MaxMessageBytes+(1<<20); written += len(chunk) {
			if _, err := io.WriteString(cl.conn, chunk); err != nil {
				return
			}
		}
		io.WriteString(cl.conn, "\"}}\n")
	}()

	msg := cl.read()
	if msg.Error == nil || msg.Error.Code != rpc.CodeInvalidParams {
		t.Fatalf("oversize message produced %+v, want invalid_params", msg.Error)
	}

	// The connection survives: the decoder resynced on the next line.
	var status rpc.DaemonStatusResult
	if e := cl.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("daemon.status after an oversize message: %v", e)
	}
}

// TestStreamCancelOnAnUnknownID keeps the cancel path honest before any
// streaming method exists to use it.
func TestStreamCancelOnAnUnknownID(t *testing.T) {
	cl := newClient(t)

	e := cl.call("stream.cancel", rpc.StreamCancel{ID: "nope"}, nil)
	if e == nil || e.Code != rpc.CodeNotFound {
		t.Fatalf("stream.cancel on an unknown id = %+v, want not_found", e)
	}
}

// TestIdleTimeoutStopsTheDaemon and TestKeepaliveDisablesIdleTimeout are the
// two halves of the idle rule.
func TestIdleTimeoutStopsTheDaemon(t *testing.T) {
	idle := New(Options{Socket: "/test/option-berth.sock", IdleTimeout: 40 * time.Millisecond,
		Scanner: scanner.New(scanner.Options{})})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go idle.watchIdle(ctx)

	select {
	case <-idle.stopping:
	case <-time.After(3 * time.Second):
		t.Fatal("the daemon did not stop after its idle timeout")
	}
}

func TestKeepaliveDisablesIdleTimeout(t *testing.T) {
	idle := New(Options{Socket: "/test/option-berth.sock", IdleTimeout: 40 * time.Millisecond,
		Scanner: scanner.New(scanner.Options{})})
	idle.keepalives.Store(1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go idle.watchIdle(ctx)

	select {
	case <-idle.stopping:
		t.Fatal("the daemon idled out with a keepalive client connected")
	case <-time.After(300 * time.Millisecond):
	}
}

// nextDelta reads until a state.delta notification arrives and decodes it.
func (cl *testClient) nextDelta() state.Delta {
	cl.t.Helper()
	var d state.Delta
	msg := cl.nextNotification(rpc.MethodStateDelta)
	if err := json.Unmarshal(msg.Params, &d); err != nil {
		cl.t.Fatalf("decoding state.delta: %v", err)
	}
	return d
}

// subscribeAndSettle subscribes and waits for the delta the subscription's own
// wake produces, so a test that needs the ports on screen has them. The
// subscribe reply itself is the cached snapshot and may still be empty.
func (cl *testClient) subscribeAndSettle(p rpc.StateSubscribeParams) state.Delta {
	cl.t.Helper()
	if e := cl.call("state.subscribe", p, nil); e != nil {
		cl.t.Fatalf("state.subscribe: %v", e)
	}
	return cl.nextDelta()
}

// TestStatusReportsConfiguredIntervals: `daemon.scan_interval` and
// `daemon.stats_interval` are both read once at startup, so `daemon.status` is
// the only way for a client — or for the person who just edited the config file
// and restarted — to learn which values the running daemon is actually on.
func TestStatusReportsConfiguredIntervals(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	hn := newHarness(t, ctx, func(o *scanner.Options) {
		o.ScanInterval = 4 * time.Second
		o.StatsInterval = 300 * time.Millisecond
	})
	cl := hn.dial(ctx)

	var status rpc.DaemonStatusResult
	if e := cl.call("daemon.status", rpc.Empty{}, &status); e != nil {
		t.Fatalf("asking a running daemon for its status: %v", e)
	}
	if status.ScanBaseIntervalMs != 4000 {
		t.Errorf("scan_base_interval_ms = %d, want the configured 4000", status.ScanBaseIntervalMs)
	}
	if status.StatsIntervalMs != 300 {
		t.Errorf("stats_interval_ms = %d, want the configured 300", status.StatsIntervalMs)
	}
	// The adaptive interval is a number of its own, and still starts at the base
	// rather than at the 2 s constant.
	if status.ScanIntervalMs < status.ScanBaseIntervalMs {
		t.Errorf("scan_interval_ms = %d, want at least the %dms base",
			status.ScanIntervalMs, status.ScanBaseIntervalMs)
	}
}

// TestDroppedConnectionReclaimsItsSubscription: a subscriber that vanishes
// without unsubscribing must not keep the loop scanning, and must not block the
// fan-out to a live subscriber. The connection is closed under the server — no
// state.unsubscribe — which is what a killed CLI or a dead app leaves behind.
func TestDroppedConnectionReclaimsItsSubscription(t *testing.T) {
	for _, warm := range []bool{false, true} {
		t.Run(fmt.Sprintf("cached=%v", warm), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			scanStarted, unblock := make(chan struct{}), make(chan struct{})
			var once sync.Once
			hn := newHarness(t, ctx, func(o *scanner.Options) {
				scan := o.Scan
				o.Scan = func(include scanner.Include) ([]ports.ListeningPort, error) {
					rows, err := scan(include)
					once.Do(func() {
						close(scanStarted)
						select {
						case <-unblock:
						case <-ctx.Done():
						}
					})
					return rows, err
				}
			})
			// Cancel before the harness waits for the blocked collector on failure.
			t.Cleanup(cancel)
			hn.setRows(ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"})
			if warm {
				close(unblock)
				if _, err := hn.loop.Rescan(scanner.Include{}); err != nil {
					t.Fatal(err)
				}
			}
			gone := hn.dial(ctx)
			if e := gone.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, nil); e != nil {
				t.Fatalf("state.subscribe: %v", e)
			}
			if got := hn.srv.Subscribers(); got != 1 {
				t.Fatalf("Subscribers() = %d after subscribe, want 1", got)
			}
			select {
			case <-scanStarted:
			case <-time.After(settle):
				t.Fatal("subscription never woke the collector")
			}
			gone.conn.Close()
			deadline := time.Now().Add(settle)
			for hn.srv.Subscribers() != 0 && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := hn.srv.Subscribers(); got != 0 {
				t.Fatalf("Subscribers() = %d after drop, want 0", got)
			}

			live := hn.dial(ctx)
			var snap state.Snapshot
			if e := live.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
				t.Fatalf("live state.subscribe: %v", e)
			}
			if !warm {
				if len(snap.Ports) != 0 {
					t.Fatal("blocked first scan unexpectedly entered subscription snapshot")
				}
				// Force the initial 8123 delta to be pending on the live stream.
				// It is valid and must be applied, not mistaken for the next change.
				close(unblock)
				deadline = time.Now().Add(settle)
				for len(hn.loop.Cached().Ports) == 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if len(hn.loop.Cached().Ports) != 1 {
					t.Fatal("initial collector never published")
				}
			}
			hn.setRows(
				ports.ListeningPort{Port: 8123, BindAddress: "127.0.0.1", PID: 42, Process: "python3"},
				ports.ListeningPort{Port: 8124, BindAddress: "127.0.0.1", PID: 43, Process: "worker"},
			)
			want, err := hn.loop.Rescan(scanner.Include{})
			if err != nil {
				t.Fatalf("rescan: %v", err)
			}
			added := 0
			for snap.Seq < want.Seq {
				delta := live.nextDelta()
				if delta.Seq <= snap.Seq {
					t.Fatalf("non-monotonic delta %d after %d", delta.Seq, snap.Seq)
				}
				for _, row := range delta.Ports.Added {
					if row.Port == 8124 {
						added++
					}
				}
				snap = state.Apply(snap, delta)
			}
			if added != 1 || !reflect.DeepEqual(snap.Ports, want.Ports) {
				t.Fatalf("reconstructed ports = %+v, want %+v; new port delivered %d times", snap.Ports, want.Ports, added)
			}
		})
	}
}
