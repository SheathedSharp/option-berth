package daemon

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
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

	raw := deltaFrame(prev, next, viewSpec{scope: state.Scope{Worktrees: []string{selectedRoot}}})
	if raw == nil {
		t.Fatal("deltaFrame returned nil for a selected change")
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
	s.bindWorkspaceLocked(c1, state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}})
	s.bindWorkspaceLocked(c2, state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-dashboard"}})
	got := s.effectiveScopeLocked(state.Scope{Workspace: "integration"})
	s.subsMu.Unlock()
	if !got.MatchGroup(state.Group{Name: "example-worker", Repo: "example-worker", RootDir: stringPtr("/code/example-worker")}) ||
		!got.MatchGroup(state.Group{Name: "example-dashboard", Repo: "example-dashboard", RootDir: stringPtr("/code/example-dashboard")}) {
		t.Fatalf("workspace scope = %+v", got)
	}
}

// TestWorkspaceOnlyResubscribeInheritsOnlyPeers pins the boundary a workspace
// member crosses when it stops naming its own selectors. Two explicit members
// A and B share a workspace; A resubscribes with the workspace label and no
// selector. That is legal — B still defines the relation — and A must now see
// exactly B's rows: its own old selector is gone, or switching to workspace-only
// would silently keep a worktree the relation no longer names. The companion
// case, where A is the only member and the same request fails with its old
// binding intact, is TestFailedWorkspaceResubscribeKeepsExistingBinding.
func TestWorkspaceOnlyResubscribeInheritsOnlyPeers(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-inherit.sock", Version: "test"})
	memberA := newConn(1, s, nil)
	memberB := newConn(2, s, nil)
	aWorker := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-worker"}}
	bDashboard := state.Scope{Workspace: "integration", Worktrees: []string{"/code/example-dashboard"}}
	if err := s.subscribe(memberA, json.RawMessage("1"), scanner.Include{}, true, aWorker, 0); err != nil {
		t.Fatalf("subscribing member A: %v", err)
	}
	if err := s.subscribe(memberB, json.RawMessage("2"), scanner.Include{}, true, bDashboard, 0); err != nil {
		t.Fatalf("subscribing member B: %v", err)
	}

	workspaceOnly := state.Scope{Workspace: "integration"}
	if err := s.subscribe(memberA, json.RawMessage("3"), scanner.Include{}, true, workspaceOnly, 0); err != nil {
		t.Fatalf("workspace-only resubscribe with a peer member: %v", err)
	}

	s.subsMu.RLock()
	effective := s.effectiveScopeLocked(memberA.scope)
	s.subsMu.RUnlock()
	worker := state.Group{Name: "example-worker", Repo: "example-worker", RootDir: stringPtr("/code/example-worker")}
	dashboard := state.Group{Name: "example-dashboard", Repo: "example-dashboard", RootDir: stringPtr("/code/example-dashboard")}
	if effective.MatchGroup(worker) {
		t.Fatalf("A still sees its dropped selector after switching to workspace-only: %+v", effective)
	}
	if !effective.MatchGroup(dashboard) {
		t.Fatalf("A does not inherit its peer's selector: %+v", effective)
	}
}

