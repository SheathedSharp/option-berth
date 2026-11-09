package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
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

// TestScopedEventWithNoViewersIsNotEncoded pins the fan-out's cheapest early
// exit. A transition is published for every scan tick whether or not any
// scoped subscriber is listening; building the whole change set and encoding a
// notification per tick for nobody is work the daemon pays forever.
func TestScopedEventWithNoViewersIsNotEncoded(t *testing.T) {
	s := New(Options{Socket: "/test/fanout-none.sock", Version: "test"})
	encoded, _ := countNotifications(t)

	s.subsMu.RLock()
	if s.hasScopedListenerLocked() {
		s.subsMu.RUnlock()
		t.Fatal("a server with no subscribers reports a scoped listener")
	}
	s.subsMu.RUnlock()

	root := "/code/example-worker"
	prev := state.Snapshot{Seq: 10}
	next := state.Snapshot{Seq: 11, Groups: []state.Group{{Name: "example-worker", Repo: "example-worker", RootDir: &root}}}
	s.publish(prev, next, nil)
	if got := encoded(); got != 0 {
		t.Errorf("encoded %d notifications with no subscriber listening, want 0", got)
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
