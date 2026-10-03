package state

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScopeFiltersRelatedRowsAndSessions(t *testing.T) {
	mainRoot := "/code/example-worker"
	hotRoot := "/code/example-dashboard"
	otherRoot := "/code/unrelated"
	mainSession := Session{ID: "example-worker-session", Worktree: "main"}
	hotSession := Session{ID: "example-dashboard-session", Worktree: "main"}
	otherSession := Session{ID: "other-session", Worktree: "main"}

	snap := Snapshot{
		Seq: 9,
		Groups: []Group{
			{Name: "example-worker", Repo: "example-worker", RootDir: &mainRoot, Services: []Service{{Name: "api"}}},
			{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &hotRoot, Services: []Service{{Name: "worker"}}},
			{Name: "unrelated", Repo: "unrelated", RootDir: &otherRoot},
		},
		Ports: []Port{
			{Port: 18090, Group: stringPtr("example-worker"), ProjectRoot: &mainRoot, Session: &mainSession},
			{Port: 19090, Group: stringPtr("example-dashboard"), ProjectRoot: &hotRoot, Session: &hotSession},
			{Port: 19999, Group: stringPtr("unrelated"), ProjectRoot: &otherRoot, Session: &otherSession},
		},
		Sessions: []SessionRecord{
			{Session: mainSession}, {Session: hotSession}, {Session: otherSession},
		},
	}

	filtered := (Scope{Worktrees: []string{mainRoot, hotRoot}}).FilterSnapshot(snap)
	if len(filtered.Groups) != 2 || len(filtered.Ports) != 2 || len(filtered.Sessions) != 2 {
		t.Fatalf("filtered snapshot = groups %d, ports %d, sessions %d; want 2/2/2", len(filtered.Groups), len(filtered.Ports), len(filtered.Sessions))
	}
	if filtered.Seq != snap.Seq {
		t.Fatalf("filtered seq = %d, want %d", filtered.Seq, snap.Seq)
	}
}

func TestScopeSelectorsAreAdditiveAndStable(t *testing.T) {
	if !(Scope{Worktrees: []string{"  "}}).Empty() {
		t.Fatal("blank selectors should leave the scope empty")
	}
	root := "/code/example-worker"
	g := Group{Name: "example-worker@feature", Repo: "example-worker", RootDir: &root}

	scope := Scope{
		Repositories: []string{"example-worker"},
		Worktrees:    []string{"/code/example-worker", "/code/example-worker"},
	}
	if !scope.MatchGroup(g) {
		t.Fatal("repository selector did not match group")
	}
	if scope.Key() != (Scope{Worktrees: []string{root}, Repositories: []string{"example-worker"}}).Key() {
		t.Fatal("scope key changed with selector order or duplicates")
	}
}

func TestWorktreeSelectorIncludesSiblingWorktreesInSameRepository(t *testing.T) {
	mainRoot := "/code/example-worker"
	featureRoot := "/code/example-worker-feature"
	otherRoot := "/code/example-dashboard"
	groups := []Group{
		{Name: "example-worker", Repo: "example-worker", RootDir: &mainRoot},
		{Name: "example-worker@feature", Repo: "example-worker", RootDir: &featureRoot},
		{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &otherRoot},
	}
	scope := Scope{Worktrees: []string{mainRoot}}
	filtered := scope.FilterSnapshot(Snapshot{Groups: groups})
	if len(filtered.Groups) != 2 {
		t.Fatalf("same-repository groups = %d, want 2: %+v", len(filtered.Groups), filtered.Groups)
	}
	if !scope.MatchEvent(Event{Source: EventSourceForGroup(groups[1])}, groups) {
		t.Fatal("sibling worktree event did not follow the repository relation")
	}
	if scope.MatchEvent(Event{Source: EventSourceForGroup(groups[2])}, groups) {
		t.Fatal("unrelated repository event crossed the worktree relation")
	}
}

