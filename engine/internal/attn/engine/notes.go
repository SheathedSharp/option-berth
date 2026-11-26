// Package attnengine is Attention's native half: a tracked note per causal
// finding in a worktree's Runtime World, plus an optional in-process triage of
// those notes.
//
// The split inside this package is the design:
//
//   - Deriving notes is deterministic and always on. It reads a
//     runtimecore.World and writes one owner-only JSON document per worktree.
//     No key, no network and no settings file are involved, so a person who
//     never enabled Attention still gets a note the moment something breaks.
//   - Triage is optional and asynchronous. It attaches a recommendation to a
//     note under a world-revision compare-and-swap, so a slow answer can never
//     describe a world that has moved on.
//
// Nothing here starts, stops or restarts a service, and nothing here is
// consulted by status, up, down, restart or logs.
package attnengine

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
)

const (
	// NotesSchema is the version tag of one worktree's note document.
	NotesSchema = "oberth.attn.notes/v1"
	// MaxResolvedNotes is how much resolved history one worktree keeps. A note
	// that stopped being true is worth a short audit trail, not an archive.
	MaxResolvedNotes = 10
	// maxNotesFile bounds a read. A document larger than this is not one this
	// build wrote.
	maxNotesFile = 1 << 20
	// maxBriefRunes bounds the mid-turn text a native hook may inject.
	maxBriefRunes = 500
)

// NoteStatus is where a tracked note is in its life. open and triaged are the
// live states, delivered records that an agent turn already saw it, resolved
// records that the world stopped producing it, and suppressed is a person's
// explicit "do not raise this again".
type NoteStatus string

const (
	NoteOpen       NoteStatus = "open"
	NoteTriaged    NoteStatus = "triaged"
	NoteDelivered  NoteStatus = "delivered"
	NoteResolved   NoteStatus = "resolved"
	NoteSuppressed NoteStatus = "suppressed"
)

// TriageResult is what System-1 said about a note, if anything. Every field is
// a recommendation: none of them grants an action, and none of them is a
// runtime fact.
type TriageResult struct {
	Mode                  string   `json:"mode,omitempty"`
	Model                 string   `json:"model,omitempty"`
	TaskRelevant          *float64 `json:"task_relevant,omitempty"`
	NeedsHuman            *float64 `json:"needs_human,omitempty"`
	Urgency               *float64 `json:"urgency,omitempty"`
	NextOption            string   `json:"next_option,omitempty"`
	Confidence            *float64 `json:"confidence,omitempty"`
	Summary               string   `json:"summary,omitempty"`
	TriagedAt             string   `json:"triaged_at,omitempty"`
	UnavailableReason     string   `json:"unavailable_reason,omitempty"`
	AutoReconcileEligible bool     `json:"auto_reconcile_eligible,omitempty"`
}

// Note is one tracked causal finding about a worktree.
//
// WorktreeRoot is local: it is the key a person reads, and it never travels to
// a provider (EvaluateWorld hashes it). FirstError is already redacted by the
// time it is stored.
type Note struct {
	ID            string                  `json:"id"`
	WorktreeID    string                  `json:"worktree_id"`
	WorktreeRoot  string                  `json:"worktree_root,omitempty"`
	Group         string                  `json:"group,omitempty"`
	WorldRevision string                  `json:"world_revision,omitempty"`
	Status        NoteStatus              `json:"status"`
	Severity      runtimecore.Severity    `json:"severity"`
	Service       string                  `json:"service,omitempty"`
	Kind          runtimecore.DriftKind   `json:"kind,omitempty"`
	Summary       string                  `json:"summary"`
	Impacted      []string                `json:"impacted,omitempty"`
	FirstError    string                  `json:"first_error,omitempty"`
	Loop          *runtimecore.LoopSignal `json:"loop,omitempty"`
	Command       string                  `json:"command,omitempty"`
	Safety        string                  `json:"safety"`
	FirstSeen     string                  `json:"first_seen"`
	LastSeen      string                  `json:"last_seen"`
	ResolvedAt    string                  `json:"resolved_at,omitempty"`
	DeliveredAt   string                  `json:"delivered_at,omitempty"`
	// DeliveredRev is the world revision already injected into an agent turn.
	// It is what keeps a PostToolUse hook from repeating the same alert on
	// every tool call.
	DeliveredRev string        `json:"delivered_rev,omitempty"`
	Triage       *TriageResult `json:"triage,omitempty"`
}

