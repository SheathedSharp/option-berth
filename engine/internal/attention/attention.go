// Package attention defines the derived handoff artifact an optional adapter
// can publish for a coding agent. It deliberately has no daemon or model
// dependency: runtime facts remain option-berth's source of truth, while Jev
// may enrich an artifact outside the control path.
package attention

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	Schema       = "oberth.attention/v1"
	AttentionDir = "attention"
	DefaultTTL   = 10 * time.Minute
)

type EventKind string

const (
	EventNone             EventKind = ""
	EventServiceFailed    EventKind = "service_failed"
	EventRepeatedCrash    EventKind = "repeated_crash"
	EventPortOccupied     EventKind = "port_occupied"
	EventReadyTimeout     EventKind = "ready_timeout"
	EventHealthFailed     EventKind = "health_failed"
	EventMachineOffline   EventKind = "machine_unavailable"
	EventUndeclared       EventKind = "undeclared_listener"
	EventManifestConflict EventKind = "manifest_runtime_conflict"
)

const (
	DefaultCrashWindow    = 10 * time.Minute
	DefaultCrashThreshold = 3
)

// Facts is the small, code-produced input needed to derive an attention
// artifact. It intentionally mirrors evidence rather than importing the
// command package, so adapters and tests can consume it without a daemon.
type Facts struct {
	StateRevision  string
	ObservedAt     time.Time
	WorktreeRoot   string
	Branch         string
	Services       []Service
	Machine        []Machine
	Listeners      []Listener
	History        []ExitHistory
	CrashWindow    time.Duration
	CrashThreshold int
}

type Service struct {
	Name          string
	Running       bool
	LogPath       string
	RunID         string
	StartedAt     string
	DeclaredPort  *int
	ActualPort    *int
	LastExit      *Exit
	ReadyTimedOut bool
	HealthStatus  string
	HealthCode    int
	HealthReason  string
}

type Exit struct {
	Code   int
	Reason string
	At     time.Time
	RunID  string
}

type Machine struct {
	Name      string
	Port      int
	Listening bool
}

// Listener is a port attributed to the current status scope. The status
// command already scopes rows to a worktree; Cwd and ProjectRoot are retained
// so a caller that starts from a host snapshot can enforce that same boundary.
type Listener struct {
	Port        int
	Cwd         string
	ProjectRoot string
}

// ExitHistory is a bounded, read-only view of finished runs used to recognize
// a repeated crash. It is derived from the existing runs list, never stored by
// this package.
type ExitHistory struct {
	Service string
	Reason  string
	RunID   string
	At      time.Time
}

