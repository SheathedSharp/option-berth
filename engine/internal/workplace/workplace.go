// Package workplace is the durable record of work done in one worktree: a
// named scope, the shell commands and agent tasks recorded in it, and the
// rules that decide what a record means.
//
// It owns the vocabulary and the naming rules; internal/store owns the rows.
// The daemon serves it as the workplace.* methods and `oberth workplace` is
// the CLI over them, so the same record is available to a person, to an agent
// and to the desktop client without any of them keeping a second copy.
//
// What this package deliberately does not do: it never invents an outcome.
// A block's status is `running` until something observed an end — a process
// exit the engine waited on, or a turn outcome the bridge reported. A record
// with no end evidence stays running rather than being assumed to have
// succeeded.
package workplace

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Kind is what a block records.
type Kind string

const (
	// KindShell is a command the engine ran.
	KindShell Kind = "shell"
	// KindAgent is a task handed to a coding agent through the structured
	// bridge.
	KindAgent Kind = "agent"
)

// Source says who observed a block.
type Source string

const (
	// SourceEngine is work this engine started: the run identity, pid, log
	// path and exit code are its own observation.
	SourceEngine Source = "engine"
	// SourceReported is work the engine did not start and could not observe —
	// an interactive shell or native agent in a front-end's own terminal. The
	// front-end reports what it saw; the engine records it as reported, never
	// as its own evidence.
	SourceReported Source = "reported"
)

// Status is where a work item got to. `running` is the state of every record
// that has no end evidence; there is no "unknown success".
type Status string

const (
	StatusRunning     Status = "running"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusInterrupted Status = "interrupted"
)

// Terminal reports whether a status is an outcome rather than a state the work
// is still in.
func (s Status) Terminal() bool {
	for _, outcome := range TerminalStatuses() {
		if s == Status(outcome) {
			return true
		}
	}
	return false
}

// TerminalStatuses is the vocabulary of outcomes, as the store takes it.
//
// The store enforces "an outcome is not undone by a later observation" inside
// the write transaction, and it does that with this list rather than a copy of
// its own: what a status means is decided here, once.
func TerminalStatuses() []string {
	return []string{string(StatusCompleted), string(StatusFailed), string(StatusInterrupted)}
}

// DefaultName is the workplace a worktree gets when work is recorded before
// anyone named one. Naming it rather than leaving it anonymous keeps the list
// readable; it is not a claim that the user created it.
const DefaultName = "default"

const (
	// MaxNameRunes bounds a workplace name. Long enough for a task title,
	// short enough to stay one column in a list.
	MaxNameRunes = 64
	// MaxInputBytes bounds the recorded input. A command line or a task
	// prompt fits; a pasted file does not belong in the record.
	MaxInputBytes = 8 << 10
	// MaxOutputBytes bounds the output text of a reported block. Engine
	// blocks carry a log path instead, so this limit never applies to them.
	MaxOutputBytes = 64 << 10
)