// TestWorkspaceChangeReachesOnlyPeers pins the membership-refresh broadcast: a
// state.changed with Changed ["workspace_changed"] goes to the live event
// viewers of that workspace and no one else. The connection that caused the
// change is excluded (it already got its own opening snapshot), a member of a
// different workspace or a viewer without events must not be woken, and an
// empty workspace name names no peers and publishes nothing at all.
func TestWorkspaceChangeReachesOnlyPeers(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-change.sock", Version: "test"})
	conn := func(id uint64, workspace string, events bool) *Conn {
		c := newConn(id, s, nil)
		c.subscribed, c.events, c.scope = true, events, state.Scope{Workspace: workspace}
		s.registerConn(c)
		return c
	}
	peer := conn(1, "integration", true)
	excluded := conn(2, "integration", true)
	otherWorkspace := conn(3, "delivery", true)
	silent := conn(4, "integration", false)

	s.broadcastWorkspaceChanged("integration", excluded)

	frames := drain(peer)
	if len(frames) != 1 {
		t.Fatalf("peer received %d frames, want one workspace_changed", len(frames))
	}
	var msg rpc.Notification
	if err := json.Unmarshal(frames[0], &msg); err != nil {
		t.Fatalf("notification: %v", err)
	}
	if msg.Method != rpc.MethodStateChanged {
		t.Fatalf("method = %q, want %q", msg.Method, rpc.MethodStateChanged)
	}
	var change state.StateChanged
	if err := json.Unmarshal(msg.Params, &change); err != nil {
		t.Fatalf("state.changed: %v", err)
	}
	if change.Type != "state.changed" || len(change.Changed) != 1 || change.Changed[0] != "workspace_changed" {
		t.Fatalf("change = %+v, want a workspace_changed summary", change)
	}
	for name, c := range map[string]*Conn{"excluded": excluded, "other workspace": otherWorkspace, "silent": silent} {
		if got := drain(c); len(got) != 0 {
			t.Errorf("%s received %d frames, want none", name, len(got))
		}
	}
	s.broadcastWorkspaceChanged("   ", nil)
}

// TestWorkspaceBindingReportsOnlyAChangedRelation pins the write half of a
// workspace subscription: a scope with no workspace, or a workspace label with
// no selectors, binds nothing; the first concrete binding and a changed one are
// reported as a peer-visible change, while re-binding the same relation is not.
func TestWorkspaceBindingReportsOnlyAChangedRelation(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-bind.sock", Version: "test"})
	c := newConn(1, s, nil)
	if s.bindWorkspaceLocked(c, state.Scope{}) {
		t.Error("an empty scope was bound and reported as a change")
	}
	if s.bindWorkspaceLocked(c, state.Scope{Workspace: "integration"}) {
		t.Error("a workspace-only scope with no selectors was bound")
	}
	first := state.Scope{Workspace: "integration", Worktrees: []string{"/w/a"}}
	if !s.bindWorkspaceLocked(c, first) {
		t.Error("the first concrete binding was not reported as a change")
	}
	if s.bindWorkspaceLocked(c, first) {
		t.Error("re-binding the same relation was reported as a change")
	}
	second := state.Scope{Workspace: "integration", Worktrees: []string{"/w/b"}}
	if !s.bindWorkspaceLocked(c, second) {
		t.Error("a changed relation was not reported as a change")
	}
	s.subsMu.RLock()
	binding := s.workspaceScopes["integration"][c.id]
	s.subsMu.RUnlock()
	if binding.Key() != second.Concrete().Key() {
		t.Errorf("stored binding = %+v, want the latest relation", binding)
	}
}

// TestDetachWorkspaceReportsOnlyAHeldRelation pins what detach returns: no
// workspace or no binding was not held, so a caller must not restore or
// broadcast on its behalf. Only an actual member is removed and reported held.
func TestDetachWorkspaceReportsOnlyAHeldRelation(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-detach.sock", Version: "test"})
	c := newConn(1, s, nil)
	if workspace, _, held := s.detachWorkspaceLocked(c); workspace != "" || held {
		t.Fatalf("detaching a connection with no workspace = %q/%v, want empty/false", workspace, held)
	}
	c.scope = state.Scope{Workspace: "integration"}
	if workspace, _, held := s.detachWorkspaceLocked(c); workspace != "integration" || held {
		t.Fatalf("detaching a workspace with no bindings = %q/%v, want integration/false", workspace, held)
	}
	scope := state.Scope{Workspace: "integration", Worktrees: []string{"/w/a"}}
	s.bindWorkspaceLocked(c, scope)
	workspace, previous, held := s.detachWorkspaceLocked(c)
	if !held || workspace != "integration" || previous.Key() != scope.Concrete().Key() {
		t.Fatalf("detaching a held binding = %q/%+v/%v, want the stored relation", workspace, previous, held)
	}
}

