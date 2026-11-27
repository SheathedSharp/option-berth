package runtimecore

import (
	"fmt"
	"sort"
	"sync"
	"time"
)

// The two loop shapes a bounded window can prove. A crash loop is repeated
// endings; a restart thrash is repeated starts of the same intent and the same
// code, which is the shape a supervisor retry produces.
type LoopKind string

const (
	LoopCrashLoop     LoopKind = "crash_loop"
	LoopRestartThrash LoopKind = "restart_thrash"
)

const (
	loopWindow          = 10 * time.Minute
	crashLoopWindow     = 10 * time.Minute
	restartThrashWindow = 5 * time.Minute
	loopThreshold       = 3
	maxLoopEvents       = 64
)

// LoopSignal is one repeated-start or repeated-crash finding. It is a fact
// about a bounded recent window, not a diagnosis of why the process fails.
type LoopSignal struct {
	Kind           LoopKind `json:"kind"`
	Service        string   `json:"service"`
	Count          int      `json:"count"`
	WindowSec      int      `json:"window_sec"`
	Summary        string   `json:"summary"`
	Recommendation string   `json:"recommendation"`
}

type loopEvent struct {
	kind     string // "start" or "exit"
	runID    string
	reason   string
	specHash string
	codeHead string
	at       time.Time
}

// LoopTracker is the sliding-window memory behind crash_loop and
// restart_thrash. It is safe for concurrent use, keeps a bounded number of
// events per (worktree, service), and never blocks the caller that records a
// fact.
type LoopTracker struct {
	mu     sync.Mutex
	events map[string][]loopEvent
	seen   map[string]map[string]bool
}

// NewLoopTracker returns an empty tracker.
func NewLoopTracker() *LoopTracker {
	return &LoopTracker{
		events: map[string][]loopEvent{},
		seen:   map[string]map[string]bool{},
	}
}

// RecordExit records one ended run. Repeated notifications for the same run id
// are ignored, so a re-read of the same service row cannot fabricate a loop.
func (t *LoopTracker) RecordExit(worktree, service, runID, reason string, at time.Time) {
	if t == nil || service == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	key := loopKey(worktree, service)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureLocked()
	if runID != "" {
		if t.seen[key] == nil {
			t.seen[key] = map[string]bool{}
		}
		token := "exit:" + runID
		if t.seen[key][token] {
			return
		}
		t.seen[key][token] = true
	}
	t.events[key] = appendBounded(t.events[key], loopEvent{
		kind: "exit", runID: runID, reason: reason, at: at,
	})
}

// RecordStart records one run start with the spec and code identity it ran. A
// changed specHash or codeHead is a different intent, not a restart of the
// same one, and the thrash rule treats it that way.
func (t *LoopTracker) RecordStart(worktree, service, runID, specHash, codeHead string, at time.Time) {
	if t == nil || service == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	key := loopKey(worktree, service)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.ensureLocked()
	if runID != "" {
		if t.seen[key] == nil {
			t.seen[key] = map[string]bool{}
		}
		token := "start:" + runID
		if t.seen[key][token] {
			return
		}
		t.seen[key][token] = true
	}
	t.events[key] = appendBounded(t.events[key], loopEvent{
		kind: "start", runID: runID, specHash: specHash, codeHead: codeHead, at: at,
	})
}

