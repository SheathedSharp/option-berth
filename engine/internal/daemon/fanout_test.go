package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// unencodable fails json.Marshal on every field, which is the one thing a
// state.changed payload from this daemon cannot do. It stands in for an encoder
// fault so the fan-out's handling of one can be seen without a payload shape
// that the daemon could actually produce.
type unencodable struct{}

func (unencodable) MarshalJSON() ([]byte, error) { return nil, errUnencodable }

var errUnencodable = errors.New("injected unencodable payload")

// countNotifications makes every notification frame go through a counter, so a
// test can tell one encoding per view from one per subscriber. Nothing in this
// package runs in parallel, so the counter cannot leak into another test.
func countNotifications(t *testing.T) (encoded func() int, restore func()) {
	t.Helper()
	original := marshalNotification
	count := 0
	marshalNotification = func(method string, params any) ([]byte, error) {
		count++
		return original(method, params)
	}
	restore = func() { marshalNotification = original }
	t.Cleanup(restore)
	return func() int { return count }, restore
}

// scopedConnection is a registry entry with the subscription fields the fan-out
// reads, set the way state.subscribe sets them.
func scopedConnection(s *Server, id uint64, scope state.Scope) *Conn {
	c := newConn(id, s, nil)
	c.subscribed, c.events, c.scope = true, true, scope
	return c
}

// drain returns whatever was queued for a connection without blocking.
func drain(c *Conn) [][]byte {
	var out [][]byte
	for {
		select {
		case msg := <-c.out:
			out = append(out, msg)
		default:
			return out
		}
	}
}

// cachedGroups makes a loop whose cached snapshot already carries a real group
// row, so a standalone broadcast is fanned out against a non-empty state. It
// scans once through the injected collector; the loop is not started, so
// nothing else publishes behind the test's back.
func cachedGroups(t *testing.T) *scanner.Loop {
	t.Helper()
	dir := t.TempDir()
	body := "name: example\nservices:\n  - name: api\n    port: 8000\n"
	if err := os.WriteFile(filepath.Join(dir, ".oberth.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	loop := scanner.New(scanner.Options{
		DaemonVersion: "test",
		Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
			return []ports.ListeningPort{{Port: 8000, PID: 42, Process: "python3", Cwd: dir}}, nil
		},
	})
	if _, err := loop.Snapshot(scanner.Include{}); err != nil {
		t.Fatalf("populating the cached snapshot: %v", err)
	}
	if len(loop.Cached().Groups) == 0 {
		t.Fatal("test setup: the cached snapshot did not attribute the group")
	}
	return loop
}

// notificationMethod names the method of one already-queued frame.
func notificationMethod(t *testing.T, raw []byte) string {
	t.Helper()
	var msg rpc.Notification
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("decoding notification %q: %v", raw, err)
	}
	return msg.Method
}

// TestBroadcastEventSendsNoDeltaToUnscopedViewers pins that a standalone event
// is not a state transition. broadcastEvent exists for events that do not come
// from a scan — daemon_stopping, a bridged event — so an unscoped events
// subscriber must receive the state.event and nothing else. Reusing the
// transition path here would spread an empty previous snapshot against the
// cached groups and hand every subscriber a delta whose "added" list is the
// whole machine and whose "at" is empty: a change nobody observed.
func TestBroadcastEventSendsNoDeltaToUnscopedViewers(t *testing.T) {
	s := New(Options{Socket: "/test/broadcast.sock", Version: "test", Scanner: cachedGroups(t)})
	c := newConn(1, s, nil)
	c.subscribed, c.events = true, true
	s.registerConn(c)

	s.BroadcastEvent(state.Event{Kind: "probe", At: "now"})

	sawEvent := false
	for _, raw := range drain(c) {
		switch notificationMethod(t, raw) {
		case rpc.MethodStateDelta:
			t.Fatalf("an unscoped events subscriber received a state.delta for a standalone event: %s", raw)
		case rpc.MethodStateEvent:
			sawEvent = true
		}
	}
	if !sawEvent {
		t.Fatal("the standalone event was not delivered to a subscriber that asked for events")
	}
}