// TestRestoreWorkspaceIgnoresNothingToRestore pins the undo rule: a rollback
// only puts back a relation that was actually detached. An empty workspace or a
// binding that was never held must leave no member behind.
func TestRestoreWorkspaceIgnoresNothingToRestore(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-restore.sock", Version: "test"})
	c := newConn(1, s, nil)
	s.restoreWorkspaceLocked(c, "", state.Scope{}, false)
	s.restoreWorkspaceLocked(c, "integration", state.Scope{}, false)
	if _, ok := s.workspaceScopes["integration"]; ok {
		t.Fatal("a rollback restored a relation that was never held")
	}
	previous := state.Scope{Worktrees: []string{"/w/a"}}
	s.restoreWorkspaceLocked(c, "integration", previous, true)
	s.subsMu.RLock()
	stored, ok := s.workspaceScopes["integration"][c.id]
	s.subsMu.RUnlock()
	if !ok || stored.Key() != previous.Key() {
		t.Fatalf("restored binding = %+v/%v, want the detached relation", stored, ok)
	}
}

// TestWorkspaceChangeEventIDsAreUnique pins that each membership change is a
// distinct event: two changes must not share an EventID, or a client could drop
// the second as a duplicate.
func TestWorkspaceChangeEventIDsAreUnique(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-ids.sock", Version: "test"})
	first := s.workspaceChange("integration")
	second := s.workspaceChange("integration")
	if first.EventID == "" || first.EventID == second.EventID {
		t.Fatalf("workspace change ids = %q and %q, want two distinct ids", first.EventID, second.EventID)
	}
}

// TestResubscribeAnnouncesTheRightWorkspacePeers pins which peers are told to
// refresh when a member changes its relation. Staying in the same workspace is
// one announcement, not two; moving away tells the workspace it left and the
// one it joined, each once.
func TestResubscribeAnnouncesTheRightWorkspacePeers(t *testing.T) {
	s := New(Options{Socket: "/test/workspace-announce.sock", Version: "test"})
	integrationPeer := newConn(1, s, nil)
	integrationPeer.subscribed, integrationPeer.events = true, true
	integrationPeer.scope = state.Scope{Workspace: "integration"}
	deliveryPeer := newConn(2, s, nil)
	deliveryPeer.subscribed, deliveryPeer.events = true, true
	deliveryPeer.scope = state.Scope{Workspace: "delivery"}
	s.registerConn(integrationPeer)
	s.registerConn(deliveryPeer)

	member := newConn(3, s, nil)
	first := state.Scope{Workspace: "integration", Worktrees: []string{"/w/a"}}
	second := state.Scope{Workspace: "integration", Worktrees: []string{"/w/b"}}
	moved := state.Scope{Workspace: "delivery", Worktrees: []string{"/w/d"}}
	if err := s.subscribe(member, json.RawMessage("1"), scanner.Include{}, true, first, 0); err != nil {
		t.Fatalf("joining the integration workspace: %v", err)
	}
	drain(integrationPeer) // the join itself is announced once

	if err := s.subscribe(member, json.RawMessage("2"), scanner.Include{}, true, second, 0); err != nil {
		t.Fatalf("resubscribing inside the integration workspace: %v", err)
	}
	if got := drain(integrationPeer); len(got) != 1 {
		t.Fatalf("same-workspace resubscribe announced %d times to a peer, want 1", len(got))
	}
	if got := drain(deliveryPeer); len(got) != 0 {
		t.Fatalf("same-workspace resubscribe reached an unrelated workspace %d times", len(got))
	}

	if err := s.subscribe(member, json.RawMessage("3"), scanner.Include{}, true, moved, 0); err != nil {
		t.Fatalf("moving to the delivery workspace: %v", err)
	}
	if got := drain(integrationPeer); len(got) != 1 {
		t.Fatalf("moving away announced %d times to the old workspace, want 1", len(got))
	}
	if got := drain(deliveryPeer); len(got) != 1 {
		t.Fatalf("moving in announced %d times to the new workspace, want 1", len(got))
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
