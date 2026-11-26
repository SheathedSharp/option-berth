package attnengine

// The engine has two steps per world and they deliberately cannot block each
// other. Step A is deterministic, in-process and always on. Step B is optional,
// asynchronous and bounded: it never runs without consent, never runs more than
// Workers at a time, coalesces by worktree so a burst of scans cannot pile up,
// and gives up for a cooldown after three consecutive transport failures.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

const (
	defaultWorkers       = 2
	defaultCooldown      = 60 * time.Second
	breakerThreshold     = 3
	triageQueueBound     = 16
	retriageAfterFailure = 60 * time.Second
	triageTaskSummary    = "Triage this worktree's runtime state for a coding agent."
)

// Options is everything the engine needs. Resolve is a callback rather than a
// value so a person turning Attention off takes effect on the next scan without
// restarting the runtime service.
type Options struct {
	Home    string
	Logger  *slog.Logger
	Resolve func() model.Resolved
	// HTTPClient is injected in tests; nil uses the client's default.
	HTTPClient *http.Client
	// Workers bounds concurrent triage calls. Zero means 2.
	Workers int
	// Cooldown is how long the breaker stays open after three consecutive
	// transport failures. Zero means 60s.
	Cooldown time.Duration
	// Now is injected in tests; nil uses time.Now.
	Now func() time.Time
}

// Engine tracks one note document per worktree and optionally triages it.
type Engine struct {
	opts   Options
	store  NoteStore
	logger *slog.Logger

	// observeMu serializes Step A across worktrees. Observers arrive one world
	// at a time already; the lock is here so a direct caller cannot interleave
	// two read-modify-writes of the same document.
	observeMu sync.Mutex

	queue   chan triageJob
	mu      sync.Mutex
	pending map[string]bool

	cancel  context.CancelFunc
	wg      sync.WaitGroup
	started bool

	brMu      sync.Mutex
	failures  int
	openUntil time.Time
}

type triageJob struct {
	root     string
	world    runtimecore.World
	resolved model.Resolved
}

// New returns an engine. It does nothing until Start is called: constructing an
// engine must never start a goroutine that nobody stopped.
func New(opts Options) *Engine {
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.Cooldown <= 0 {
		opts.Cooldown = defaultCooldown
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		opts:    opts,
		store:   NoteStore{Home: opts.Home},
		logger:  logger,
		queue:   make(chan triageJob, triageQueueBound),
		pending: map[string]bool{},
	}
}

// Store exposes the note store the engine writes through, so a command can read
// the same documents the runtime service produces.
func (e *Engine) Store() NoteStore { return e.store }

// Start launches the triage workers. A second Start is a no-op, and a
// cancelled context stops them exactly as Stop does.
func (e *Engine) Start(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.started || e.opts.Home == "" {
		return
	}
	runCtx, cancel := context.WithCancel(ctx)
	e.cancel = cancel
	e.started = true
	for i := 0; i < e.opts.Workers; i++ {
		e.wg.Add(1)
		go e.work(runCtx)
	}
}

// Stop cancels the workers and waits for every one of them, so a process that
// stops the engine holds no goroutine after it returns.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel := e.cancel
	e.cancel = nil
	e.started = false
	e.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	e.wg.Wait()
}

// Observe is the Runtime Service observer. It derives the deterministic notes
// for this world, persists them when they changed, and — only when the person
// enabled Attention — enqueues one bounded triage.
func (e *Engine) Observe(world runtimecore.World) {
	root := filepath.Clean(strings.TrimSpace(world.Worktree.Root))
	if root == "" || root == "." || e.opts.Home == "" {
		return
	}
	e.observeMu.Lock()
	defer e.observeMu.Unlock()

	var doc WorktreeNotes
	lockErr := e.store.withLock(root, func() error {
		previous, loadErr := e.store.Load(root)
		if loadErr != nil {
			// A corrupt document is replaced by a fresh derivation: derived notes
			// are cheap, and refusing to write would leave the file broken forever.
			e.logger.Warn("attention notes unreadable; deriving fresh", "worktree", root, "error", loadErr)
			previous = WorktreeNotes{WorktreeRoot: root}
		}
		var changed bool
		doc, changed = DeriveNotes(world, previous, e.now())
		// A revision or health move is news even when the note set is identical,
		// but only worth a write while something is live: a healthy worktree with
		// no notes costs no disk at all.
		revisionMoved := previous.WorldRevision != doc.WorldRevision || previous.WorldHealth != doc.WorldHealth
		if changed || (len(ActiveNotes(doc)) > 0 && revisionMoved) {
			return e.store.Save(doc)
		}
		return nil
	})
	if lockErr != nil {
		e.logger.Warn("attention notes not saved", "worktree", root, "error", lockErr)
		return
	}
	e.maybeTriage(world, doc)
}