func TestScopeKeepsDaemonWideEvents(t *testing.T) {
	scope := Scope{Worktrees: []string{"/code/example-worker"}}
	if !scope.MatchEvent(Event{Kind: "daemon_stopping"}, nil) {
		t.Fatal("daemon-wide event was filtered")
	}
	group := "example-dashboard"
	if scope.MatchEvent(Event{Kind: "port_up", Group: &group}, []Group{{Name: group, Repo: group}}) {
		t.Fatal("unrelated group event passed the scope")
	}
	if scope.MatchEvent(Event{Kind: "service_failed"}, nil) {
		t.Fatal("unattributed object event became daemon-wide")
	}
	selectedRoot := "/code/example-worker"
	otherRoot := "/code/example-dashboard"
	selectedGroup := "example-worker"
	if scope.MatchEvent(Event{
		Kind: "port_up", Source: EventSourceForGroup(Group{Name: "example-dashboard", Repo: "example-dashboard", RootDir: &otherRoot}),
		Port: &Port{Group: &selectedGroup, ProjectRoot: &selectedRoot},
	}, []Group{{Name: selectedGroup, Repo: selectedGroup, RootDir: &selectedRoot}}) {
		t.Fatal("conflicting source and port widened the scope")
	}
	if scope.MatchChanged(StateChanged{Changed: []string{"port_changed"}}) {
		t.Fatal("an unattributed object change became daemon-wide")
	}
	if !scope.MatchChanged(StateChanged{Changed: []string{"daemon_stopping"}}) {
		t.Fatal("daemon lifecycle change was filtered")
	}
	pathless := EventSourceForGroup(Group{Name: "other", Repo: "other"})
	if (Scope{Worktrees: []string{"."}}).MatchChanged(StateChanged{Source: pathless, Changed: []string{"worktree_changed"}}) {
		t.Fatal("pathless source matched a relative path selector")
	}
}

func TestEventSourceCarriesWorktreeIdentity(t *testing.T) {
	root := "/code/example-worker"
	group := "example-worker@feature"
	p := Port{Host: LocalhostName, Group: &group, ProjectRoot: &root}
	g := Group{Name: group, Repo: "example-worker", Worktree: "feature", Branch: "feature/x", RootDir: &root}
	source := EventSourceForPort(p, []Group{g})
	if source == nil || source.RepoID != RepoID(g) || source.WorktreeID != WorktreeID(g) || source.Branch != "feature/x" {
		t.Fatalf("event source = %+v", source)
	}
}

func TestEventSourceKeepsRepositoryAliasWhenRepoFieldIsEmpty(t *testing.T) {
	g := Group{Name: "standalone"}
	if !(Scope{Repositories: []string{"standalone"}}).MatchGroup(g) {
		t.Fatal("group-name repository alias did not filter a pathless standalone group")
	}
	source := EventSourceForGroup(g)
	if !(Scope{Repositories: []string{"standalone"}}).MatchChanged(StateChanged{
		Source: source, Changed: []string{"worktree_changed"},
	}) {
		t.Fatal("group-name repository alias did not route a pathless standalone group")
	}
}

func TestEventSourceUsesProjectRootWhenGroupIsMissing(t *testing.T) {
	root := "/code/example-worker"
	g := Group{Name: "example-worker", Repo: "example-worker", RootDir: &root}
	p := Port{Host: LocalhostName, ProjectRoot: &root}
	source := EventSourceForPort(p, []Group{g})
	if source == nil || source.RepoID != RepoID(g) || source.WorktreeID != WorktreeID(g) {
		t.Fatalf("root-only event source = %+v, want repository and worktree identity", source)
	}
	if !(Scope{Worktrees: []string{WorktreeID(g)}}).MatchChanged(StateChanged{Source: source, Changed: []string{"port_changed"}}) {
		t.Fatal("worktree ID selector did not match a root-only port source")
	}
	if !(Scope{Repositories: []string{RepoID(g)}}).MatchPort(p, []Group{g}) {
		t.Fatal("repository ID selector did not match a root-only port")
	}
}

