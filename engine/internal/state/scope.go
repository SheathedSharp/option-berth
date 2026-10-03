package state

import (
	"encoding/json"
	"path/filepath"
	"sort"
	"strings"
)

// Scope selects the rows a state subscriber is interested in. The selectors
// are additive: a row is included when it matches any populated selector.
// Worktrees accepts canonical checkout roots or worktree IDs and follows the
// matched repository to sibling worktrees; repositories accepts repository IDs
// (and the existing internal label as a transition alias), and Workspace names
// an ephemeral explicit integration relation.
// An entirely empty scope preserves the daemon's historical machine-wide view.
//
// Scope is an ephemeral read filter. It is not persisted in oberth.yaml and
// does not create a second relationship model between projects.
type Scope struct {
	Worktrees    []string `json:"worktrees,omitempty"`
	Repositories []string `json:"repositories,omitempty"`
	Workspace    string   `json:"workspace,omitempty"`
}

// Empty reports whether the scope leaves the daemon's machine-wide view
// unchanged.
func (s Scope) Empty() bool {
	return !hasSelector(s.Worktrees) && !hasSelector(s.Repositories) && strings.TrimSpace(s.Workspace) == ""
}

// HasSelectors reports whether this scope contributes a concrete relation to
// a workspace. A workspace-only scope is valid after another subscriber has
// registered the relation, but cannot create one by itself.
func (s Scope) HasSelectors() bool {
	return hasSelector(s.Worktrees) || hasSelector(s.Repositories)
}

// Concrete returns only the explicit relation selectors, without its
// workspace label. It is used by the daemon's in-memory workspace registry.
func (s Scope) Concrete() Scope {
	return Scope{Worktrees: append([]string(nil), s.Worktrees...), Repositories: append([]string(nil), s.Repositories...)}
}

// Merge unions two explicit scopes. Workspace labels are retained when either
// side has one; callers only merge scopes for the same workspace.
func (s Scope) Merge(other Scope) Scope {
	out := Scope{
		Worktrees:    append(append([]string(nil), s.Worktrees...), other.Worktrees...),
		Repositories: append(append([]string(nil), s.Repositories...), other.Repositories...),
		Workspace:    strings.TrimSpace(s.Workspace),
	}
	if out.Workspace == "" {
		out.Workspace = strings.TrimSpace(other.Workspace)
	}
	return out
}

