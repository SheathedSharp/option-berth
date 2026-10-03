package state

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Change is one collection's diff. Added and Updated carry full objects;
// Removed carries keys. All three always marshal as arrays, never null.
type Change[T any] struct {
	Added   []T      `json:"added"`
	Updated []T      `json:"updated"`
	Removed []string `json:"removed"`
}

// Delta is the incremental update broadcast to state.subscribe subscribers.
type Delta struct {
	Seq      uint64                `json:"seq"`
	At       string                `json:"at"`
	Ports    Change[Port]          `json:"ports"`
	Groups   Change[Group]         `json:"groups"`
	Sessions Change[SessionRecord] `json:"sessions"`
}

// Event is a discrete notification, sent alongside deltas when a subscriber asked for events.
type Event struct {
	EventID string         `json:"event_id,omitempty"`
	Seq     uint64         `json:"seq,omitempty"`
	Kind    string         `json:"kind"`
	At      string         `json:"at"`
	Source  *EventSource   `json:"source,omitempty"`
	Port    *Port          `json:"port,omitempty"`
	Group   *string        `json:"group,omitempty"`
	Data    map[string]any `json:"data,omitempty"`
}

// EventSource is the compact, non-path identity of the worktree that produced
// a scoped state.changed notification. The unexported fields are used by the
// daemon while routing and never cross the wire.
type EventSource struct {
	RepoID     string `json:"repo_id,omitempty"`
	WorktreeID string `json:"worktree_id,omitempty"`
	Branch     string `json:"branch,omitempty"`

	repo     string
	worktree string
	rootDir  string
	group    string
}

// RepoID returns the stable opaque identity for a repository label on a host.
// The label remains an internal matching alias; the wire only carries the ID.
func RepoID(g Group) string {
	repo := strings.TrimSpace(g.Repo)
	if repo == "" {
		repo = strings.TrimSpace(g.Name)
	}
	return stableIdentity("repo", g.Host, repo)
}

// WorktreeID returns the stable opaque identity for one checkout. A canonical
// root is preferred; stopped or pathless groups fall back to their namespaced
// group key so they can still be routed.
func WorktreeID(g Group) string {
	root := ""
	if g.RootDir != nil {
		root = strings.TrimSpace(*g.RootDir)
	}
	return stableIdentity("worktree", g.Host, worktreeIdentityValue(root, g.Key()))
}

func worktreeIdentityValue(root, fallback string) string {
	if strings.TrimSpace(root) != "" {
		return "root\x00" + filepath.Clean(strings.TrimSpace(root))
	}
	return "group\x00" + strings.TrimSpace(fallback)
}

func stableIdentity(kind, host, value string) string {
	input := strings.Join([]string{kind, HostOf(host), strings.TrimSpace(value)}, "\x00")
	sum := sha256.Sum256([]byte(input))
	return hex.EncodeToString(sum[:])[:24]
}