func TestEventSourceDoesNotCrossSameNamedGroupWithDifferentRoot(t *testing.T) {
	firstRoot := "/code/first"
	secondRoot := "/code/second"
	g := Group{Name: "service", Repo: "service", RootDir: &firstRoot}
	group := "service"
	p := Port{Host: LocalhostName, Group: &group, ProjectRoot: &secondRoot}
	source := EventSourceForPort(p, []Group{g})
	if source == nil || source.RepoID != "" || source.WorktreeID == WorktreeID(g) {
		t.Fatalf("conflicting-root event source = %+v, should remain unattributed", source)
	}
}

func TestEventSourceUsesCurrentGroupMetadataDuringTransition(t *testing.T) {
	root := "/code/example-worker"
	port := Port{Host: LocalhostName, ProjectRoot: &root}
	groups := []Group{
		{Name: "example-worker", Repo: "example-worker", Branch: "old", RootDir: &root},
		{Name: "example-worker", Repo: "example-worker", Branch: "new", RootDir: &root},
	}
	source := EventSourceForPort(port, groups)
	if source == nil {
		t.Fatal("transition source is nil")
	}
	if source.Branch != "new" {
		t.Fatalf("transition source branch = %q, want current metadata", source.Branch)
	}
}

func TestIsWorktreeIDRecognizesOpaqueIdentity(t *testing.T) {
	if !IsWorktreeID("0123456789abcdef01234567") {
		t.Fatal("valid worktree ID was rejected")
	}
	for _, value := range []string{"0123456789abcdef0123456", "0123456789abcdef0123456g", "/code/example-worker"} {
		if IsWorktreeID(value) {
			t.Fatalf("invalid worktree ID %q was accepted", value)
		}
	}
}

func TestStreamSnapshotRedactsCommandsAndPaths(t *testing.T) {
	root := "/code/example-worker"
	cmd := "npm run dev"
	cwd := "/code/example-worker/frontend"
	manifestHash := "manifest-hash"
	runtimeHash := "runtime-hash"
	snap := Snapshot{
		Seq: 7,
		At:  "2026-09-28T00:00:00Z",
		Groups: []Group{{
			Name: "example-worker", Repo: "example-worker", Branch: "feature", RootDir: &root, Status: "running",
			Services: []Service{{Name: "web", Cmd: cmd, Prepare: "npm build", Cwd: cwd, Running: true,
				ManifestHash: &manifestHash, RuntimeSpecHash: &runtimeHash, ManifestRuntimeMismatch: true}},
		}},
		Ports: []Port{{Port: 18090, BindAddress: "127.0.0.1", ProjectRoot: &root, Command: cmd, Cwd: cwd, Process: "node"}},
	}
	raw, err := json.Marshal(StreamSnapshotFor(snap, Scope{Worktrees: []string{root}}))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, forbidden := range []string{cmd, "npm build", cwd, "command", "cwd"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("scoped snapshot contains %q: %s", forbidden, text)
		}
	}
	if !strings.Contains(text, "worktree_id") || !strings.Contains(text, "state_revision") {
		t.Fatalf("scoped snapshot lost identity/revision: %s", text)
	}
	if !strings.Contains(text, manifestHash) || !strings.Contains(text, runtimeHash) ||
		!strings.Contains(text, "manifest_runtime_mismatch") {
		t.Fatalf("scoped snapshot lost safe drift evidence: %s", text)
	}
}