// TestEventAndDeltaViewersShareNoCachedFrames pins the fan-out cache against
// the one dimension a view's frames depend on but a naive key would omit. Two
// connections with the same include set and scope differ only in whether they
// asked for events; if the cache key omits that, whichever is visited first
// decides the frames both receive — one silently drops the event it subscribed
// to, the other receives an event it never asked for. The map order makes which
// one breaks random, so this asserts on both connections and fails whichever
// way the cache went wrong. It is therefore red before the key carries events
// and green after, with no sleeps and no retry loop.
func TestEventAndDeltaViewersShareNoCachedFrames(t *testing.T) {
	s := New(Options{Socket: "/test/fanout-events-key.sock", Version: "test"})
	root := "/code/example-worker"
	port := state.Port{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 101, ProjectRoot: &root}
	prev := state.Snapshot{Seq: 10, Groups: []state.Group{{Name: "example-worker", Repo: "example-worker", RootDir: &root}}}
	next := state.Snapshot{Seq: 11, At: "2026-09-28T00:00:11Z", Groups: prev.Groups, Ports: []state.Port{port}}
	events := []state.Event{{EventID: "11:port_up:localhost/18090", Seq: 11, Kind: "port_up", At: next.At, Port: &port}}

	deltaOnly := newConn(1, s, nil)
	deltaOnly.subscribed, deltaOnly.events = true, false
	eventsToo := newConn(2, s, nil)
	eventsToo.subscribed, eventsToo.events = true, true
	s.registerConn(deltaOnly)
	s.registerConn(eventsToo)

	s.publish(prev, next, events)

	for _, raw := range drain(deltaOnly) {
		if notificationMethod(t, raw) == rpc.MethodStateEvent {
			t.Fatalf("a delta-only subscriber received an event it never subscribed to: %s", raw)
		}
	}
	var sawDelta, sawEvent bool
	for _, raw := range drain(eventsToo) {
		switch notificationMethod(t, raw) {
		case rpc.MethodStateDelta:
			sawDelta = true
		case rpc.MethodStateEvent:
			sawEvent = true
		}
	}
	if !sawDelta || !sawEvent {
		t.Fatalf("events subscriber got delta=%v event=%v, want both", sawDelta, sawEvent)
	}
}

// TestUnscopedViewerDoesNotBuildTheScopedChangeSet pins the fan-out's cheapest
// early exit against the case it actually protects. A transition is published
// for every scan tick; an unscoped subscriber — the common legacy client that
// wants events but no scope — must not pay to build the redacted change set for
// scopes nobody is watching. Counting the builds, not the encodings, is what
// makes the guard observable: delete `if aud.scoped` and the change set is
// built for this subscriber, which fails here. The delta the subscriber did ask
// for must still arrive.
func TestUnscopedViewerDoesNotBuildTheScopedChangeSet(t *testing.T) {
	s := New(Options{Socket: "/test/fanout-unscoped.sock", Version: "test"})
	original := buildChangeSet
	builds := 0
	buildChangeSet = func(prev, next state.Snapshot, events []state.Event) changeSet {
		builds++
		return original(prev, next, events)
	}
	t.Cleanup(func() { buildChangeSet = original })

	c := newConn(1, s, nil)
	c.subscribed, c.events = true, true
	s.registerConn(c)

	root := "/code/example-worker"
	prev := state.Snapshot{Seq: 10}
	next := state.Snapshot{Seq: 11, Groups: []state.Group{{Name: "example-worker", Repo: "example-worker", RootDir: &root}}}
	s.publish(prev, next, nil)

	if builds != 0 {
		t.Fatalf("built %d scoped change sets with no scoped viewer, want 0", builds)
	}
	if len(drain(c)) == 0 {
		t.Fatal("the unscoped subscriber received nothing for a transition it watches")
	}
}