func hasSelector(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// Key returns a stable cache key. Selector order does not change the view.
func (s Scope) Key() string {
	n := s.normalized()
	raw, _ := json.Marshal(n)
	return string(raw)
}

func (s Scope) normalized() Scope {
	n := Scope{
		Worktrees:    normalizeSelectors(s.Worktrees, true),
		Repositories: normalizeSelectors(s.Repositories, false),
		Workspace:    strings.TrimSpace(s.Workspace),
	}
	return n
}

func normalizeSelectors(in []string, paths bool) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, raw := range in {
		v := strings.TrimSpace(raw)
		if v == "" {
			continue
		}
		if paths {
			v = filepath.Clean(v)
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

func containsSelector(values []string, want string, paths bool) bool {
	want = strings.TrimSpace(want)
	if want == "" {
		return false
	}
	if paths {
		want = filepath.Clean(want)
	}
	for _, value := range values {
		v := strings.TrimSpace(value)
		if paths {
			v = filepath.Clean(v)
		}
		if v == want {
			return true
		}
	}
	return false
}

// MatchGroup reports whether a group belongs to this scope.
func (s Scope) MatchGroup(g Group) bool {
	if s.Empty() {
		return true
	}
	if g.RootDir != nil && containsSelector(s.Worktrees, *g.RootDir, true) {
		return true
	}
	if containsSelector(s.Worktrees, WorktreeID(g), false) {
		return true
	}
	repo := strings.TrimSpace(g.Repo)
	if repo == "" {
		repo = strings.TrimSpace(g.Name)
	}
	if containsSelector(s.Repositories, repo, false) || containsSelector(s.Repositories, RepoID(g), false) {
		return true
	}
	return false
}

// ExpandRepositories adds the repositories reached through explicit
// worktree selectors. A worktree is therefore the narrow entry point for the
// automatic same-repository relation: sibling checkouts become visible, but
// another repository still needs its own selector or workspace binding.
func (s Scope) ExpandRepositories(groups []Group) Scope {
	if s.Empty() || !hasSelector(s.Worktrees) {
		return s
	}
	out := Scope{
		Worktrees:    append([]string(nil), s.Worktrees...),
		Repositories: append([]string(nil), s.Repositories...),
		Workspace:    s.Workspace,
	}
	seen := make(map[string]struct{}, len(out.Repositories))
	for _, repo := range out.Repositories {
		if strings.TrimSpace(repo) != "" {
			seen[strings.TrimSpace(repo)] = struct{}{}
		}
	}
	for _, g := range groups {
		if !matchesWorktreeSelector(s, g) {
			continue
		}
		repoAlias := strings.TrimSpace(g.Repo)
		if repoAlias == "" {
			repoAlias = strings.TrimSpace(g.Name)
		}
		for _, repo := range []string{repoAlias, RepoID(g)} {
			repo = strings.TrimSpace(repo)
			if repo == "" {
				continue
			}
			if _, ok := seen[repo]; ok {
				continue
			}
			seen[repo] = struct{}{}
			out.Repositories = append(out.Repositories, repo)
		}
	}
	return out
}

func matchesWorktreeSelector(s Scope, g Group) bool {
	if g.RootDir != nil && containsSelector(s.Worktrees, *g.RootDir, true) {
		return true
	}
	return containsSelector(s.Worktrees, WorktreeID(g), false)
}

// MatchPort reports whether a port belongs to this scope. A root selector can
// match a port even when the scanner has not assigned it to a group yet.
func (s Scope) MatchPort(p Port, groups []Group) bool {
	if s.Empty() {
		return true
	}
	s = s.ExpandRepositories(groups)
	projectRoot := ""
	if p.ProjectRoot != nil {
		projectRoot = strings.TrimSpace(*p.ProjectRoot)
	}
	if projectRoot != "" && containsSelector(s.Worktrees, projectRoot, true) {
		return true
	}
	if projectRoot != "" {
		for _, g := range groups {
			if HostOf(g.Host) != HostOf(p.Host) || g.RootDir == nil ||
				filepath.Clean(strings.TrimSpace(*g.RootDir)) != filepath.Clean(projectRoot) {
				continue
			}
			if s.MatchGroup(g) {
				return true
			}
		}
	}
	if p.Group != nil {
		for _, g := range groups {
			if g.Name != *p.Group || HostOf(g.Host) != HostOf(p.Host) || !s.MatchGroup(g) {
				continue
			}
			// A group name is only a useful fallback when the port does not
			// carry a conflicting project root. This prevents two repositories
			// that happen to use the same group label from crossing the scope.
			if projectRoot != "" && g.RootDir != nil &&
				filepath.Clean(strings.TrimSpace(*g.RootDir)) != filepath.Clean(projectRoot) {
				continue
			}
			return true
		}
	}
	return false
}

// MatchEvent reports whether an event belongs to this scope. Events without a
// port or group are daemon-wide lifecycle events and remain visible to every
// subscriber.
func (s Scope) MatchEvent(ev Event, groups []Group) bool {
	if s.Empty() {
		return true
	}
	s = s.ExpandRepositories(groups)
	if ev.Source == nil && ev.Port == nil && ev.Group != nil {
		// A group-only event is safe to route only when its name identifies one
		// worktree on this daemon. Duplicate names across repositories are
		// intentionally treated as unattributed.
		if source := EventSourceForEvent(ev, groups); source != nil {
			return s.MatchChanged(StateChanged{Source: source, Changed: []string{eventChangeKind(ev.Kind)}})
		}
		return false
	}
	if ev.Source != nil {
		if containsSelector(s.Worktrees, ev.Source.rootDir, true) ||
			containsSelector(s.Worktrees, ev.Source.WorktreeID, false) ||
			containsSelector(s.Repositories, ev.Source.repo, false) ||
			containsSelector(s.Repositories, ev.Source.RepoID, false) {
			return true
		}
		if ev.Source.rootDir != "" || ev.Source.WorktreeID != "" ||
			ev.Source.repo != "" || ev.Source.RepoID != "" {
			// Once a producer has supplied an identity, it is authoritative;
			// do not let a conflicting legacy port/group field widen the scope.
			return false
		}
	}
	if ev.Source != nil && ev.Port == nil && ev.Group == nil {
		return false
	}
	if ev.Port == nil && ev.Group == nil {
		return daemonWideChange(ev.Kind)
	}
	if ev.Port != nil && s.MatchPort(*ev.Port, groups) {
		return true
	}
	if ev.Group != nil {
		for _, g := range groups {
			if g.Name == *ev.Group && s.MatchGroup(g) {
				return true
			}
		}
	}
	return false
}

// MatchChanged reports whether a redacted scoped notification belongs to this
// relation. The daemon calls it before JSON encoding, while the source's
// private routing aliases are still available.
func (s Scope) MatchChanged(ch StateChanged) bool {
	if s.Empty() {
		return true
	}
	if ch.Source == nil {
		for _, kind := range ch.Changed {
			if daemonWideChange(kind) {
				return true
			}
		}
		return false
	}
	return containsSelector(s.Worktrees, ch.Source.rootDir, true) ||
		containsSelector(s.Worktrees, ch.Source.WorktreeID, false) ||
		containsSelector(s.Repositories, ch.Source.repo, false) ||
		containsSelector(s.Repositories, ch.Source.RepoID, false)
}

// MatchChangedIn applies the same-repository relation before matching a
// redacted notification. The source keeps private routing aliases until the
// daemon serializes it, so no path is added to the wire payload.
func (s Scope) MatchChangedIn(ch StateChanged, groups []Group) bool {
	return s.ExpandRepositories(groups).MatchChanged(ch)
}

// FilterSnapshot returns a snapshot containing only rows selected by scope.
// It preserves the daemon sequence and keeps session rows attached to selected
// ports. Sessions do not carry a canonical root, so matching by their short
// worktree name would risk leaking a same-named checkout from another repo.
func (s Scope) FilterSnapshot(snap Snapshot) Snapshot {
	if s.Empty() {
		return snap
	}
	scope := s.ExpandRepositories(snap.Groups)

	groups := make([]Group, 0, len(snap.Groups))
	for _, g := range snap.Groups {
		if !scope.MatchGroup(g) {
			continue
		}
		groups = append(groups, g)
	}

	ports := make([]Port, 0, len(snap.Ports))
	selectedSessions := make(map[string]bool)
	for _, p := range snap.Ports {
		if !scope.MatchPort(p, snap.Groups) {
			continue
		}
		ports = append(ports, p)
		if p.Session != nil && p.Session.ID != "" {
			selectedSessions[p.Session.ID] = true
		}
	}

	sessions := make([]SessionRecord, 0, len(snap.Sessions))
	for _, rec := range snap.Sessions {
		if selectedSessions[rec.ID] {
			sessions = append(sessions, rec)
		}
	}

	if groups == nil {
		groups = []Group{}
	}
	if ports == nil {
		ports = []Port{}
	}
	if sessions == nil {
		sessions = []SessionRecord{}
	}
	snap.Groups, snap.Ports, snap.Sessions = groups, ports, sessions
	return snap
}