func TestStreamSnapshotAttachesRootOnlyPortToItsWorktree(t *testing.T) {
	root := "/code/example-worker"
	snap := Snapshot{
		Seq:    1,
		Groups: []Group{{Name: "example-worker", Repo: "example-worker", RootDir: &root}},
		Ports:  []Port{{Host: LocalhostName, Port: 18090, ProjectRoot: &root}},
	}
	stream := StreamSnapshotFor(snap, Scope{Worktrees: []string{root}})
	if len(stream.Worktrees) != 1 || len(stream.Worktrees[0].Ports) != 1 {
		t.Fatalf("root-only port created a duplicate worktree row: %+v", stream.Worktrees)
	}
}

func TestStreamSnapshotDoesNotAttachConflictingRootByGroupName(t *testing.T) {
	selectedRoot := "/code/selected"
	otherRoot := "/code/other"
	group := "api"
	snap := Snapshot{
		Seq:    1,
		Groups: []Group{{Name: group, Repo: "selected", RootDir: &selectedRoot}},
		Ports: []Port{{
			Host: LocalhostName, Port: 18090, Group: &group, ProjectRoot: &otherRoot,
		}},
	}
	stream := StreamSnapshotFor(snap, Scope{Worktrees: []string{selectedRoot, otherRoot}})
	if len(stream.Worktrees) != 2 {
		t.Fatalf("worktrees = %+v, want separate selected and conflicting-root rows", stream.Worktrees)
	}
	for _, worktree := range stream.Worktrees {
		if worktree.Source.WorktreeID == WorktreeID(snap.Groups[0]) && len(worktree.Ports) != 0 {
			t.Fatalf("conflicting-root port attached to selected group: %+v", worktree)
		}
	}
}

func TestWorkspaceScopeIsExplicitAndMergeable(t *testing.T) {
	first := Scope{Workspace: "integration-session", Worktrees: []string{"/code/example-worker"}}
	second := Scope{Workspace: "integration-session", Repositories: []string{"example-dashboard"}}
	merged := first.Merge(second)
	if merged.Workspace != "integration-session" || !merged.MatchGroup(Group{Name: "example-dashboard", Repo: "example-dashboard"}) {
		t.Fatalf("merged workspace scope = %+v", merged)
	}
	if !merged.MatchGroup(Group{Name: "example-worker", Repo: "example-worker", RootDir: stringPtr("/code/example-worker")}) {
		t.Fatal("workspace merge lost the explicit worktree")
	}
}

func TestStateChangesCarrySummaryOnlyIdentity(t *testing.T) {
	root := "/code/example-worker"
	group := "example-worker"
	prev := Snapshot{Seq: 3, Groups: []Group{{Name: group, Repo: group, RootDir: &root}}}
	next := Snapshot{
		Seq:    4,
		At:     "2026-09-28T00:00:04Z",
		Groups: prev.Groups,
		Ports:  []Port{{Port: 18090, BindAddress: "127.0.0.1", Group: &group, ProjectRoot: &root}},
	}
	changes := StateChangesForTransition(prev, next, nil)
	if len(changes) != 1 || changes[0].Source == nil {
		t.Fatalf("changes = %+v, want one sourced change", changes)
	}
	if changes[0].Type != "state.changed" || changes[0].Seq != next.Seq || changes[0].StateRevision != "4" {
		t.Fatalf("change identity = %+v", changes[0])
	}
	if len(changes[0].Changed) != 1 || changes[0].Changed[0] != "port_changed" {
		t.Fatalf("changed = %v", changes[0].Changed)
	}
	if !IsWorktreeID(changes[0].EventID) {
		t.Fatalf("event id = %q, want opaque 24-hex token", changes[0].EventID)
	}
}