// TestOneScopedViewIsEncodedOnce pins the reason the fan-out caches at all: a
// room full of clients watching the same worktree must cost one encoding, not
// one each. The cache is keyed by what the client actually sees — the include
// set and the effective scope — so two clients with the same view share a frame
// and two with different views do not.
func TestOneScopedViewIsEncodedOnce(t *testing.T) {
	s := New(Options{Socket: "/test/fanout-cache.sock", Version: "test"})
	encoded, restore := countNotifications(t)

	root, other := "/code/example-worker", "/code/example-dashboard"
	sameView, sameViewAgain := state.Scope{Worktrees: []string{root}}, state.Scope{Worktrees: []string{root}}
	otherView := state.Scope{Worktrees: []string{other}}

	c1 := scopedConnection(s, 1, sameView)
	c2 := scopedConnection(s, 2, sameViewAgain)
	c3 := scopedConnection(s, 3, otherView)
	s.registerConn(c1)
	s.registerConn(c2)
	s.registerConn(c3)

	prev := state.Snapshot{Seq: 10}
	next := state.Snapshot{Seq: 11, At: "2026-09-28T00:00:11Z", Groups: []state.Group{
		{Name: "example-worker", Repo: "example-worker", RootDir: &root},
		{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &other},
	}}
	s.publish(prev, next, nil)

	// One encoding for the view c1 and c2 share; c3's view is different, so
	// its own. Nothing should be encoded per subscriber.
	if got := encoded(); got != 2 {
		t.Errorf("encoded %d notifications for two distinct views across three subscribers, want 2", got)
	}
	for i, c := range []*Conn{c1, c2, c3} {
		if len(drain(c)) == 0 {
			t.Errorf("connection %d received nothing for a transition inside its scope", i+1)
		}
	}
	restore()
}

// TestUnencodableScopedChangeIsDropped pins what the fan-out does when the
// encoder refuses a change: nothing. Sending a notification whose params are
// absent would hand the client a frame it cannot decode as a state.changed,
// and the correct behavior for a fault the daemon cannot describe on this
// channel is to drop the frame rather than emit a malformed one.
func TestUnencodableScopedChangeIsDropped(t *testing.T) {
	s := New(Options{Socket: "/test/fanout-encode.sock", Version: "test"})
	root := "/code/example-worker"
	c := scopedConnection(s, 1, state.Scope{Worktrees: []string{root}})
	s.registerConn(c)

	raw, err := marshalNotification(rpc.MethodStateChanged, unencodable{})
	if err == nil {
		t.Fatal("an unencodable payload was encoded into a notification")
	}
	if raw != nil {
		t.Errorf("a failed notification returned %q, want nil so the caller drops it", raw)
	}
}

// TestNotificationFrameNeverCarriesEmptyParams is the wire half of the same
// rule, checked on a real subscriber: a state.changed frame that arrives must
// carry decodable params, never an absent payload.
func TestNotificationFrameNeverCarriesEmptyParams(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	root := "/code/example-worker"
	scope := state.Scope{Worktrees: []string{root}}
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true, Scope: &scope}, nil); err != nil {
		t.Fatalf("scoped subscribe: %v", err)
	}
	prev := state.Snapshot{Seq: 10}
	next := state.Snapshot{Seq: 11, At: "2026-09-28T00:00:11Z",
		Groups: []state.Group{{Name: "example-worker", Repo: "example-worker", RootDir: &root}},
		Ports:  []state.Port{{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 101, ProjectRoot: &root}},
	}
	h.srv.publish(prev, next, []state.Event{{EventID: "11:port_up:localhost/18090", Seq: 11, Kind: "port_up", At: next.At, Port: &next.Ports[0]}})

	m := c.nextNotification(rpc.MethodStateChanged)
	if len(m.Params) == 0 {
		t.Fatal("state.changed arrived without params")
	}
	var change state.StateChanged
	if err := json.Unmarshal(m.Params, &change); err != nil {
		t.Fatalf("state.changed params: %v", err)
	}
	if change.Type != "state.changed" || change.Seq != 11 {
		t.Errorf("change = %+v, want a state.changed at seq 11", change)
	}
}
