package runtimesvc

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// errInjectedMarshal stands in for the encoding failure the subscribe contract
// has to answer, so a test can tell the injected fault from a real one.
var errInjectedMarshal = errors.New("injected marshal failure")

// stubMarshalFailure makes every hand-framed subscribe reply fail to encode
// until restore is called. No snapshot the daemon can produce makes
// encoding/json refuse to encode, so the failure is injected at the seam rather
// than forced through the wire. Nothing in this package runs in parallel, so the
// stub cannot leak into a concurrent test; restore is idempotent.
func stubMarshalFailure(t *testing.T) (restore func()) {
	t.Helper()
	original := marshalSnapshot
	marshalSnapshot = func(any) ([]byte, error) { return nil, errInjectedMarshal }
	restore = func() { marshalSnapshot = original }
	t.Cleanup(restore)
	return restore
}

// subscriberOn returns the daemon side of the one open connection, with the
// subscription fields read under the lock that guards them.
func subscriberOn(t *testing.T, s *Server) (conn *Conn, subscribed bool) {
	t.Helper()
	s.subsMu.RLock()
	defer s.subsMu.RUnlock()
	for _, candidate := range s.conns {
		return candidate, candidate.subscribed
	}
	t.Fatal("the connection was not registered")
	return nil, false
}

// TestSubscribeMarshalFailureAnswersAndRegistersNothing pins the contract of the
// one reply the daemon frames by hand. A snapshot that cannot be encoded is an
// engine fault: the request must be answered with an internal error, and the
// connection must not be left subscribed. A client told its subscribe failed but
// kept on the subscriber list would have the scanner keep collecting demand for
// a stream it has no opening sequence for, and would never learn why nothing
// arrived.
func TestSubscribeMarshalFailureAnswersAndRegistersNothing(t *testing.T) {
	restoreDone := stubMarshalFailure(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	failure := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, nil)
	if failure == nil {
		t.Fatal("state.subscribe reported success while its snapshot could not be encoded")
	}
	if failure.Code != rpc.CodeInternal {
		t.Errorf("error code = %d, want %d (internal)", failure.Code, rpc.CodeInternal)
	}
	if !strings.Contains(failure.Message, "marshalling snapshot") {
		t.Errorf("error message = %q, want the marshalling snapshot prefix", failure.Message)
	}

	conn, subscribed := subscriberOn(t, h.srv)
	if subscribed {
		t.Errorf("c.subscribed = true after a failed subscribe, want false")
	}
	if got := h.srv.Subscribers(); got != 0 {
		t.Errorf("Subscribers() = %d after a failed subscribe, want 0", got)
	}
	if got := h.srv.Clients(); got != 1 {
		t.Errorf("Clients() = %d after a failed subscribe, want 1: the connection itself stays open", got)
	}

	// The failed attempt must leave the connection usable rather than
	// half-registered: once the reply can be encoded again, the same connection
	// subscribes for real.
	restoreDone()
	var snap state.Snapshot
	if e := c.call("state.subscribe", rpc.StateSubscribeParams{Events: true}, &snap); e != nil {
		t.Fatalf("state.subscribe after a marshal failure: %v", e)
	}
	if got := h.srv.Subscribers(); got != 1 {
		t.Errorf("Subscribers() = %d after the retried subscribe, want 1", got)
	}
	if _, subscribed := subscriberOn(t, h.srv); !subscribed || conn == nil {
		t.Error("the retried subscribe did not register the connection")
	}
}

