package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestMarshalDeltaFiltersBeforeDiffing(t *testing.T) {
	selectedRoot := "/code/example-worker"
	otherRoot := "/code/example-dashboard"
	prev := state.Snapshot{
		Seq: 1,
		Groups: []state.Group{
			{Name: "example-worker", Repo: "example-worker", RootDir: &selectedRoot},
			{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &otherRoot},
		},
		Ports: []state.Port{
			{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 10, Group: stringPtr("example-worker"), ProjectRoot: &selectedRoot},
			{Host: "localhost", Port: 19090, BindAddress: "127.0.0.1", PID: 11, Group: stringPtr("example-dashboard"), ProjectRoot: &otherRoot},
		},
	}
	next := prev
	next.Seq = 2
	next.Ports = append([]state.Port(nil), prev.Ports...)
	next.Ports[0].PID = 12
	next.Ports[1].PID = 13

	raw := marshalDelta(prev, next, scanner.Include{}, state.Scope{Worktrees: []string{selectedRoot}})
	if raw == nil {
		t.Fatal("marshalDelta returned nil for a selected change")
	}
	var msg rpc.Notification
	if err := json.Unmarshal(raw, &msg); err != nil {
		t.Fatalf("notification: %v", err)
	}
	var delta state.Delta
	if err := json.Unmarshal(msg.Params, &delta); err != nil {
		t.Fatalf("delta: %v", err)
	}
	if len(delta.Ports.Added) != 1 || delta.Ports.Added[0].Port != 18090 {
		t.Fatalf("filtered added = %+v, want only example-worker", delta.Ports.Added)
	}
	if len(delta.Ports.Removed) != 1 || delta.Ports.Removed[0] != "18090:127.0.0.1" {
		t.Fatalf("filtered removed = %v, want only example-worker", delta.Ports.Removed)
	}
}

func TestScopedChangesRouteOnlySelectedWorktreeAndStayRedacted(t *testing.T) {
	selectedRoot := "/code/example-worker"
	otherRoot := "/code/example-dashboard"
	selected := state.Event{
		Kind: "port_up", At: "now",
		Port: &state.Port{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", ProjectRoot: &selectedRoot, Command: "npm run dev", Cwd: selectedRoot},
	}
	other := state.Event{
		Kind: "port_up", At: "now",
		Port: &state.Port{Host: "localhost", Port: 19090, BindAddress: "127.0.0.1", ProjectRoot: &otherRoot, Command: "go run .", Cwd: otherRoot},
	}
	groups := []state.Group{
		{Name: "example-worker", Repo: "example-worker", RootDir: &selectedRoot},
		{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &otherRoot},
	}
	changes := []state.StateChanged{
		state.StateChangedForEvent(selected, 8, groups),
		state.StateChangedForEvent(other, 8, groups),
	}
	raw := marshalStateChanges(changes, state.Scope{Worktrees: []string{selectedRoot}}, groups)
	if len(raw) != 1 {
		t.Fatalf("routed notifications = %d, want one", len(raw))
	}
	var msg rpc.Notification
	if err := json.Unmarshal(raw[0], &msg); err != nil {
		t.Fatal(err)
	}
	if msg.Method != rpc.MethodStateChanged {
		t.Fatalf("method = %q, want %q", msg.Method, rpc.MethodStateChanged)
	}
	text := string(msg.Params)
	for _, forbidden := range []string{"npm run dev", selectedRoot, "cwd", "command"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("scoped change contains %q: %s", forbidden, text)
		}
	}
	var change state.StateChanged
	if err := json.Unmarshal(msg.Params, &change); err != nil {
		t.Fatal(err)
	}
	if change.Source == nil || change.Source.WorktreeID == "" || len(change.Changed) != 1 || change.Changed[0] != "port_changed" {
		t.Fatalf("change = %+v", change)
	}
}

func TestWorkspaceBindingUnionsExplicitRelations(t *testing.T) {
	s := New(Options{Socket: "/test/workspace.sock", Version: "test"})
	c1 := newConn(1, s, nil)
	c2 := newConn(2, s, nil)
	s.subsMu.Lock()
	if _, err := s.attachWorkspaceLocked(c1, state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}); err != nil {
		s.subsMu.Unlock()
		t.Fatal(err)
	}
	if _, err := s.attachWorkspaceLocked(c2, state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-dashboard"}}); err != nil {
		s.subsMu.Unlock()
		t.Fatal(err)
	}
	got := s.effectiveScopeLocked(state.Scope{Workspace: "integration"})
	s.subsMu.Unlock()
	if !got.MatchGroup(state.Group{Name: "example-worker", Repo: "example-worker", RootDir: stringPtr("/code/example-worker")}) ||
		!got.MatchGroup(state.Group{Name: "example-dashboard", Repo: "example-dashboard", RootDir: stringPtr("/code/example-dashboard")}) {
		t.Fatalf("workspace scope = %+v", got)
	}
}

