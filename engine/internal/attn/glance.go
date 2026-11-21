package attn

import (
	"os"
	"strings"
	"time"
)

// Glance is a redacted view of a published worktree artifact: kind and time
// only. It never includes evidence, options, paths, assessments, or task text.
type Glance struct {
	Present     bool   `json:"present"`
	GeneratedAt string `json:"generated_at,omitempty"`
	EventKind   string `json:"event_kind,omitempty"`
}

// GlanceAt reads the canonical artifact for root, if one exists. A missing
// file is not an error. Decode failures still report that a file was present,
// without leaking the parse text.
func GlanceAt(home, root string) (Glance, error) {
	root = strings.TrimSpace(root)
	if home == "" || root == "" {
		return Glance{}, nil
	}
	path := Path(home, root)
	artifact, err := Read(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Glance{}, nil
		}
		return Glance{Present: true}, nil
	}
	return Glance{
		Present:     true,
		GeneratedAt: formatGlanceTime(artifact.GeneratedAt),
		EventKind:   publicEventKind(artifact.Event.Kind),
	}, nil
}

// ObservedAfter reports whether lastObservedAt is at or after the artifact's
// generated time, meaning an observation followed the publish.
func (g Glance) ObservedAfter(lastObservedAt string) bool {
	if !g.Present || g.GeneratedAt == "" {
		return false
	}
	published, ok := parseGlanceTime(g.GeneratedAt)
	if !ok {
		return false
	}
	observed, ok := parseGlanceTime(lastObservedAt)
	if !ok {
		return false
	}
	return !observed.Before(published)
}

func publicEventKind(kind EventKind) string {
	switch kind {
	case EventServiceFailed, EventRepeatedCrash, EventPortOccupied, EventReadyTimeout, EventHealthFailed, EventManifestConflict:
		return string(kind)
	default:
		return ""
	}
}

func formatGlanceTime(value string) string {
	at, ok := parseGlanceTime(value)
	if !ok {
		return ""
	}
	return at.UTC().Format(time.RFC3339)
}

func parseGlanceTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if at, err := time.Parse(layout, value); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}