// Signals returns the loop findings for one worktree at now. The result is
// ordered by service and then kind so two reads of the same window agree.
func (t *LoopTracker) Signals(worktree string, now time.Time) []LoopSignal {
	if t == nil {
		return nil
	}
	if now.IsZero() {
		now = time.Now()
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	var out []LoopSignal
	for key, events := range t.events {
		worktreeOf, service := splitLoopKey(key)
		if worktreeOf != worktree {
			continue
		}
		if signal, ok := crashLoopSignal(service, events, now); ok {
			out = append(out, signal)
		}
		if signal, ok := restartThrashSignal(service, events, now); ok {
			out = append(out, signal)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Kind < out[j].Kind
	})
	return out
}

// Prune drops events older than the widest window. It keeps the tracker's
// memory bounded across a runtime service lifetime.
func (t *LoopTracker) Prune(now time.Time) {
	if t == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	cutoff := now.Add(-loopWindow)
	t.mu.Lock()
	defer t.mu.Unlock()
	for key, events := range t.events {
		kept := events[:0]
		for _, event := range events {
			if event.at.After(cutoff) {
				kept = append(kept, event)
			}
		}
		if len(kept) == 0 {
			delete(t.events, key)
			delete(t.seen, key)
			continue
		}
		t.events[key] = kept
		// The dedupe set follows the retained events. Keeping tokens for
		// pruned generations would grow without bound and would also refuse a
		// run id that was legitimately reused after the mirror was reset.
		tokens := make(map[string]bool, len(kept))
		for _, event := range kept {
			if event.runID != "" {
				tokens[event.kind+":"+event.runID] = true
			}
		}
		t.seen[key] = tokens
	}
}

func crashLoopSignal(service string, events []loopEvent, now time.Time) (LoopSignal, bool) {
	cutoff := now.Add(-crashLoopWindow)
	seen := map[string]bool{}
	count := 0
	for _, event := range events {
		if event.kind != "exit" || event.reason != "crashed" || !event.at.After(cutoff) {
			continue
		}
		if event.runID != "" && seen[event.runID] {
			continue
		}
		seen[event.runID] = true
		count++
	}
	if count < loopThreshold {
		return LoopSignal{}, false
	}
	return LoopSignal{
		Kind: LoopCrashLoop, Service: service, Count: count,
		WindowSec:      int(crashLoopWindow / time.Second),
		Summary:        fmt.Sprintf("%d crashes in %s", count, shortWindow(crashLoopWindow)),
		Recommendation: "oberth logs " + service + " --once",
	}, true
}

func restartThrashSignal(service string, events []loopEvent, now time.Time) (LoopSignal, bool) {
	cutoff := now.Add(-restartThrashWindow)
	var starts []loopEvent
	for _, event := range events {
		if event.kind == "start" && event.at.After(cutoff) {
			starts = append(starts, event)
		}
	}
	if len(starts) < loopThreshold {
		return LoopSignal{}, false
	}
	sort.SliceStable(starts, func(i, j int) bool { return starts[i].at.Before(starts[j].at) })
	last := starts[len(starts)-1]
	previous := starts[len(starts)-2]
	if last.specHash != previous.specHash || last.codeHead != previous.codeHead {
		// The latest start is a real change of intent or code. Counting the
		// starts before it as a thrash would blame the wrong generation.
		return LoopSignal{}, false
	}
	count := 1 // the latest start
	for i := len(starts) - 2; i >= 0; i-- {
		if starts[i].specHash != last.specHash || starts[i].codeHead != last.codeHead {
			break
		}
		count++
	}
	if count < loopThreshold {
		return LoopSignal{}, false
	}
	return LoopSignal{
		Kind: LoopRestartThrash, Service: service, Count: count,
		WindowSec:      int(restartThrashWindow / time.Second),
		Summary:        fmt.Sprintf("%d starts of the same spec and code in %s", count, shortWindow(restartThrashWindow)),
		Recommendation: "oberth logs " + service + " --once",
	}, true
}

func shortWindow(window time.Duration) string {
	if window%time.Minute == 0 {
		return fmt.Sprintf("%dm", int(window/time.Minute))
	}
	return window.String()
}

// ensureLocked lets the zero value work: a tracker is a memory, not a
// constructor-only object, and a caller that declares one as a field should
// not have to remember a factory. Caller holds t.mu.
func (t *LoopTracker) ensureLocked() {
	if t.events == nil {
		t.events = map[string][]loopEvent{}
	}
	if t.seen == nil {
		t.seen = map[string]map[string]bool{}
	}
}

func appendBounded(events []loopEvent, event loopEvent) []loopEvent {
	events = append(events, event)
	if len(events) > maxLoopEvents {
		events = append([]loopEvent(nil), events[len(events)-maxLoopEvents:]...)
	}
	return events
}

func loopKey(worktree, service string) string {
	return worktree + "\x00" + service
}

func splitLoopKey(key string) (worktree, service string) {
	for i := 0; i < len(key); i++ {
		if key[i] == 0 {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}