func TestStateChangesIgnoreStatsAndClassifyHealth(t *testing.T) {
	root := "/code/example-worker"
	group := "example-worker"
	oldHealth := &Health{Status: HealthOK, Code: 200}
	newHealth := &Health{Status: HealthFail, Code: 503}
	oldStats := &Stats{CPUPercent: 1}
	newStats := &Stats{CPUPercent: 2}
	prev := Snapshot{Seq: 1, Groups: []Group{{Name: group, Repo: group, RootDir: &root}}, Ports: []Port{
		{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1", PID: 9, Group: &group, ProjectRoot: &root,
			Health: oldHealth, Stats: oldStats},
	}}
	next := prev
	next.Seq = 2
	next.Ports = []Port{prev.Ports[0]}
	next.Ports[0].Health = newHealth
	next.Ports[0].Stats = newStats
	changes := StateChangesForTransition(prev, next, nil)
	if len(changes) != 1 || len(changes[0].Changed) != 1 || changes[0].Changed[0] != "health_changed" {
		t.Fatalf("health-only changes = %+v, want one health_changed summary", changes)
	}

	next = prev
	next.Seq = 3
	next.Ports = []Port{prev.Ports[0]}
	next.Ports[0].Stats = newStats
	if changes := StateChangesForTransition(prev, next, nil); len(changes) != 0 {
		t.Fatalf("stats-only changes = %+v, want none", changes)
	}
}

func TestStateChangesReportReadyTimeoutAsItsOwnFact(t *testing.T) {
	root := "/code/example-worker"
	group := "example-worker"
	before := Group{Name: group, Repo: group, RootDir: &root, Services: []Service{{
		Name: "api", Running: true,
	}}}
	after := before
	after.Services = []Service{{
		Name: "api", LastExit: &ServiceExit{Code: 1, Reason: "ready_timeout", At: "now", RunID: "run-1"},
	}}
	changes := StateChangesForTransition(
		Snapshot{Seq: 1, Groups: []Group{before}},
		Snapshot{Seq: 2, At: "now", Groups: []Group{after}}, nil,
	)
	if len(changes) != 1 || changes[0].Source == nil {
		t.Fatalf("ready timeout changes = %+v, want one sourced change", changes)
	}
	seen := map[string]bool{}
	for _, kind := range changes[0].Changed {
		seen[kind] = true
	}
	if !seen["ready_timeout"] || !seen["service_changed"] {
		t.Fatalf("ready timeout changed = %v, want readiness and lifecycle facts", changes[0].Changed)
	}

	unchanged := StateChangesForTransition(
		Snapshot{Seq: 2, Groups: []Group{after}},
		Snapshot{Seq: 3, At: "later", Groups: []Group{after}}, nil,
	)
	if len(unchanged) != 0 {
		t.Fatalf("repeated ready timeout = %+v, want no duplicate event", unchanged)
	}
}

func TestStateChangesIncludeMachineReferenceFactsOnOwningWorktree(t *testing.T) {
	root := "/code/example-worker"
	before := Group{
		Name: "example-worker", Repo: "example-worker", RootDir: &root,
		Machine: []MachineRef{{Name: "db", Port: 5432, Listening: false}},
	}
	after := before
	after.Machine = []MachineRef{{Name: "db", Port: 5432, Listening: true}}
	changes := StateChangesForTransition(
		Snapshot{Seq: 1, Groups: []Group{before}},
		Snapshot{Seq: 2, At: "now", Groups: []Group{after}}, nil,
	)
	if len(changes) != 1 || changes[0].Source == nil || len(changes[0].Changed) != 1 || changes[0].Changed[0] != "worktree_changed" {
		t.Fatalf("machine reference change = %+v, want one owning-worktree summary", changes)
	}
}

func TestMovedWorktreeNotifiesBothIdentities(t *testing.T) {
	oldRoot, newRoot := "/code/example-worker", "/code/example-worker-moved"
	oldGroup := Group{Name: "example-worker", Repo: "example-worker", RootDir: &oldRoot}
	newGroup := oldGroup
	newGroup.RootDir = &newRoot
	changes := StateChangesForTransition(
		Snapshot{Seq: 1, Groups: []Group{oldGroup}},
		Snapshot{Seq: 2, At: "now", Groups: []Group{newGroup}}, nil,
	)
	if len(changes) != 2 {
		t.Fatalf("moved worktree changes = %+v, want old and new identities", changes)
	}
	seen := map[string]bool{}
	for _, change := range changes {
		if change.Source == nil {
			t.Fatalf("moved worktree change has no source: %+v", change)
		}
		seen[change.Source.WorktreeID] = true
	}
	if !seen[WorktreeID(oldGroup)] || !seen[WorktreeID(newGroup)] {
		t.Fatalf("moved worktree identities = %v", seen)
	}
}