// Revision returns a stable fingerprint of the facts that can create an
// attention event. Poll observation time is intentionally excluded so repeated
// reads of unchanged state do not create a new revision; a persisted exit time
// remains part of the run evidence and therefore identifies a new run.
func Revision(f Facts) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s", filepath.Clean(f.WorktreeRoot), f.Branch, f.StateRevision)
	services := append([]Service(nil), f.Services...)
	sort.SliceStable(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	for _, service := range services {
		_, _ = fmt.Fprintf(h, "\x00service\x00%s\x00%t\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s", service.Name, service.Running, service.RunID, service.StartedAt, service.LogPath, service.HealthStatus, service.HealthCode, service.HealthReason)
		if service.DeclaredPort != nil {
			_, _ = fmt.Fprintf(h, "\x00declared_port\x00%d", *service.DeclaredPort)
		}
		if service.ActualPort != nil {
			_, _ = fmt.Fprintf(h, "\x00actual_port\x00%d", *service.ActualPort)
		}
		if service.LastExit != nil {
			_, _ = fmt.Fprintf(h, "\x00exit\x00%d\x00%s\x00%s\x00%s", service.LastExit.Code, service.LastExit.Reason, service.LastExit.RunID, service.LastExit.At.UTC().Format(time.RFC3339Nano))
		}
		if service.ReadyTimedOut {
			_, _ = fmt.Fprint(h, "\x00ready_timeout")
		}
	}
	machines := append([]Machine(nil), f.Machine...)
	sort.SliceStable(machines, func(i, j int) bool {
		if machines[i].Name != machines[j].Name {
			return machines[i].Name < machines[j].Name
		}
		return machines[i].Port < machines[j].Port
	})
	for _, machine := range machines {
		_, _ = fmt.Fprintf(h, "\x00machine\x00%s\x00%d\x00%t", machine.Name, machine.Port, machine.Listening)
	}
	listeners := append([]Listener(nil), f.Listeners...)
	sort.SliceStable(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			return listeners[i].Port < listeners[j].Port
		}
		if listeners[i].ProjectRoot != listeners[j].ProjectRoot {
			return listeners[i].ProjectRoot < listeners[j].ProjectRoot
		}
		return listeners[i].Cwd < listeners[j].Cwd
	})
	for _, listener := range listeners {
		_, _ = fmt.Fprintf(h, "\x00listener\x00%d\x00%s\x00%s", listener.Port, listener.Cwd, listener.ProjectRoot)
	}
	history := append([]ExitHistory(nil), f.History...)
	sort.SliceStable(history, func(i, j int) bool {
		if history[i].Service != history[j].Service {
			return history[i].Service < history[j].Service
		}
		if !history[i].At.Equal(history[j].At) {
			return history[i].At.Before(history[j].At)
		}
		return history[i].RunID < history[j].RunID
	})
	for _, exit := range history {
		_, _ = fmt.Fprintf(h, "\x00history\x00%s\x00%s\x00%s\x00%s", exit.Service, exit.Reason, exit.RunID, exit.At.UTC().Format(time.RFC3339Nano))
	}
	_, _ = fmt.Fprintf(h, "\x00crash_window\x00%d\x00crash_threshold\x00%d", crashWindow(f), crashThreshold(f))
	return hex.EncodeToString(h.Sum(nil))[:24]
}

type Event struct {
	Kind         EventKind `json:"kind"`
	Service      string    `json:"service,omitempty"`
	RunID        string    `json:"run_id,omitempty"`
	ExitCode     *int      `json:"exit_code,omitempty"`
	Machine      string    `json:"machine,omitempty"`
	Port         int       `json:"port,omitempty"`
	DeclaredPort *int      `json:"declared_port,omitempty"`
	ActualPort   *int      `json:"actual_port,omitempty"`
	Count        int       `json:"count,omitempty"`
	Reason       string    `json:"reason,omitempty"`
	At           string    `json:"at"`
}

type Evidence struct {
	Ref     string `json:"ref"`
	Service string `json:"service,omitempty"`
	RunID   string `json:"run_id,omitempty"`
	// LogPath is local evidence published by status. Adapters must not send
	// this path or log contents to a remote model.
	LogPath string `json:"log_path,omitempty"`
}

// Option is a fixed action identifier. An adapter or agent maps it to an
// existing documented CLI command; the artifact never contains shell text.
type Option struct {
	ID string `json:"id"`
}