// Workplace is one named scope under a worktree.
type Workplace struct {
	ID        string
	Name      string
	Worktree  string
	Active    bool
	Blocks    int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Block is one recorded work item.
type Block struct {
	ID        string
	Workplace string
	Seq       int64
	Kind      Kind
	Source    Source
	Input     string
	Status    Status
	Provider  string
	RunID     string
	PID       int
	LogPath   string
	Output    string
	ExitCode  *int
	StartedAt time.Time
	EndedAt   time.Time
}

// Record is an observation of a work item, as a caller reports it. The same
// id reported twice updates one block rather than adding a second, which is
// how the two ends of a command or a turn reach the record in either order.
type Record struct {
	// ID identifies the work item. An engine-launched run uses the run's own
	// id; a bridge turn uses the session id and turn id together. When empty
	// the daemon assigns one.
	ID string
	// Kind is required: shell or agent.
	Kind Kind
	// Source defaults to SourceReported, which is what an explicit report is.
	// Only the engine's own recording paths set SourceEngine.
	Source    Source
	Input     string
	Status    Status
	Provider  string
	RunID     string
	PID       int
	LogPath   string
	Output    string
	ExitCode  *int
	StartedAt time.Time
	EndedAt   time.Time
}

// Validate refuses a record the store would otherwise have to guess about.
// The rules are about what the record can mean, not about style: an unknown
// kind or status would be a state nothing can render, and an oversized field
// would put a payload in the ledger where the caller meant to put a note.
func (r Record) Validate() error {
	switch r.Kind {
	case KindShell, KindAgent:
	default:
		return fmt.Errorf("%w: kind must be %q or %q, not %q", ErrInvalid, KindShell, KindAgent, r.Kind)
	}
	switch r.Source {
	case "", SourceEngine, SourceReported:
	default:
		return fmt.Errorf("%w: source must be %q or %q, not %q", ErrInvalid, SourceEngine, SourceReported, r.Source)
	}
	switch r.Status {
	case "":
		// Resolved by WithDefaults.
	case StatusRunning, StatusCompleted, StatusFailed, StatusInterrupted:
	default:
		return fmt.Errorf("%w: status must be one of running, completed, failed, interrupted — not %q",
			ErrInvalid, r.Status)
	}
	if len(r.Input) > MaxInputBytes {
		return fmt.Errorf("%w: input is %d bytes; the record keeps at most %d",
			ErrInvalid, len(r.Input), MaxInputBytes)
	}
	if len(r.Output) > MaxOutputBytes {
		return fmt.Errorf("%w: output is %d bytes; the record keeps at most %d — send the tail you want kept",
			ErrInvalid, len(r.Output), MaxOutputBytes)
	}
	return nil
}

// WithDefaults fills in what the caller left open. An explicit report with no
// status is running work: the report says the work started, and nothing has
// said it ended.
func (r Record) WithDefaults() Record {
	if r.Source == "" {
		r.Source = SourceReported
	}
	if r.Status == "" {
		r.Status = StatusRunning
	}
	if r.StartedAt.IsZero() {
		r.StartedAt = time.Now().UTC()
	}
	if r.Status.Terminal() && r.EndedAt.IsZero() {
		r.EndedAt = time.Now().UTC()
	}
	if r.ID == "" {
		r.ID = NewID()
	}
	return r
}

// Row is the record as a store row for one workplace.
func (r Record) Row(workplaceID string) store.BlockRow {
	return store.BlockRow{
		ID:        r.ID,
		Workplace: workplaceID,
		Kind:      string(r.Kind),
		Source:    string(r.Source),
		Input:     r.Input,
		Status:    string(r.Status),
		Provider:  r.Provider,
		RunID:     r.RunID,
		PID:       r.PID,
		LogPath:   r.LogPath,
		Output:    r.Output,
		ExitCode:  r.ExitCode,
		StartedAt: r.StartedAt,
		EndedAt:   r.EndedAt,
	}
}

// FromRow reads a workplace row.
func FromRow(r store.WorkplaceRow) Workplace {
	return Workplace{
		ID: r.ID, Name: r.Name, Worktree: r.Worktree, Active: r.Active,
		Blocks: r.Blocks, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
	}
}

// BlockFromRow reads a block row.
func BlockFromRow(r store.BlockRow) Block {
	return Block{
		ID: r.ID, Workplace: r.Workplace, Seq: r.Seq,
		Kind: Kind(r.Kind), Source: Source(r.Source), Input: r.Input, Status: Status(r.Status),
		Provider: r.Provider, RunID: r.RunID, PID: r.PID, LogPath: r.LogPath,
		Output: r.Output, ExitCode: r.ExitCode, StartedAt: r.StartedAt, EndedAt: r.EndedAt,
	}
}

// The named failures this package reports. They are sentinels rather than
// message text so the daemon and the CLI can classify the same failure the
// same way; errors.go maps them onto the wire codes and the next step.
var (
	// ErrNotFound is returned when a selector names no workplace.
	ErrNotFound = errors.New("no such workplace")
	// ErrAmbiguous is returned when a selector names more than one. It is
	// never resolved by picking one: work recorded into the wrong workplace is
	// worse than being asked which one was meant.
	ErrAmbiguous = errors.New("that workplace name matches more than one")
	// ErrConflict is returned when a name is already taken in this worktree.
	ErrConflict = errors.New("this worktree already has a workplace with that name")
	// ErrNoWorkplace is returned when a worktree has no workplace yet.
	ErrNoWorkplace = errors.New("this worktree has no workplace yet")
	// ErrInvalid is returned for a record or a name that cannot be stored.
	ErrInvalid = errors.New("invalid workplace record")
)

// Resolve picks the one workplace a selector names out of a worktree's list.
//
// The tiers run in order — exact name, exact id, id prefix, name prefix — and
// the first tier that matches anything decides. Inside a tier more than one
// match is an error, not a coin flip.
func Resolve(all []Workplace, selector string) (Workplace, error) {
	q := strings.TrimSpace(selector)
	if q == "" {
		return Workplace{}, fmt.Errorf("%w: the selector is empty", ErrNotFound)
	}
	lower := strings.ToLower(q)
	tiers := []func(Workplace) bool{
		func(w Workplace) bool { return strings.ToLower(w.Name) == lower },
		func(w Workplace) bool { return strings.EqualFold(w.ID, q) },
		func(w Workplace) bool { return strings.HasPrefix(strings.ToLower(w.ID), lower) },
		func(w Workplace) bool { return strings.HasPrefix(strings.ToLower(w.Name), lower) },
	}
	for _, match := range tiers {
		var hits []Workplace
		for _, w := range all {
			if match(w) {
				hits = append(hits, w)
			}
		}
		switch len(hits) {
		case 0:
			continue
		case 1:
			return hits[0], nil
		default:
			return Workplace{}, fmt.Errorf("%w: %q matches %s", ErrAmbiguous, q, strings.Join(names(hits), ", "))
		}
	}
	return Workplace{}, fmt.Errorf("%w: %q", ErrNotFound, q)
}

// Active returns the workplace new work lands in, if the worktree has one.
func Active(all []Workplace) (Workplace, bool) {
	for _, w := range all {
		if w.Active {
			return w, true
		}
	}
	return Workplace{}, false
}

// ValidateName checks a name a caller asked for and returns it trimmed.
// Spaces are allowed — a workplace is often named after the task — but a name
// that is only whitespace, or that carries control characters into a table
// cell, is refused rather than silently rewritten.
func ValidateName(name string) (string, error) {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("%w: a workplace name cannot be empty", ErrInvalid)
	}
	if len([]rune(trimmed)) > MaxNameRunes {
		return "", fmt.Errorf("%w: a workplace name is at most %d characters", ErrInvalid, MaxNameRunes)
	}
	for _, r := range trimmed {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("%w: a workplace name cannot contain control characters", ErrInvalid)
		}
	}
	return trimmed, nil
}

// NewID returns an opaque id for a workplace or a block. It is random rather
// than sequential so two processes recording into different worktrees cannot
// collide, and short enough to read out of a list.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("wp%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// StatusForExit maps a finished process to the status of its block. An
// interrupt we forwarded is its own outcome: a command the user stopped is
// not a command that failed.
func StatusForExit(code int, interrupted bool) Status {
	if interrupted {
		return StatusInterrupted
	}
	if code == 0 {
		return StatusCompleted
	}
	return StatusFailed
}

// StatusForOutcome maps a structured bridge turn outcome to a block status.
// An outcome this build does not know is not a success: the work ended without
// evidence we can read, and calling that completed would be an invention.
func StatusForOutcome(outcome string) Status {
	switch outcome {
	case "completed":
		return StatusCompleted
	case "interrupted":
		return StatusInterrupted
	case "failed", "lost", "error":
		return StatusFailed
	default:
		return StatusFailed
	}
}

func names(ws []Workplace) []string {
	out := make([]string, 0, len(ws))
	for _, w := range ws {
		out = append(out, fmt.Sprintf("%s (%s)", w.Name, w.ID))
	}
	return out
}