// TestFailedMarshalLeavesNoWorkspaceBinding pins the other half of "nothing was
// registered": the workspace registry the call touched. A failed subscribe must
// leave the registry exactly as it found it — no member for a subscription that
// was answered with an error, and no lost member for the relation it replaced.
// A leftover binding is not inert: effectiveScopeLocked unions every member's
// selectors into every other client's view of that workspace, and forgetConn
// detaches through c.scope, which still names the old relation, so nothing would
// ever collect the stale member.
func TestFailedMarshalLeavesNoWorkspaceBinding(t *testing.T) {
	t.Run("first subscribe", func(t *testing.T) {
		s := New(Options{Socket: "/test/subscribe-marshal-first.sock", Version: "test"})
		c := newConn(1, s, nil)
		restoreDone := stubMarshalFailure(t)
		wanted := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}
		if err := s.subscribe(c, json.RawMessage("1"), scanner.Include{}, true, wanted, 0); err != nil {
			t.Fatalf("subscribe with an unencodable snapshot: %v, want the error on the wire only", err)
		}
		restoreDone()

		s.subsMu.RLock()
		_, registered := s.workspaceScopes["integration"][c.id]
		s.subsMu.RUnlock()
		if registered {
			t.Error("a failed first subscribe left a workspace member behind")
		}
	})

	t.Run("switch workspace", func(t *testing.T) {
		s := New(Options{Socket: "/test/subscribe-marshal-switch.sock", Version: "test"})
		c := newConn(1, s, nil)
		first := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}
		if err := s.subscribe(c, json.RawMessage("1"), scanner.Include{}, true, first, 0); err != nil {
			t.Fatalf("initial subscribe: %v", err)
		}

		restoreDone := stubMarshalFailure(t)
		next := state.Scope{Workspace: "delivery", Worktrees: []string{"/code/example-dashboard"}}
		if err := s.subscribe(c, json.RawMessage("2"), scanner.Include{}, true, next, 0); err != nil {
			t.Fatalf("resubscribe with an unencodable snapshot: %v, want the error on the wire only", err)
		}
		restoreDone()

		s.subsMu.RLock()
		_, joinedNew := s.workspaceScopes["delivery"][c.id]
		keptOld, ok := s.workspaceScopes["integration"][c.id]
		s.subsMu.RUnlock()
		if joinedNew {
			t.Error("a failed resubscribe left the connection in the workspace it never subscribed to")
		}
		if !ok || keptOld.Key() != first.Concrete().Key() {
			t.Errorf("old workspace member = %+v/%v, want the pre-existing %+v", keptOld, ok, first)
		}
	})
}

// TestFailedMarshalResubscribeKeepsWorkspaceRelation covers what a failed
// subscribe does leave behind: a resubscribe detaches the caller's old workspace
// binding, validates and frames the new reply, and only then would it attach the
// new binding. A failure while framing therefore restores the pre-existing
// relation rather than keeping the new one. That relation is ephemeral state
// outside the sequence, and losing it on a failed resubscribe would change which
// worktrees this connection is entitled to see on its next attempt.
func TestFailedMarshalResubscribeKeepsWorkspaceRelation(t *testing.T) {
	s := New(Options{Socket: "/test/subscribe-marshal.sock", Version: "test"})
	c := newConn(1, s, nil)
	explicit := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}
	switched := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-dashboard"}}
	if err := s.subscribe(c, json.RawMessage("1"), scanner.Include{}, true, explicit, 0); err != nil {
		t.Fatalf("initial subscribe: %v", err)
	}

	restoreDone := stubMarshalFailure(t)
	if err := s.subscribe(c, json.RawMessage("2"), scanner.Include{}, true, switched, 0); err != nil {
		t.Fatalf("resubscribe with an unencodable snapshot: %v, want the error on the wire only", err)
	}
	restoreDone()

	s.subsMu.RLock()
	binding, ok := s.workspaceScopes["integration"][c.id]
	gotScope := c.scope
	subscribed := c.subscribed
	s.subsMu.RUnlock()
	if !ok || binding.Key() != explicit.Concrete().Key() {
		t.Errorf("workspace binding = %+v, want the pre-existing %+v", binding, explicit)
	}
	if gotScope.Key() != explicit.Key() || !subscribed {
		t.Errorf("connection scope/subscribed = %+v/%v, want the pre-existing relation left intact",
			gotScope, subscribed)
	}
}