// WorktreeNotes is the whole document for one worktree.
type WorktreeNotes struct {
	Schema        string                  `json:"schema"`
	WorktreeID    string                  `json:"worktree_id"`
	WorktreeRoot  string                  `json:"worktree_root,omitempty"`
	Group         string                  `json:"group,omitempty"`
	WorldRevision string                  `json:"world_revision,omitempty"`
	WorldHealth   runtimecore.WorldHealth `json:"world_health,omitempty"`
	UpdatedAt     string                  `json:"updated_at,omitempty"`
	Notes         []Note                  `json:"notes,omitempty"`
}

// MidTurnBrief is one bounded, non-blocking finding for a native hook.
type MidTurnBrief struct {
	Revision string
	Text     string
}

// WorktreeID is the stable, non-reversible identity a note is filed under.
func WorktreeID(root string) string { return model.WorktreeID(root) }

// NotesDir is where every worktree's note document lives.
func NotesDir(home string) string {
	return filepath.Join(home, attn.AttentionDir, "notes")
}

// NotesPath is one worktree's note document.
func NotesPath(home, root string) string {
	return filepath.Join(NotesDir(home), WorktreeID(root)+".json")
}

// NoteStore persists note documents under one BERTH_HOME. It is a value: two
// callers in two processes share the same file, not the same struct.
type NoteStore struct {
	Home string
}

// Load reads one worktree's document. A missing document is an empty one, not
// an error: "no notes yet" is the normal state of a healthy worktree. A
// malformed document is an error, and the caller decides whether to replace it.
func (s NoteStore) Load(root string) (WorktreeNotes, error) {
	doc := WorktreeNotes{
		Schema: NotesSchema, WorktreeID: WorktreeID(root), WorktreeRoot: filepath.Clean(root),
	}
	info, err := os.Stat(NotesPath(s.Home, root))
	if err != nil {
		if os.IsNotExist(err) {
			return doc, nil
		}
		return doc, fmt.Errorf("reading attention notes: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxNotesFile {
		return doc, errors.New("attention notes are not a readable document")
	}
	data, err := os.ReadFile(NotesPath(s.Home, root))
	if err != nil {
		return doc, fmt.Errorf("reading attention notes: %w", err)
	}
	var loaded WorktreeNotes
	if err := json.Unmarshal(data, &loaded); err != nil {
		return doc, errors.New("attention notes are not valid JSON")
	}
	if loaded.Schema != "" && loaded.Schema != NotesSchema {
		return doc, errors.New("attention notes schema is unsupported")
	}
	loaded.WorktreeID = WorktreeID(root)
	loaded.WorktreeRoot = filepath.Clean(root)
	loaded.Schema = NotesSchema
	return loaded, nil
}

// Save publishes one document atomically at 0600. The temporary file is
// created in the destination directory so the rename cannot cross a device.
func (s NoteStore) Save(doc WorktreeNotes) error {
	if strings.TrimSpace(doc.WorktreeRoot) == "" {
		return errors.New("attention notes: worktree root is required")
	}
	doc.Schema = NotesSchema
	doc.WorktreeID = WorktreeID(doc.WorktreeRoot)
	if doc.UpdatedAt == "" {
		doc.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	dir := NotesDir(s.Home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attention notes directory: %w", err)
	}
	_ = os.Chmod(dir, 0o700)
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode attention notes: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".notes-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary attention notes: %w", err)
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("restrict attention notes: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write attention notes: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync attention notes: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close attention notes: %w", err)
	}
	if err := os.Rename(name, NotesPath(s.Home, doc.WorktreeRoot)); err != nil {
		return fmt.Errorf("publish attention notes: %w", err)
	}
	return nil
}