func (e *Engine) now() time.Time { return e.opts.Now().UTC() }

func (e *Engine) maybeTriage(world runtimecore.World, doc WorktreeNotes) {
	if e.opts.Resolve == nil {
		return
	}
	now := e.now()
	wanted := false
	for _, note := range ActiveNotes(doc) {
		if noteWantsTriage(note, now) {
			wanted = true
			break
		}
	}
	if !wanted || e.breakerOpen(now) {
		return
	}
	resolved := e.opts.Resolve()
	if !resolved.Enabled || resolved.Reason != "" {
		return
	}
	e.enqueue(triageJob{root: doc.WorktreeRoot, world: world, resolved: resolved})
}

func noteWantsTriage(note Note, now time.Time) bool {
	if note.Triage == nil {
		return true
	}
	if note.Triage.UnavailableReason == "" {
		return false
	}
	at, err := time.Parse(time.RFC3339Nano, note.Triage.TriagedAt)
	if err != nil {
		return true
	}
	return now.Sub(at) >= retriageAfterFailure
}

// enqueue coalesces by worktree: while a job for this root is queued or
// running, another observe adds nothing, and the next scan after it completes
// enqueues the fresher world.
func (e *Engine) enqueue(job triageJob) {
	e.mu.Lock()
	if e.cancel == nil {
		e.mu.Unlock()
		return
	}
	if e.pending[job.root] {
		e.mu.Unlock()
		return
	}
	e.pending[job.root] = true
	e.mu.Unlock()
	select {
	case e.queue <- job:
	default:
		e.mu.Lock()
		delete(e.pending, job.root)
		e.mu.Unlock()
	}
}

func (e *Engine) work(ctx context.Context) {
	defer e.wg.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-e.queue:
			e.runTriage(ctx, job)
			e.mu.Lock()
			delete(e.pending, job.root)
			e.mu.Unlock()
		}
	}
}