type Assessment struct {
	NeedsHuman *float64 `json:"needs_human,omitempty"`
	// ContextID binds an assessment cache to the task and authorized option
	// allowlist that produced it. It is opaque to the core and never used as a
	// runtime fact.
	ContextID string `json:"context_id,omitempty"`
	// TaskRelevant is the adapter's probability that this event materially
	// affects the task context it was given. It is a recommendation, never a
	// runtime fact, and is omitted when an adapter did not ask that question.
	TaskRelevant  *float64           `json:"task_relevant,omitempty"`
	NextOption    string             `json:"next_option,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    *float64           `json:"confidence,omitempty"`
	Model         string             `json:"model,omitempty"`
}

type Artifact struct {
	Schema        string `json:"schema"`
	EventID       string `json:"event_id"`
	StateRevision string `json:"state_revision"`
	WorktreeRoot  string `json:"worktree_root"`
	Branch        string `json:"branch,omitempty"`
	GeneratedAt   string `json:"generated_at"`
	FreshUntil    string `json:"fresh_until"`
	FirstSeen     string `json:"first_seen"`
	LastSeen      string `json:"last_seen"`
	RecoveredAt   string `json:"recovered_at,omitempty"`
	Event         Event  `json:"event"`
	// Events contains every currently observed deterministic anomaly. Event
	// remains the first event for readers of the original v1 shape; new
	// consumers should use Events when they need to reason about all failures.
	Events     []Event     `json:"events,omitempty"`
	Evidence   []Evidence  `json:"evidence"`
	Options    []Option    `json:"options"`
	Assessment *Assessment `json:"assessment,omitempty"`
}

// ClassifyAll returns every deterministic anomaly in a stable order.
// Normal stops, normal exits and ordinary log changes do not create requests.
// Keeping all anomalies in one worktree-scoped artifact means an agent cannot
// miss a second failed service while handling the first one.
func ClassifyAll(f Facts) []Event {
	services := append([]Service(nil), f.Services...)
	sort.SliceStable(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	events := make([]Event, 0, len(services)+len(f.Machine)+len(f.Listeners))
	for _, service := range services {
		if service.ReadyTimedOut {
			events = append(events, Event{Kind: EventReadyTimeout, Service: service.Name, RunID: service.RunID, Reason: "ready_timeout", At: eventTime(f)})
		} else if service.Running && service.HealthStatus == "fail" {
			events = append(events, Event{Kind: EventHealthFailed, Service: service.Name, RunID: service.RunID, Reason: service.HealthReason, At: eventTime(f)})
		} else if !service.Running && service.LastExit != nil {
			reason := service.LastExit.Reason
			kind := EventServiceFailed
			switch reason {
			case "port_occupied":
				kind = EventPortOccupied
			case "ready_timeout":
				kind = EventReadyTimeout
			case "crashed":
				kind = EventServiceFailed
				if n := recentCrashes(f, service.Name, service.LastExit); n >= crashThreshold(f) {
					kind = EventRepeatedCrash
				}
			case "start_failed":
				kind = EventServiceFailed
			case "dependency_timeout", "dependency_not_ready":
				kind = EventServiceFailed
			default:
				kind = EventNone
			}
			if kind != EventNone {
				at := service.LastExit.At
				if at.IsZero() {
					at = eventTimeValue(f)
				}
				code := service.LastExit.Code
				atText := ""
				if !at.IsZero() {
					atText = at.UTC().Format(time.RFC3339Nano)
				}
				event := Event{Kind: kind, Service: service.Name, RunID: service.LastExit.RunID, ExitCode: &code, Reason: reason, At: atText}
				if kind == EventRepeatedCrash {
					event.Count = recentCrashes(f, service.Name, service.LastExit)
				}
				events = append(events, event)
			}
		}
		// A spawned process may not have bound its port yet. Absence alone is
		// not a conflict without an explicit ready-timeout fact. Only two
		// observed, differing ports are a deterministic manifest mismatch.
		if service.DeclaredPort != nil && service.Running && service.ActualPort == nil && strings.TrimSpace(service.StartedAt) == "" {
			port := *service.DeclaredPort
			events = append(events, Event{Kind: EventManifestConflict, Service: service.Name, RunID: service.RunID, DeclaredPort: &port, Reason: "declared_port_not_listening", At: eventTime(f)})
		} else if service.DeclaredPort != nil && service.ActualPort != nil && service.Running && *service.ActualPort != *service.DeclaredPort {
			declared, actual := *service.DeclaredPort, *service.ActualPort
			events = append(events, Event{Kind: EventManifestConflict, Service: service.Name, RunID: service.RunID, DeclaredPort: &declared, ActualPort: &actual, Reason: "declared_port_mismatch", At: eventTime(f)})
		}
	}
	machines := append([]Machine(nil), f.Machine...)
	sort.SliceStable(machines, func(i, j int) bool {
		if machines[i].Name != machines[j].Name {
			return machines[i].Name < machines[j].Name
		}
		return machines[i].Port < machines[j].Port
	})
	for _, machine := range machines {
		if !machine.Listening {
			events = append(events, Event{Kind: EventMachineOffline, Machine: machine.Name, Port: machine.Port, Reason: "not_listening", At: eventTime(f)})
		}
	}
	listeners := append([]Listener(nil), f.Listeners...)
	sort.SliceStable(listeners, func(i, j int) bool {
		if listeners[i].Port != listeners[j].Port {
			return listeners[i].Port < listeners[j].Port
		}
		return listeners[i].ProjectRoot < listeners[j].ProjectRoot
	})
	declared := make(map[int]bool)
	for _, service := range services {
		if service.DeclaredPort != nil {
			declared[*service.DeclaredPort] = true
		}
		if service.ActualPort != nil {
			declared[*service.ActualPort] = true
		}
	}
	seenPorts := make(map[int]bool)
	for _, listener := range listeners {
		if listener.Port <= 0 || declared[listener.Port] || seenPorts[listener.Port] || !listenerInRoot(listener, f.WorktreeRoot) {
			continue
		}
		seenPorts[listener.Port] = true
		port := listener.Port
		events = append(events, Event{Kind: EventUndeclared, Port: port, Reason: "no_service_declares_port", At: eventTime(f)})
	}
	return events
}

func listenerInRoot(listener Listener, root string) bool {
	if strings.TrimSpace(root) == "" {
		return false
	}
	path := strings.TrimSpace(listener.Cwd)
	if path == "" {
		path = strings.TrimSpace(listener.ProjectRoot)
	}
	if path == "" {
		// A host snapshot must include one of the paths above before it can
		// claim a listener. Do not turn an incomplete row into a machine-wide
		// port alert when the worktree root is known.
		return false
	}
	rootAbs, errRoot := filepath.Abs(root)
	pathAbs, errPath := filepath.Abs(path)
	if errRoot != nil || errPath != nil {
		return false
	}
	// macOS commonly publishes /private/var/... for a path the caller knew
	// as /var/.... Resolve existing paths before the containment check.
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	if resolved, err := filepath.EvalSymlinks(pathAbs); err == nil {
		pathAbs = resolved
	}
	rel, err := filepath.Rel(filepath.Clean(rootAbs), filepath.Clean(pathAbs))
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func crashThreshold(f Facts) int {
	if f.CrashThreshold > 0 {
		return f.CrashThreshold
	}
	return DefaultCrashThreshold
}

func crashWindow(f Facts) time.Duration {
	if f.CrashWindow > 0 {
		return f.CrashWindow
	}
	return DefaultCrashWindow
}

func recentCrashes(f Facts, service string, current *Exit) int {
	observed := f.ObservedAt
	if observed.IsZero() {
		for _, exit := range f.History {
			if exit.Service == service && exit.At.After(observed) {
				observed = exit.At
			}
		}
		if current != nil && current.At.After(observed) {
			observed = current.At
		}
		if observed.IsZero() {
			return 0
		}
	}
	cutoff := observed.Add(-crashWindow(f))
	count := 0
	seen := make(map[string]bool)
	for _, exit := range f.History {
		if exit.Service != service || exit.Reason != "crashed" || exit.At.IsZero() || exit.At.Before(cutoff) || exit.At.After(observed) {
			continue
		}
		key := exit.RunID
		if key == "" {
			key = exit.Service + "\x00" + exit.At.UTC().Format(time.RFC3339Nano)
		}
		if !seen[key] {
			seen[key] = true
			count++
		}
	}
	if current != nil && current.Reason == "crashed" && !current.At.IsZero() && !current.At.Before(cutoff) && !current.At.After(observed) {
		key := current.RunID
		if key == "" {
			key = service + "\x00" + current.At.UTC().Format(time.RFC3339Nano)
		}
		if !seen[key] {
			count++
		}
	}
	return count
}

// Classify returns the first deterministic anomaly for compatibility with
// callers that only display one event. Use ClassifyAll for handoff artifacts.
func Classify(f Facts) (Event, bool) {
	events := ClassifyAll(f)
	if len(events) == 0 {
		return Event{}, false
	}
	return events[0], true
}

// Derive creates a fact-only artifact. Assessment is intentionally nil until
// an optional external adapter adds a Jev result.
func Derive(f Facts, now time.Time, ttl time.Duration) (Artifact, bool) {
	events := ClassifyAll(f)
	if len(events) == 0 {
		return Artifact{}, false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if strings.TrimSpace(f.StateRevision) == "" {
		f.StateRevision = Revision(f)
	}
	event := events[0]
	artifact := Artifact{
		Schema:        Schema,
		EventID:       eventIDs(f, events),
		StateRevision: f.StateRevision,
		WorktreeRoot:  filepath.Clean(f.WorktreeRoot),
		Branch:        strings.TrimSpace(f.Branch),
		GeneratedAt:   now.UTC().Format(time.RFC3339Nano),
		FreshUntil:    now.UTC().Add(ttl).Format(time.RFC3339Nano),
		FirstSeen:     now.UTC().Format(time.RFC3339Nano),
		LastSeen:      now.UTC().Format(time.RFC3339Nano),
		Event:         event,
		Events:        append([]Event(nil), events...),
		Evidence:      evidenceForFacts(f, events),
		Options:       optionsForAll(events),
	}
	return artifact, true
}

// Recover returns a short-lived derived record for an anomaly that is no
// longer present. It carries the prior event identity and first-seen time so
// an adapter can close its handoff without inventing a second state store.
// Callers may publish it before Clear, or simply use it as an audit value.
func Recover(previous Artifact, now time.Time, ttl time.Duration) Artifact {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	at := now.UTC().Format(time.RFC3339Nano)
	previous.LastSeen = at
	previous.RecoveredAt = at
	previous.FreshUntil = now.UTC().Add(ttl).Format(time.RFC3339Nano)
	previous.Assessment = nil
	return previous
}

func optionsFor(kind EventKind) []Option {
	switch kind {
	case EventMachineOffline:
		return []Option{{ID: "inspect_dependency"}, {ID: "continue_without_action"}, {ID: "ask_user_for_context"}}
	case EventPortOccupied:
		return []Option{{ID: "inspect_logs"}, {ID: "inspect_port"}, {ID: "ask_user_for_context"}}
	case EventHealthFailed:
		return []Option{{ID: "inspect_logs"}, {ID: "restart_service"}, {ID: "ask_user_for_context"}}
	case EventUndeclared:
		return []Option{{ID: "inspect_port"}, {ID: "inspect_manifest"}, {ID: "ask_user_for_context"}}
	case EventManifestConflict:
		return []Option{{ID: "inspect_manifest"}, {ID: "inspect_logs"}, {ID: "ask_user_for_context"}}
	case EventRepeatedCrash:
		return []Option{{ID: "inspect_logs"}, {ID: "restart_service"}, {ID: "ask_user_for_context"}}
	default:
		return []Option{{ID: "inspect_logs"}, {ID: "inspect_manifest"}, {ID: "restart_service"}, {ID: "continue_without_action"}}
	}
}

func optionsForAll(events []Event) []Option {
	seen := make(map[string]bool)
	options := make([]Option, 0, 4)
	for _, event := range events {
		for _, option := range optionsFor(event.Kind) {
			if seen[option.ID] {
				continue
			}
			seen[option.ID] = true
			options = append(options, option)
		}
	}
	return options
}

func evidenceFor(event Event) []Evidence {
	return evidenceForFacts(Facts{}, []Event{event})
}

func evidenceForFacts(f Facts, events []Event) []Evidence {
	evidence := make([]Evidence, 0, len(events)*2)
	for _, event := range events {
		for _, item := range evidenceForOne(f, event) {
			evidence = append(evidence, item)
		}
	}
	return evidence
}

func evidenceForOne(f Facts, event Event) []Evidence {
	if event.Service != "" {
		if event.Kind == EventRepeatedCrash {
			return []Evidence{{Ref: "status.exits[" + event.Service + "]"}, logEvidence(f, event.Service, event.RunID)}
		}
		if event.Kind == EventManifestConflict {
			return []Evidence{{Ref: "status.worktree.services[" + event.Service + "].port"}, {Ref: "status.worktree.services[" + event.Service + "].port_actual"}}
		}
		return []Evidence{{Ref: "status.worktree.services[" + event.Service + "].last_exit"}, logEvidence(f, event.Service, event.RunID)}
	}
	if event.Machine != "" {
		return []Evidence{{Ref: "status.worktree.machine[" + event.Machine + "]"}}
	}
	if event.Kind == EventUndeclared {
		return []Evidence{{Ref: "status.ports[" + fmt.Sprint(event.Port) + "]"}}
	}
	return nil
}

func logEvidence(f Facts, service, runID string) Evidence {
	ref := "logs:" + service
	if runID != "" {
		ref += ":" + runID
	}
	for _, item := range f.Services {
		if item.Name == service {
			return Evidence{Ref: ref, Service: service, RunID: runID, LogPath: item.LogPath}
		}
	}
	return Evidence{Ref: ref, Service: service, RunID: runID}
}

func evidenceForAll(events []Event) []Evidence {
	seen := make(map[string]bool)
	evidence := make([]Evidence, 0, len(events)*2)
	for _, event := range events {
		for _, item := range evidenceFor(event) {
			if seen[item.Ref] {
				continue
			}
			seen[item.Ref] = true
			evidence = append(evidence, item)
		}
	}
	return evidence
}

func eventID(f Facts, event Event) string {
	return eventIDs(f, []Event{event})
}

func eventIDs(f Facts, events []Event) string {
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s\x00%s\x00%s", filepath.Clean(f.WorktreeRoot), f.Branch, f.StateRevision)
	for _, event := range events {
		// At is evidence, not identity: dependency polls use a new observation
		// timestamp each time and must not create a new request identity.
		_, _ = fmt.Fprintf(h, "\x00%s\x00%s\x00%s\x00%s\x00%d\x00%s", event.Kind, event.Service, event.Machine, event.RunID, event.Port, event.Reason)
		if event.DeclaredPort != nil {
			_, _ = fmt.Fprintf(h, "\x00declared\x00%d", *event.DeclaredPort)
		}
		if event.ActualPort != nil {
			_, _ = fmt.Fprintf(h, "\x00actual\x00%d", *event.ActualPort)
		}
		if event.Count > 0 {
			_, _ = fmt.Fprintf(h, "\x00count\x00%d", event.Count)
		}
		if event.ExitCode != nil {
			_, _ = fmt.Fprintf(h, "\x00exit_code\x00%d", *event.ExitCode)
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:24]
}

func eventTime(f Facts) string {
	at := eventTimeValue(f)
	if at.IsZero() {
		// No clock-bearing fact was supplied. Keep classification a pure
		// function of the facts instead of introducing a polling-time value.
		return ""
	}
	return at.UTC().Format(time.RFC3339Nano)
}

func eventTimeValue(f Facts) time.Time {
	if !f.ObservedAt.IsZero() {
		return f.ObservedAt
	}
	var latest time.Time
	for _, service := range f.Services {
		if service.LastExit != nil && service.LastExit.At.After(latest) {
			latest = service.LastExit.At
		}
	}
	for _, exit := range f.History {
		if exit.At.After(latest) {
			latest = exit.At
		}
	}
	return latest
}

// Path returns the canonical per-worktree artifact location under home.
func Path(home, root string) string {
	h := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(home, AttentionDir, hex.EncodeToString(h[:])[:24]+".json")
}

var artifactMu sync.Mutex

// Write atomically publishes an artifact with owner-only permissions. If the
// previous artifact represents the same event at the same state revision, its
// adapter assessment is retained while generated/freshness metadata is
// refreshed. This keeps a status poll from erasing a Jev result.
func Write(home string, artifact Artifact) (string, error) {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	var path string
	err := withArtifactLock(home, func() error {
		var err error
		path, err = write(home, artifact, true)
		return err
	})
	return path, err
}

func write(home string, artifact Artifact, preserveAssessment bool) (string, error) {
	if strings.TrimSpace(home) == "" {
		return "", errors.New("attention: empty home")
	}
	if strings.TrimSpace(artifact.WorktreeRoot) == "" {
		return "", errors.New("attention: empty worktree root")
	}
	if artifact.Schema == "" {
		artifact.Schema = Schema
	}
	if artifact.FirstSeen == "" {
		artifact.FirstSeen = artifact.GeneratedAt
	}
	if artifact.LastSeen == "" {
		artifact.LastSeen = artifact.GeneratedAt
	}
	path := Path(home, artifact.WorktreeRoot)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("attention: create directory: %w", err)
	}
	if preserveAssessment {
		if previous, err := Read(path); err == nil &&
			previous.EventID == artifact.EventID &&
			previous.StateRevision == artifact.StateRevision &&
			previous.Assessment != nil && artifact.Assessment == nil {
			artifact.Assessment = cloneAssessment(previous.Assessment)
		}
		if previous, err := Read(path); err == nil &&
			previous.EventID == artifact.EventID &&
			previous.StateRevision == artifact.StateRevision && previous.FirstSeen != "" {
			artifact.FirstSeen = previous.FirstSeen
		}
	}
	data, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return "", fmt.Errorf("attention: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".attention-*.tmp")
	if err != nil {
		return "", fmt.Errorf("attention: create temporary file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("attention: chmod: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("attention: write: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", fmt.Errorf("attention: sync: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("attention: close: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", fmt.Errorf("attention: publish: %w", err)
	}
	return path, nil
}

func Read(path string) (Artifact, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return Artifact{}, fmt.Errorf("attention: decode: %w", err)
	}
	if artifact.Schema != Schema {
		return Artifact{}, fmt.Errorf("attention: unsupported schema %q", artifact.Schema)
	}
	return artifact, nil
}

// ReadFresh reads an artifact and rejects it when its freshness deadline has
// passed. A caller should use this immediately before presenting or acting on
// a request; status revision and event identity still need to be compared
// after the read when another status poll may have raced with the caller.
func ReadFresh(path string, now time.Time) (Artifact, error) {
	artifact, err := Read(path)
	if err != nil {
		return Artifact{}, err
	}
	if err := ValidateFreshness(artifact, now); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

// ValidateFreshness checks the explicit FreshUntil marker in an artifact.
// Zero now means the current UTC time. The generated timestamp is not used as
// a clock anchor, so callers can deliberately choose a test or observed time.
func ValidateFreshness(artifact Artifact, now time.Time) error {
	deadline, err := time.Parse(time.RFC3339Nano, artifact.FreshUntil)
	if err != nil || deadline.IsZero() {
		return errors.New("attention: invalid freshness deadline")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if !now.Before(deadline) {
		return fmt.Errorf("attention: artifact expired at %s", deadline.UTC().Format(time.RFC3339Nano))
	}
	return nil
}

// IsFresh is the non-error form of ValidateFreshness for readers that only
// need a guard before showing optional agent context.
func IsFresh(artifact Artifact, now time.Time) bool {
	return ValidateFreshness(artifact, now) == nil
}

// UpdateAssessment validates and atomically persists an adapter assessment.
// It is the safe read-modify-write path for a Jev adapter; status writes use
// Write and preserve the resulting assessment when identity and revision
// still match.
func UpdateAssessment(path string, assessment Assessment) error {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	home := filepath.Dir(filepath.Dir(path))
	return withArtifactLock(home, func() error {
		artifact, err := Read(path)
		if err != nil {
			return err
		}
		if err := AttachAssessment(&artifact, assessment); err != nil {
			return err
		}
		if filepath.Clean(Path(home, artifact.WorktreeRoot)) != filepath.Clean(path) {
			return errors.New("attention: artifact path is not under its BERTH_HOME")
		}
		_, err = write(home, artifact, false)
		return err
	})
}

// CompareAndSwap applies an adapter update only while the artifact still has
// the expected event and state revision. The comparison and write happen
// under the cross-process attention lock, so a status refresh or Clear in a
// different CLI process cannot be resurrected by a stale adapter response.
func CompareAndSwap(path, expectedEventID, expectedRevision string, update func(*Artifact) error) error {
	if update == nil {
		return errors.New("attention: nil compare-and-swap update")
	}
	artifactMu.Lock()
	defer artifactMu.Unlock()
	home := filepath.Dir(filepath.Dir(path))
	return withArtifactLock(home, func() error {
		latest, err := Read(path)
		if err != nil {
			return err
		}
		if latest.EventID != expectedEventID || latest.StateRevision != expectedRevision {
			return ErrStale
		}
		if filepath.Clean(Path(home, latest.WorktreeRoot)) != filepath.Clean(path) {
			return errors.New("attention: artifact path is not under its BERTH_HOME")
		}
		if err := update(&latest); err != nil {
			return err
		}
		// The callback is an adapter update, not a second source of facts.
		// Keep the CAS identity and destination immutable even if a caller
		// accidentally mutates unrelated artifact fields in the callback.
		if latest.EventID != expectedEventID || latest.StateRevision != expectedRevision {
			return ErrStale
		}
		if filepath.Clean(Path(home, latest.WorktreeRoot)) != filepath.Clean(path) {
			return errors.New("attention: artifact path is not under its BERTH_HOME")
		}
		_, err = write(home, latest, false)
		return err
	})
}

func cloneAssessment(assessment *Assessment) *Assessment {
	if assessment == nil {
		return nil
	}
	copy := *assessment
	if assessment.Probabilities != nil {
		copy.Probabilities = make(map[string]float64, len(assessment.Probabilities))
		for option, probability := range assessment.Probabilities {
			copy.Probabilities[option] = probability
		}
	}
	return &copy
}

// AttachAssessment validates and attaches a typed adapter result. It accepts
// only the fixed option IDs already present in the artifact; free-form model
// commands cannot enter the handoff document.
func AttachAssessment(artifact *Artifact, assessment Assessment) error {
	if artifact == nil {
		return errors.New("attention: nil artifact")
	}
	if assessment.NeedsHuman != nil && !probability(*assessment.NeedsHuman) {
		return errors.New("attention: needs_human must be between 0 and 1")
	}
	if assessment.TaskRelevant != nil && !probability(*assessment.TaskRelevant) {
		return errors.New("attention: task_relevant must be between 0 and 1")
	}
	if assessment.Confidence != nil && !probability(*assessment.Confidence) {
		return errors.New("attention: confidence must be between 0 and 1")
	}
	allowed := make(map[string]bool, len(artifact.Options))
	for _, option := range artifact.Options {
		allowed[option.ID] = true
	}
	if assessment.NextOption != "" && !allowed[assessment.NextOption] {
		return fmt.Errorf("attention: unknown option %q", assessment.NextOption)
	}
	if len(assessment.Probabilities) > 0 {
		total := 0.0
		for option, value := range assessment.Probabilities {
			if !allowed[option] {
				return fmt.Errorf("attention: probability for unknown option %q", option)
			}
			if !probability(value) {
				return fmt.Errorf("attention: probability for %q must be between 0 and 1", option)
			}
			total += value
		}
		if math.Abs(total-1) > 0.01 {
			return fmt.Errorf("attention: probabilities sum to %.4f, want 1", total)
		}
	}
	copy := assessment
	if assessment.Probabilities != nil {
		copy.Probabilities = make(map[string]float64, len(assessment.Probabilities))
		for key, value := range assessment.Probabilities {
			copy.Probabilities[key] = value
		}
	}
	artifact.Assessment = &copy
	return nil
}

func probability(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0) && value >= 0 && value <= 1
}

// Clear removes the current artifact for a worktree. A missing artifact is
// already clear and is not an error.
func Clear(home, root string) error {
	artifactMu.Lock()
	defer artifactMu.Unlock()
	return withArtifactLock(home, func() error {
		err := os.Remove(Path(home, root))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	})
}