// MarkDelivered records that the notes active at revision were shown to an
// agent turn. It is a read-modify-write, so it runs under the same file lock the
// hook and the runtime service share.
func (s NoteStore) MarkDelivered(root, revision string, at time.Time) error {
	if strings.TrimSpace(revision) == "" {
		return errors.New("attention notes: revision is required")
	}
	return s.withLock(root, func() error {
		doc, err := s.Load(root)
		if err != nil {
			return err
		}
		changed := false
		for i := range doc.Notes {
			note := &doc.Notes[i]
			if !note.live() || note.DeliveredRev == revision {
				continue
			}
			note.DeliveredRev = revision
			note.DeliveredAt = at.UTC().Format(time.RFC3339Nano)
			if note.Status == NoteOpen || note.Status == NoteTriaged {
				note.Status = NoteDelivered
			}
			changed = true
		}
		if !changed {
			return nil
		}
		doc.UpdatedAt = at.UTC().Format(time.RFC3339Nano)
		return s.Save(doc)
	})
}

// withLock runs fn while holding the per-worktree note lock. The lock is the
// runtime service's own advisory lock: one implementation, one behaviour on
// every platform. A busy lock is retried for a short bounded window and then
// reported, never ignored — a lost update would either repeat an alert or drop
// one.
func (s NoteStore) withLock(root string, fn func() error) error {
	dir := NotesDir(s.Home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create attention notes directory: %w", err)
	}
	path := filepath.Join(dir, WorktreeID(root)+".lock")
	deadline := time.Now().Add(500 * time.Millisecond)
	for {
		lock, err := runtimesvc.AcquireLock(path)
		if err == nil {
			runErr := fn()
			return errors.Join(runErr, lock.Release())
		}
		if !runtimesvc.IsAlreadyRunning(err) {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("attention notes are busy; try again")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// ActiveNotes returns the live notes of a document, ordered the way a reader
// should act on them: severity first, then the triage's urgency, then age. A
// resolved note is history and never appears here.
func ActiveNotes(doc WorktreeNotes) []Note {
	var active []Note
	for _, note := range doc.Notes {
		if note.live() {
			active = append(active, note)
		}
	}
	sort.SliceStable(active, func(i, j int) bool {
		if severityRank(active[i].Severity) != severityRank(active[j].Severity) {
			return severityRank(active[i].Severity) < severityRank(active[j].Severity)
		}
		ui, uj := urgencyOf(active[i]), urgencyOf(active[j])
		if ui != uj {
			return ui > uj
		}
		if active[i].Service != active[j].Service {
			return active[i].Service < active[j].Service
		}
		return active[i].ID < active[j].ID
	})
	return active
}

// ResolvedNotes returns the kept resolved history, newest first.
func ResolvedNotes(doc WorktreeNotes) []Note {
	var resolved []Note
	for _, note := range doc.Notes {
		if note.Status == NoteResolved {
			resolved = append(resolved, note)
		}
	}
	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].ResolvedAt > resolved[j].ResolvedAt
	})
	return resolved
}

// TakeMidTurnBrief is the PostToolUse fast path: one file read, at most one
// atomic write, no subprocess and no network. It returns false when every live
// note has already been delivered at the current world revision, which is what
// keeps an agent turn from seeing the same alert on every tool call.
func (s NoteStore) TakeMidTurnBrief(root string) (MidTurnBrief, bool) {
	return s.TakeMidTurnBriefFor(root, nil)
}