func TestFailedWorkspaceResubscribeKeepsExistingBinding(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-resubscribe.sock", Version: "test"})
	c := newConn(1, s, nil)
	explicit := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}
	scopeOnly := state.Scope{Workspace: "integration"}
	if err := s.subscribe(c, json.RawMessage("1"), scanner.Include{}, true, explicit, 0); err != nil {
		t.Fatalf("initial subscribe: %v", err)
	}
	if err := s.subscribe(c, json.RawMessage("2"), scanner.Include{}, true, scopeOnly, 0); err == nil {
		t.Fatal("workspace-only resubscribe unexpectedly succeeded without another member")
	}
	s.subsMu.RLock()
	binding, ok := s.workspaceScopes["integration"][c.id]
	gotScope := c.scope
	s.subsMu.RUnlock()
	if !ok || binding.Key() != explicit.Concrete().Key() || gotScope.Key() != explicit.Key() {
		t.Fatalf("failed resubscribe dropped old relation: binding=%+v scope=%+v", binding, gotScope)
	}
}

func TestScopedSubscribeUsesRedactedStateChangedWire(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	h := newHarness(t, ctx)
	c := h.dial(ctx)

	selectedRoot := "/code/example-worker"
	otherRoot := "/code/example-dashboard"
	selectedGroup := "example-worker"
	otherGroup := "example-dashboard"
	scope := state.Scope{Worktrees: []string{selectedRoot}}
	var opening state.StreamSnapshot
	if err := c.call("state.subscribe", rpc.StateSubscribeParams{
		Events: true,
		Scope:  &scope,
	}, &opening); err != nil {
		t.Fatalf("scoped state.subscribe: %v", err)
	}
	if opening.Type != "state.snapshot" {
		t.Fatalf("opening type = %q, want state.snapshot", opening.Type)
	}

	prev := state.Snapshot{Seq: 10, Groups: []state.Group{
		{Name: selectedGroup, Repo: selectedGroup, RootDir: &selectedRoot},
		{Name: otherGroup, Repo: otherGroup, RootDir: &otherRoot},
	}}
	next := state.Snapshot{Seq: 11, At: "2026-09-28T00:00:11Z", Groups: prev.Groups, Ports: []state.Port{
		{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 101,
			Group: &selectedGroup, ProjectRoot: &selectedRoot, Command: "npm run dev", Cwd: selectedRoot},
		{Host: "localhost", Port: 19090, BindAddress: "127.0.0.1", PID: 102,
			Group: &otherGroup, ProjectRoot: &otherRoot, Command: "go run .", Cwd: otherRoot},
	}}
	h.srv.publish(prev, next, []state.Event{
		{EventID: "11:port_up:localhost/18090", Seq: 11, Kind: "port_up", At: next.At,
			Port: &next.Ports[0]},
		{EventID: "11:port_up:localhost/19090", Seq: 11, Kind: "port_up", At: next.At,
			Port: &next.Ports[1]},
	})

	m := c.nextNotification(rpc.MethodStateChanged)
	var change state.StateChanged
	if err := json.Unmarshal(m.Params, &change); err != nil {
		t.Fatalf("state.changed: %v", err)
	}
	if change.Source == nil || change.Source.WorktreeID == "" || change.Seq != 11 {
		t.Fatalf("change = %+v", change)
	}
	if len(change.Changed) != 1 || change.Changed[0] != "port_changed" {
		t.Fatalf("changed = %v, want [port_changed]", change.Changed)
	}
	raw := string(m.Params)
	for _, forbidden := range []string{"npm run dev", selectedRoot, "go run .", otherRoot, "command", "cwd"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("scoped state.changed contains %q: %s", forbidden, raw)
		}
	}
}

func TestScopedSubscribeRequiresEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c := newHarness(t, ctx).dial(ctx)
	root := "/code/example-worker"
	err := c.call("state.subscribe", rpc.StateSubscribeParams{
		Scope: &state.Scope{Worktrees: []string{root}},
	}, nil)
	if err == nil || err.Code != rpc.CodeInvalidParams {
		t.Fatalf("scoped subscribe without events = %+v, want invalid_params", err)
	}
}

func stringPtr(v string) *string { return &v }
