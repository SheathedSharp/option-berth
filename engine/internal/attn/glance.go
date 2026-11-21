package attn

import (
	"os"
	"strings"
	"time"
)

// Glance is a redacted view of a published worktree artifact: kind and time
// only. It never includes evidence, options, paths, assessments, or task text.
// GeneratedAt is a whole-second display timestamp; comparison uses the
// original published time, including fractional seconds.
type Glance struct {
	Present     bool   `json:"present"`
	GeneratedAt string `json:"generated_at,omitempty"`
	EventKind   string `json:"event_kind,omitempty"`
	publishedAt time.Time
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
	published, _ := parseGlanceTime(artifact.GeneratedAt)
	generatedAt := formatGlanceTime(artifact.GeneratedAt)
	return Glance{
		Present:     true,
		GeneratedAt: generatedAt,
		EventKind:   publicEventKind(artifact.Event.Kind),
		publishedAt: published,
	}, nil
}

// ObservedAfter reports whether lastObservedAt is at or after the artifact's
// original generated time, meaning an observation followed the publish.
// Display timestamps may be truncated to whole seconds; this comparison is not.
func (g Glance) ObservedAfter(lastObservedAt string) bool {
	if !g.Present || g.publishedAt.IsZero() {
		return false
	}
	observed, ok := parseGlanceTime(lastObservedAt)
	if !ok {
		return false
	}
	return !observed.Before(g.publishedAt)
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