// IsWorktreeID reports whether value has the opaque identity shape emitted by
// WorktreeID. The CLI uses this to preserve an ID instead of resolving it as a
// path relative to the caller's current directory.
func IsWorktreeID(value string) bool {
	v := strings.TrimSpace(value)
	if len(v) != 24 {
		return false
	}
	for _, r := range v {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}

// EventSourceForGroup builds the redacted identity sent with a scoped change.
func EventSourceForGroup(g Group) *EventSource {
	root := ""
	if g.RootDir != nil {
		root = strings.TrimSpace(*g.RootDir)
	}
	repo := strings.TrimSpace(g.Repo)
	if repo == "" {
		repo = strings.TrimSpace(g.Name)
	}
	s := &EventSource{
		RepoID:     RepoID(g),
		WorktreeID: WorktreeID(g),
		Branch:     strings.TrimSpace(g.Branch),
		repo:       repo,
		worktree:   strings.TrimSpace(g.Worktree),
		rootDir:    root,
		group:      strings.TrimSpace(g.Name),
	}
	return s
}

// EventSourceForPort builds an event source from an observed port and the
// groups in the published snapshot. A port's project root remains useful when
// group attribution is temporarily unavailable during process startup.
func EventSourceForPort(p Port, groups []Group) *EventSource {
	var matched *Group
	var fallback *Group
	groupName := ""
	if p.Group != nil {
		groupName = strings.TrimSpace(*p.Group)
	}
	root := ""
	if p.ProjectRoot != nil {
		root = strings.TrimSpace(*p.ProjectRoot)
	}
	if groupName == "" && root == "" {
		return nil
	}
	for _, g := range groups {
		if HostOf(g.Host) != HostOf(p.Host) {
			continue
		}
		if groupName != "" && g.Name != groupName {
			continue
		}
		if root != "" {
			if g.RootDir == nil || filepath.Clean(strings.TrimSpace(*g.RootDir)) != filepath.Clean(root) {
				// When the port carries a root, do not fall back to a same-named
				// group from another checkout. A root-only match is still enough
				// to recover the repository identity below.
				continue
			}
		}
		if root == "" {
			if fallback == nil {
				candidate := g
				fallback = &candidate
			}
			continue
		}
		// Keep scanning exact-root matches so a transition list containing
		// both the previous and current group uses the current branch/metadata.
		candidate := g
		matched = &candidate
	}
	if matched == nil {
		matched = fallback
	}
	if matched != nil {
		s := EventSourceForGroup(*matched)
		if s.rootDir == "" {
			s.rootDir = root
		}
		return s
	}
	return &EventSource{
		WorktreeID: stableIdentity("worktree", p.Host, worktreeIdentityValue(root, PrefixKey(p.Host, groupName))),
		rootDir:    root,
		group:      groupName,
	}
}

// EventSourceForEvent resolves the source for an internally generated event.
// Port observations carry the strongest identity; group-only lifecycle events
// use a unique group name. An ambiguous group name is deliberately left
// unattributed rather than sent to the wrong repository.
func EventSourceForEvent(ev Event, groups []Group) *EventSource {
	if ev.Source != nil {
		return ev.Source
	}
	if ev.Port != nil {
		return EventSourceForPort(*ev.Port, groups)
	}
	if ev.Group == nil || strings.TrimSpace(*ev.Group) == "" {
		return nil
	}
	var matched *Group
	for i := range groups {
		if groups[i].Name != *ev.Group {
			continue
		}
		if matched != nil {
			return nil
		}
		matched = &groups[i]
	}
	if matched == nil {
		return nil
	}
	return EventSourceForGroup(*matched)
}

// NewEventID returns a stable identity for one state transition. Seq is the
// daemon's global publish sequence; key identifies the affected object.
func NewEventID(seq uint64, kind, key string) string {
	return strings.Join([]string{strconv.FormatUint(seq, 10), strings.TrimSpace(kind), strings.TrimSpace(key)}, ":")
}

// NewStateChangedID returns a stable id for one scoped summary. It contains no
// path, command, or other project detail.
func NewStateChangedID(seq uint64, source *EventSource, changed []string) string {
	parts := make([]string, len(changed))
	copy(parts, changed)
	sort.Strings(parts)
	identity := "daemon"
	if source != nil {
		identity = source.WorktreeID
		if identity == "" {
			identity = source.RepoID
		}
		if identity == "" {
			identity = source.rootDir
		}
		if identity == "" {
			identity = "source"
		}
	}
	return OpaqueEventID(NewEventID(seq, strings.Join(parts, "+"), identity))
}

// OpaqueEventID turns an internal event identity into a wire-safe token. Event
// producers normally use NewEventID already, but hashing here keeps a future
// producer from accidentally putting a path or command into state.changed.
func OpaqueEventID(eventID string) string {
	return stableIdentity("event", "", strings.TrimSpace(eventID))
}