func TestPortReattributionNotifiesBothWorktreeIdentities(t *testing.T) {
	oldRoot, newRoot := "/code/example-worker", "/code/example-dashboard"
	oldGroup, newGroup := "example-worker", "example-dashboard"
	oldPort := Port{Host: LocalhostName, Port: 18090, BindAddress: "127.0.0.1", PID: 7, Group: &oldGroup, ProjectRoot: &oldRoot}
	newPort := oldPort
	newPort.Group = &newGroup
	newPort.ProjectRoot = &newRoot
	groups := []Group{
		{Name: oldGroup, Repo: oldGroup, RootDir: &oldRoot},
		{Name: newGroup, Repo: newGroup, RootDir: &newRoot},
	}
	changes := StateChangesForTransition(
		Snapshot{Seq: 1, Groups: groups, Ports: []Port{oldPort}},
		Snapshot{Seq: 2, At: "now", Groups: groups, Ports: []Port{newPort}}, nil,
	)
	seen := map[string]bool{}
	for _, change := range changes {
		if change.Source != nil {
			seen[change.Source.WorktreeID] = true
		}
	}
	if !seen[WorktreeID(groups[0])] || !seen[WorktreeID(groups[1])] {
		t.Fatalf("port reattribution identities = %v; changes=%+v", seen, changes)
	}
}

func TestGroupOnlyEventCarriesItsWorktreeSource(t *testing.T) {
	root := "/code/example-worker"
	group := "example-worker"
	ev := Event{Kind: "service_failed", Group: &group, At: "now"}
	groups := []Group{{Name: group, Repo: group, RootDir: &root}}
	change := StateChangedForEvent(ev, 4, groups)
	if change.Source == nil || change.Source.WorktreeID != WorktreeID(groups[0]) {
		t.Fatalf("group-only source = %+v", change.Source)
	}
	if !(Scope{Worktrees: []string{root}}).MatchEvent(ev, groups) {
		t.Fatal("group-only event did not match its worktree")
	}
}

func TestAmbiguousGroupOnlyEventIsNotRouted(t *testing.T) {
	first, second := "/code/first", "/code/second"
	group := "api"
	ev := Event{Kind: "service_failed", Group: &group, At: "now"}
	groups := []Group{
		{Name: group, Repo: "first", RootDir: &first},
		{Name: group, Repo: "second", RootDir: &second},
	}
	if (Scope{Worktrees: []string{first}}).MatchEvent(ev, groups) {
		t.Fatal("ambiguous group-only event crossed a same-named worktree")
	}
}

func TestStateChangesDropUnattributedObjectChanges(t *testing.T) {
	prev := Snapshot{Seq: 1}
	next := Snapshot{Seq: 2, At: "now", Ports: []Port{{Host: "localhost", Port: 18090, BindAddress: "127.0.0.1"}}}
	if changes := StateChangesForTransition(prev, next, nil); len(changes) != 0 {
		t.Fatalf("unattributed port changes = %+v, want none", changes)
	}
	changes := StateChangesForTransition(prev, prev, []Event{{Kind: "daemon_stopping", At: "now"}})
	if len(changes) != 1 || changes[0].Source != nil || changes[0].Changed[0] != "daemon_stopping" {
		t.Fatalf("daemon-wide changes = %+v", changes)
	}
}

func stringPtr(v string) *string { return &v }