func (e *Engine) runTriage(ctx context.Context, job triageJob) {
	client, err := job.resolved.Client()
	if err != nil {
		// A configuration that cannot build a client will not fix itself
		// between two scans, so the note records the reason and waits out the
		// retry floor instead of asking again on every tick.
		e.attachUnavailable(job, err)
		return
	}
	if client.HTTPClient == nil {
		client.HTTPClient = e.opts.HTTPClient
	}
	timeout := job.resolved.Timeout
	if timeout <= 0 {
		timeout = model.DefaultTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := client.EvaluateWorld(callCtx, job.world, model.TaskContext{Task: triageTaskSummary})
	if err != nil {
		// A transport failure leaves the note untriaged so the breaker — not a
		// recorded reason — is what throttles the next attempt. A wrong answer
		// is recorded with its fixed reason: retrying a model that keeps
		// answering badly is the caller's decision, not a loop.
		if transportFailure(err) {
			e.recordFailure(err)
			return
		}
		e.attachUnavailable(job, err)
		return
	}
	e.resetBreaker()
	e.attachTriage(job, result)
}

// attachTriage writes the recommendation under the note lock and only while the
// document still describes the revision the answer was about. A slow answer to
// an old world is dropped, not attached.
func (e *Engine) attachTriage(job triageJob, result model.WorldTriageResult) {
	if err := e.store.withLock(job.root, func() error {
		doc, err := e.store.Load(job.root)
		if err != nil {
			return err
		}
		if doc.WorldRevision != job.world.Revision {
			return nil
		}
		changed := false
		for i := range doc.Notes {
			note := &doc.Notes[i]
			if !note.live() {
				continue
			}
			note.Triage = triageResultFor(note, job, result, e.now())
			if note.Status == NoteOpen {
				note.Status = NoteTriaged
			}
			changed = true
		}
		if !changed {
			return nil
		}
		doc.UpdatedAt = e.now().Format(time.RFC3339Nano)
		return e.store.Save(doc)
	}); err != nil {
		e.logger.Warn("attention triage not attached", "worktree", job.root, "error", err)
	}
}

// attachUnavailable records the fixed reason a triage could not run, so
// `oberth attn notes` can say why instead of looking unconfigured.
func (e *Engine) attachUnavailable(job triageJob, cause error) {
	reason := model.ReasonFor(cause)
	if reason == "" {
		return
	}
	if err := e.store.withLock(job.root, func() error {
		doc, err := e.store.Load(job.root)
		if err != nil {
			return err
		}
		if doc.WorldRevision != job.world.Revision {
			return nil
		}
		stamp := e.now().Format(time.RFC3339Nano)
		changed := false
		for i := range doc.Notes {
			note := &doc.Notes[i]
			if !note.live() {
				continue
			}
			note.Triage = &TriageResult{
				Mode: string(job.resolved.Mode), Model: job.resolved.Model,
				TriagedAt: stamp, UnavailableReason: reason,
			}
			changed = true
		}
		if !changed {
			return nil
		}
		doc.UpdatedAt = stamp
		return e.store.Save(doc)
	}); err != nil {
		e.logger.Warn("attention triage failure not recorded", "worktree", job.root, "error", err)
	}
}

func triageResultFor(note *Note, job triageJob, result model.WorldTriageResult, now time.Time) *TriageResult {
	taskRelevant, needsHuman, urgency, confidence := result.TaskRelevant, result.NeedsHuman, result.Urgency, result.Confidence
	return &TriageResult{
		Mode:                  string(job.resolved.Mode),
		Model:                 result.Model,
		TaskRelevant:          &taskRelevant,
		NeedsHuman:            &needsHuman,
		Urgency:               urgency,
		NextOption:            result.NextOption,
		Confidence:            confidence,
		Summary:               result.Summary,
		TriagedAt:             now.UTC().Format(time.RFC3339Nano),
		AutoReconcileEligible: reconcileEligible(*note, job, result),
	}
}

// reconcileEligible is the bounded rule behind the reconcile mode: a local
// restart of this worktree's own service, no person needed, a confident answer,
// no crash loop and no listener this run does not own. Everything else stays a
// human decision even when the model is certain.
func reconcileEligible(note Note, job triageJob, result model.WorldTriageResult) bool {
	if job.resolved.Mode != string(model.ModeReconcile) {
		return false
	}
	if note.Safety != runtimecore.SafetyLocalRestart {
		return false
	}
	if result.NeedsHuman > 0.25 {
		return false
	}
	if result.Confidence == nil || *result.Confidence < 0.80 {
		return false
	}
	if note.Kind == runtimecore.DriftRepeatedCrash || (note.Loop != nil && note.Loop.Kind == runtimecore.LoopCrashLoop) {
		return false
	}
	for _, service := range job.world.Services {
		if service.Name != note.Service || service.PortHolder == nil {
			continue
		}
		// A previous generation of the same service is ours to replace; any
		// other listener is a person's call.
		if service.PortHolder.Kind != state.PortHolderPrevious {
			return false
		}
	}
	return true
}

func (e *Engine) breakerOpen(now time.Time) bool {
	e.brMu.Lock()
	defer e.brMu.Unlock()
	return now.Before(e.openUntil)
}

func (e *Engine) recordFailure(err error) {
	if !transportFailure(err) {
		return
	}
	e.brMu.Lock()
	defer e.brMu.Unlock()
	e.failures++
	if e.failures >= breakerThreshold {
		e.openUntil = e.now().Add(e.opts.Cooldown)
		e.failures = 0
		e.logger.Warn("attention triage paused after repeated transport failures",
			"reason", model.ReasonFor(err), "cooldown", e.opts.Cooldown)
	}
}

func (e *Engine) resetBreaker() {
	e.brMu.Lock()
	defer e.brMu.Unlock()
	e.failures = 0
	e.openUntil = time.Time{}
}

// transportFailure says which failures mean the endpoint is unreachable rather
// than the answer being wrong. Only those open the breaker: a provider that
// keeps answering badly is a configuration problem, not a network outage, and
// silencing it would hide the fixed reason from the person.
func transportFailure(err error) bool {
	switch model.ReasonFor(err) {
	case model.ReasonTimeout, model.ReasonNetwork, model.ReasonProvider,
		model.ReasonRateLimited, model.ReasonUnauthorized:
		return true
	}
	return false
}

// ------------------------------------------------------------- derivation ---

// DeriveNotes reduces a world to tracked notes against the previous document.
// It is pure: the same world, the same previous document and the same clock
// produce the same document, and no file is read or written here. The bool says
// whether the note content changed; a revision or health move is the caller's
// second reason to save.
func DeriveNotes(world runtimecore.World, previous WorktreeNotes, now time.Time) (WorktreeNotes, bool) {
	root := filepath.Clean(strings.TrimSpace(world.Worktree.Root))
	stamp := now.UTC().Format(time.RFC3339Nano)
	doc := WorktreeNotes{
		Schema:        NotesSchema,
		WorktreeID:    WorktreeID(root),
		WorktreeRoot:  root,
		Group:         world.Worktree.Group,
		WorldRevision: world.Revision,
		WorldHealth:   world.Health,
		UpdatedAt:     stamp,
	}

	candidates := candidateNotes(world, root)
	previousByID := make(map[string]Note, len(previous.Notes))
	for _, note := range previous.Notes {
		previousByID[note.ID] = note
	}
	current := make(map[string]bool, len(candidates))

	merged := make([]Note, 0, len(candidates)+len(previous.Notes))
	changed := false
	for _, candidate := range candidates {
		current[candidate.ID] = true
		before, known := previousByID[candidate.ID]
		if !known {
			candidate.Status = NoteOpen
			candidate.FirstSeen, candidate.LastSeen = stamp, stamp
			merged = append(merged, candidate)
			changed = true
			continue
		}
		note := before
		note.WorktreeID, note.WorktreeRoot = candidate.WorktreeID, candidate.WorktreeRoot
		note.Group, note.WorldRevision = candidate.Group, candidate.WorldRevision
		note.Severity, note.Summary = candidate.Severity, candidate.Summary
		note.Impacted, note.Command, note.Safety = candidate.Impacted, candidate.Command, candidate.Safety
		note.Loop = candidate.Loop
		if candidate.FirstError != "" {
			note.FirstError = candidate.FirstError
		}
		if note.Status == NoteResolved || note.Status == "" {
			// The same identity is live again: a new occurrence, not a
			// resurrection of the old delivery cursor.
			note.Status = NoteOpen
			note.FirstSeen = stamp
			note.ResolvedAt = ""
			note.DeliveredAt, note.DeliveredRev = "", ""
			note.Triage = nil
			changed = true
		}
		note.LastSeen = stamp
		if noteSignature(note) != noteSignature(before) {
			changed = true
		}
		merged = append(merged, note)
	}

	for _, before := range previous.Notes {
		if current[before.ID] {
			continue
		}
		switch before.Status {
		case NoteResolved, NoteSuppressed:
			merged = append(merged, before)
		default:
			resolved := before
			resolved.Status = NoteResolved
			resolved.ResolvedAt = stamp
			resolved.WorldRevision = world.Revision
			resolved.Loop = nil
			merged = append(merged, resolved)
			changed = true
		}
	}

	doc.Notes = orderNotes(merged)
	return doc, changed
}

// candidateNotes is the deterministic half: every current root cause, every
// non-fatal drift, and every loop signal no root cause already names.
func candidateNotes(world runtimecore.World, root string) []Note {
	base := Note{
		WorktreeID:    WorktreeID(root),
		WorktreeRoot:  root,
		Group:         world.Worktree.Group,
		WorldRevision: world.Revision,
	}
	var notes []Note
	rootedServices := map[string]bool{}
	for _, cause := range world.Diagnosis.RootCauses {
		note := base
		note.ID = "cause:" + causeIdentity(cause)
		note.Service = cause.Service
		note.Kind = cause.Kind
		note.Severity = cause.Severity
		note.Summary = cause.Summary
		note.Impacted = append([]string(nil), cause.Impacted...)
		note.FirstError = model.RedactLine(cause.FirstError)
		note.Command = cause.Command
		note.Safety = safetyFor(cause.Kind, cause.Command)
		notes = append(notes, note)
		rootedServices[cause.Service] = true
	}
	for _, service := range world.Services {
		for _, drift := range service.Drifts {
			switch drift.Kind {
			case runtimecore.DriftCodeStale, runtimecore.DriftBinaryStale, runtimecore.DriftManifestStale:
			default:
				continue
			}
			note := base
			note.ID = "drift:" + service.Name + ":" + string(drift.Kind)
			note.Service = service.Name
			note.Kind = drift.Kind
			note.Severity = drift.Severity
			note.Summary = drift.Summary
			note.FirstError = model.RedactLine(service.FirstError)
			note.Command = drift.Command
			note.Safety = safetyFor(drift.Kind, drift.Command)
			notes = append(notes, note)
		}
	}
	for i := range world.Diagnosis.Loops {
		loop := world.Diagnosis.Loops[i]
		if rootedServices[loop.Service] {
			// The root cause already names this service; a second note would
			// be the same alert twice under a different id.
			continue
		}
		summary := loop.Summary
		if summary == "" {
			summary = fmt.Sprintf("%d repeated %s events", loop.Count, loop.Kind)
		}
		note := base
		note.ID = "loop:" + loop.Service + ":" + string(loop.Kind)
		note.Service = loop.Service
		note.Kind = loopDriftKind(loop.Kind)
		note.Severity = runtimecore.SeverityCritical
		note.Summary = summary
		note.Loop = &loop
		note.Command = loop.Recommendation
		note.Safety = runtimecore.SafetyReadOnly
		notes = append(notes, note)
	}
	return notes
}

func causeIdentity(cause runtimecore.RootCause) string {
	if strings.TrimSpace(cause.ID) != "" {
		return cause.ID
	}
	return cause.Service + ":" + string(cause.Kind)
}

func loopDriftKind(kind runtimecore.LoopKind) runtimecore.DriftKind {
	if kind == runtimecore.LoopCrashLoop {
		return runtimecore.DriftRepeatedCrash
	}
	return ""
}

// safetyFor names what kind of act the note's next command performs. It reads
// the command the world already derived rather than inferring one, so a note
// can never suggest an act the reducer did not offer.
func safetyFor(kind runtimecore.DriftKind, command string) string {
	switch {
	case strings.HasPrefix(command, "oberth restart"):
		return runtimecore.SafetyLocalRestart
	case kind == runtimecore.DriftPortOccupied, kind == runtimecore.DriftUnownedListener:
		return runtimecore.SafetyHumanDecision
	default:
		return runtimecore.SafetyReadOnly
	}
}

// noteSignature is the content a change is measured against. LastSeen and the
// per-note revision move on their own and must not count as news.
func noteSignature(note Note) string {
	loop := ""
	if note.Loop != nil {
		loop = string(note.Loop.Kind) + ":" + fmt.Sprint(note.Loop.Count)
	}
	return strings.Join([]string{
		string(note.Status), string(note.Severity), note.Service, string(note.Kind),
		note.Summary, note.FirstError, note.Command, note.Safety,
		strings.Join(note.Impacted, ","), loop,
	}, "\x00")
}

// orderNotes keeps live notes first in the order a person should read them, and
// at most MaxResolvedNotes of history after them.
func orderNotes(notes []Note) []Note {
	var live, resolved []Note
	for _, note := range notes {
		if note.Status == NoteResolved {
			resolved = append(resolved, note)
			continue
		}
		live = append(live, note)
	}
	sort.SliceStable(live, func(i, j int) bool {
		if severityRank(live[i].Severity) != severityRank(live[j].Severity) {
			return severityRank(live[i].Severity) < severityRank(live[j].Severity)
		}
		if live[i].Service != live[j].Service {
			return live[i].Service < live[j].Service
		}
		return live[i].ID < live[j].ID
	})
	sort.SliceStable(resolved, func(i, j int) bool {
		return resolved[i].ResolvedAt > resolved[j].ResolvedAt
	})
	if len(resolved) > MaxResolvedNotes {
		resolved = resolved[:MaxResolvedNotes]
	}
	return append(live, resolved...)
}

// KindLabel is the short word a reader sees for a note's cause. It keeps
// `attn notes` from printing an empty column for a loop with no drift kind.
func KindLabel(note Note) string {
	if note.Kind != "" {
		return string(note.Kind)
	}
	if note.Loop != nil {
		return string(note.Loop.Kind)
	}
	return "finding"
}