// TakeMidTurnBriefFor narrows the brief to services the caller's task scope
// includes. An empty scope is every service: a caller that named none asked
// about the worktree. Load, filter and delivery-cursor update run under one
// per-worktree lock so parallel PostToolUse hooks cannot deliver the same
// revision twice and out-of-scope notes keep their own delivery cursor.
func (s NoteStore) TakeMidTurnBriefFor(root string, services []string) (MidTurnBrief, bool) {
	if _, err := os.Stat(NotesPath(s.Home, root)); err != nil {
		return MidTurnBrief{}, false
	}
	scope := make(map[string]bool, len(services))
	for _, service := range services {
		scope[service] = true
	}
	var brief MidTurnBrief
	var ok bool
	err := s.withLock(root, func() error {
		doc, err := s.Load(root)
		if err != nil || strings.TrimSpace(doc.WorldRevision) == "" {
			return nil
		}
		var fresh []Note
		for _, note := range ActiveNotes(doc) {
			if note.DeliveredRev == doc.WorldRevision {
				continue
			}
			if len(scope) > 0 && !noteInScope(note, scope) {
				continue
			}
			fresh = append(fresh, note)
		}
		if len(fresh) == 0 {
			return nil
		}
		deliveredIDs := make(map[string]bool, len(fresh))
		for _, note := range fresh {
			deliveredIDs[note.ID] = true
		}
		now := time.Now().UTC().Format(time.RFC3339Nano)
		for i := range doc.Notes {
			note := &doc.Notes[i]
			if !deliveredIDs[note.ID] || !note.live() {
				continue
			}
			note.DeliveredRev = doc.WorldRevision
			note.DeliveredAt = now
			if note.Status == NoteOpen || note.Status == NoteTriaged {
				note.Status = NoteDelivered
			}
		}
		doc.UpdatedAt = now
		if err := s.Save(doc); err != nil {
			return err
		}
		brief = MidTurnBrief{Revision: doc.WorldRevision, Text: briefText(doc, fresh)}
		ok = true
		return nil
	})
	if err != nil || !ok {
		return MidTurnBrief{}, false
	}
	return brief, true
}

// noteInScope reports whether a note names a service in the caller's scope,
// directly or as the consumer of an impacted one.
func noteInScope(note Note, scope map[string]bool) bool {
	if scope[note.Service] {
		return true
	}
	for _, impacted := range note.Impacted {
		if scope[impacted] {
			return true
		}
	}
	return false
}

// briefText renders a bounded, fact-only sentence for a mid-turn hook.
func briefText(doc WorktreeNotes, notes []Note) string {
	var b strings.Builder
	health := string(doc.WorldHealth)
	if health == "" {
		health = "unknown"
	}
	fmt.Fprintf(&b, "Oberth Runtime attention: %d open note(s) in this worktree (health %s).", len(notes), health)
	for i, note := range notes {
		if i == 3 {
			fmt.Fprintf(&b, " And %d more: `oberth attn notes`.", len(notes)-i)
			break
		}
		b.WriteString(" ")
		if note.Service != "" {
			b.WriteString(note.Service)
			b.WriteString(" ")
		}
		b.WriteString(note.Summary)
		if note.FirstError != "" {
			b.WriteString(" (first error: ")
			b.WriteString(note.FirstError)
			b.WriteString(")")
		}
		if note.Command != "" {
			b.WriteString(" Next: ")
			b.WriteString(note.Command)
			b.WriteString(".")
		}
		if note.Triage != nil && note.Triage.Summary != "" {
			b.WriteString(" Assessment: ")
			b.WriteString(note.Triage.Summary)
			b.WriteString(".")
		}
	}
	b.WriteString(" Facts only; no service was changed.")
	runes := []rune(b.String())
	if len(runes) > maxBriefRunes {
		return string(runes[:maxBriefRunes-1]) + "…"
	}
	return b.String()
}

func (n Note) live() bool {
	switch n.Status {
	case NoteOpen, NoteTriaged, NoteDelivered:
		return true
	}
	return false
}

func severityRank(severity runtimecore.Severity) int {
	switch severity {
	case runtimecore.SeverityCritical:
		return 0
	case runtimecore.SeverityHigh:
		return 1
	case runtimecore.SeverityWarning:
		return 2
	default:
		return 3
	}
}

func urgencyOf(note Note) float64 {
	if note.Triage != nil && note.Triage.Urgency != nil {
		return *note.Triage.Urgency
	}
	return -1
}
