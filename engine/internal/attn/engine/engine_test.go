package attnengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimecore"
)

const testRoot = "/tmp/oberth-attn-engine-test-worktree"

type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func brokenWorld(revision string) runtimecore.World {
	return runtimecore.World{
		Schema:   runtimecore.WorldSchema,
		Revision: revision,
		Worktree: runtimecore.WorldIdentity{Group: "repo", Root: testRoot, Branch: "feat/attention"},
		Coverage: runtimecore.CoverageObservable,
		Health:   runtimecore.WorldBroken,
		Services: []runtimecore.ServiceRuntime{
			{Name: "api", State: runtimecore.ServiceCrashed},
			{Name: "web", State: runtimecore.ServiceReady, Drifts: []runtimecore.Drift{{
				Kind: runtimecore.DriftCodeStale, Severity: runtimecore.SeverityWarning,
				Summary: "code changed since this run started", Command: "oberth restart --only web",
			}}},
			{Name: "worker", State: runtimecore.ServiceCrashLoop},
		},
		Diagnosis: runtimecore.CausalDiagnosis{
			Healthy: false,
			RootCauses: []runtimecore.RootCause{{
				ID: "cause-api", Service: "api", Kind: runtimecore.DriftServiceCrashed,
				Severity: runtimecore.SeverityCritical, Summary: "crashed with exit code 1",
				Impacted: []string{"web"},
				// A realistic first-error line: it carries a token and a home
				// path, and neither may reach the stored note.
				FirstError: "Error: connect failed token=supersecret at /Users/alice/app/.env",
				Command:    "oberth logs api --once",
			}},
			Loops: []runtimecore.LoopSignal{{
				Kind: runtimecore.LoopCrashLoop, Service: "worker", Count: 4,
				Summary: "4 crashes in 10m", Recommendation: "oberth logs worker --once",
			}},
		},
	}
}

func healthyWorld(revision string) runtimecore.World {
	world := brokenWorld(revision)
	world.Health = runtimecore.WorldHealthy
	world.Diagnosis = runtimecore.CausalDiagnosis{Healthy: true, Summary: "all declared services are ready"}
	for i := range world.Services {
		world.Services[i].State = runtimecore.ServiceReady
		world.Services[i].Drifts = nil
	}
	return world
}

func newTestEngine(t *testing.T, opts Options) (*Engine, *NoteStore) {
	t.Helper()
	if opts.Home == "" {
		opts.Home = t.TempDir()
	}
	if opts.Now == nil {
		opts.Now = newFakeClock().now
	}
	engine := New(opts)
	store := NoteStore{Home: opts.Home}
	t.Cleanup(engine.Stop)
	return engine, &store
}

func triageServer(t *testing.T, calls *atomic.Int32, answer func(revision int32) string) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		text := answer(n)
		body, _ := json.Marshal(text)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(body) + `}}]}`))
	}))
}

func assistResolved(url string) model.Resolved {
	return model.Resolved{
		Enabled: true, Endpoint: url, Model: "test-model", Key: "k",
		Protocol: string(model.ProtocolOpenAIChat), Mode: string(model.ModeAssist),
		Timeout: 5 * time.Second, IncludeErrorLine: true,
	}
}

func waitFor(t *testing.T, what string, fn func() bool) {
	t.Helper()
	until := time.Now().Add(5 * time.Second)
	for !fn() {
		if time.Now().After(until) {
			t.Fatalf("condition never reached: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Step A: a broken world always becomes notes, with no key and no settings.
func TestObserveDerivesTrackedNotesWithoutAnyProvider(t *testing.T) {
	engine, store := newTestEngine(t, Options{})
	engine.Observe(brokenWorld("rev-1"))

	doc, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Schema != NotesSchema || doc.WorldRevision != "rev-1" || doc.WorldHealth != runtimecore.WorldBroken {
		t.Fatalf("document = %+v", doc)
	}
	if len(doc.Notes) != 3 {
		t.Fatalf("notes = %d, want 3: %+v", len(doc.Notes), doc.Notes)
	}
	kinds := map[string]Note{}
	for _, note := range doc.Notes {
		kinds[note.ID] = note
		if note.Status != NoteOpen || note.FirstSeen == "" || note.LastSeen == "" {
			t.Errorf("note %s = %+v, want a fresh open note", note.ID, note)
		}
		if note.WorktreeRoot != testRoot || note.WorktreeID != WorktreeID(testRoot) {
			t.Errorf("note %s has the wrong identity: %+v", note.ID, note)
		}
	}
	cause, ok := kinds["cause:cause-api"]
	if !ok {
		t.Fatalf("root cause note missing: %+v", doc.Notes)
	}
	if cause.Safety != runtimecore.SafetyReadOnly || cause.Command != "oberth logs api --once" {
		t.Fatalf("cause note = %+v", cause)
	}
	if strings.Contains(cause.FirstError, "supersecret") || strings.Contains(cause.FirstError, "/Users/alice") {
		t.Fatalf("stored first error was not redacted: %q", cause.FirstError)
	}
	if _, ok := kinds["drift:web:code_stale"]; !ok {
		t.Fatalf("drift note missing: %+v", doc.Notes)
	}
	if loop, ok := kinds["loop:worker:crash_loop"]; !ok || loop.Loop == nil || loop.Loop.Count != 4 {
		t.Fatalf("loop note missing: %+v", doc.Notes)
	}
	// The note document is owner-only: it names services and an error line.
	info, err := os.Stat(NotesPath(store.Home, testRoot))
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm&0o077 != 0 {
		t.Fatalf("notes permissions = %o, want owner-only", perm)
	}
}

// Re-observing the same world must not fabricate a change.
func TestDeriveNotesIsIdempotentForTheSameWorld(t *testing.T) {
	clock := newFakeClock()
	engine, store := newTestEngine(t, Options{Now: clock.now})
	world := brokenWorld("rev-1")
	engine.Observe(world)
	first, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	clock.advance(time.Second)
	engine.Observe(world)
	second, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Notes) != len(first.Notes) {
		t.Fatalf("notes doubled: %d -> %d", len(first.Notes), len(second.Notes))
	}
	for i := range first.Notes {
		if first.Notes[i].ID != second.Notes[i].ID || first.Notes[i].FirstSeen != second.Notes[i].FirstSeen {
			t.Fatalf("note identity moved: %+v vs %+v", first.Notes[i], second.Notes[i])
		}
	}
	_, changed := DeriveNotes(world, second, clock.now())
	if changed {
		t.Fatal("an unchanged world reported a change")
	}
}

// A healthy world resolves every live note and keeps it as bounded history.
func TestObserveAutoResolvesNotesOnAHealthyWorld(t *testing.T) {
	engine, store := newTestEngine(t, Options{})
	engine.Observe(brokenWorld("rev-1"))
	engine.Observe(healthyWorld("rev-2"))

	doc, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	if doc.WorldHealth != runtimecore.WorldHealthy {
		t.Fatalf("health = %q", doc.WorldHealth)
	}
	if active := ActiveNotes(doc); len(active) != 0 {
		t.Fatalf("active notes after recovery: %+v", active)
	}
	resolved := ResolvedNotes(doc)
	if len(resolved) != 3 {
		t.Fatalf("resolved history = %d, want 3", len(resolved))
	}
	for _, note := range resolved {
		if note.ResolvedAt == "" || note.Status != NoteResolved {
			t.Fatalf("resolved note = %+v", note)
		}
	}
}

func TestResolvedHistoryIsBounded(t *testing.T) {
	clock := newFakeClock()
	engine, store := newTestEngine(t, Options{Now: clock.now})
	for i := 0; i < MaxResolvedNotes+4; i++ {
		world := brokenWorld("rev-" + string(rune('a'+i)))
		world.Diagnosis.RootCauses[0].ID = "cause-" + string(rune('a'+i))
		engine.Observe(world)
		engine.Observe(healthyWorld("healthy-" + string(rune('a'+i))))
		clock.advance(time.Second)
	}
	doc, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(ResolvedNotes(doc)); got != MaxResolvedNotes {
		t.Fatalf("resolved history = %d, want %d", got, MaxResolvedNotes)
	}
}

// Step B: with consent, one world observation becomes one attached triage.
func TestObserveAttachesAsyncTriage(t *testing.T) {
	var calls atomic.Int32
	server := triageServer(t, &calls, func(int32) string {
		return `{"task_relevant":0.9,"needs_human":0.2,"urgency":0.7,"next_option":"inspect_logs","confidence":0.85,"probabilities":{"continue_without_action":0.1,"inspect_logs":0.8,"restart_service":0.1},"summary":"Read the api log before restarting."}`
	})
	defer server.Close()

	engine, store := newTestEngine(t, Options{
		Resolve:    func() model.Resolved { return assistResolved(server.URL) },
		HTTPClient: server.Client(),
		Workers:    1,
	})
	engine.Start(context.Background())
	engine.Observe(brokenWorld("rev-1"))

	waitFor(t, "triage attached", func() bool {
		doc, err := store.Load(testRoot)
		if err != nil {
			return false
		}
		for _, note := range doc.Notes {
			if note.Triage == nil {
				return false
			}
		}
		return len(doc.Notes) > 0
	})
	doc, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, note := range doc.Notes {
		if note.Status != NoteTriaged {
			t.Errorf("note %s status = %q, want triaged", note.ID, note.Status)
		}
		if note.Triage.Model != "test-model" || note.Triage.Mode != string(model.ModeAssist) {
			t.Errorf("note %s triage = %+v", note.ID, note.Triage)
		}
		if note.Triage.Summary == "" || note.Triage.AutoReconcileEligible {
			t.Errorf("note %s triage = %+v", note.ID, note.Triage)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want one triage per world", calls.Load())
	}
	// A second observe of the same world must not call again: every note is
	// already triaged.
	engine.Observe(brokenWorld("rev-1"))
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d after a re-observe", calls.Load())
	}
}

// A slow answer about an old revision must be dropped, not attached.
func TestTriageIsRejectedWhenTheWorldMovedOn(t *testing.T) {
	gate := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		<-gate
		body, _ := json.Marshal(`{"task_relevant":0.9,"needs_human":0.2,"urgency":0.5,"next_option":"inspect_logs","confidence":0.9,"probabilities":{"continue_without_action":0.1,"inspect_logs":0.8,"restart_service":0.1},"summary":"Read the log."}`)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":` + string(body) + `}}]}`))
	}))
	defer server.Close()

	engine, store := newTestEngine(t, Options{
		Resolve:    func() model.Resolved { return assistResolved(server.URL) },
		HTTPClient: server.Client(),
		Workers:    1,
	})
	engine.Start(context.Background())
	engine.Observe(brokenWorld("rev-1"))
	waitFor(t, "first provider call", func() bool { return calls.Load() == 1 })

	// The world moves on while the answer is still in flight.
	engine.Observe(brokenWorld("rev-2"))
	close(gate)
	waitFor(t, "triage job finished", func() bool {
		engine.mu.Lock()
		defer engine.mu.Unlock()
		return len(engine.pending) == 0
	})
	time.Sleep(20 * time.Millisecond)
	doc, err := store.Load(testRoot)
	if err != nil {
		t.Fatal(err)
	}
	if doc.WorldRevision != "rev-2" {
		t.Fatalf("revision = %q, want rev-2", doc.WorldRevision)
	}
	for _, note := range doc.Notes {
		if note.Triage != nil {
			t.Fatalf("a stale answer was attached: %+v", note.Triage)
		}
	}

	// The next observe for the current revision is answered and attached.
	engine.Observe(brokenWorld("rev-2"))
	waitFor(t, "fresh triage attached", func() bool {
		latest, err := store.Load(testRoot)
		return err == nil && latest.WorldRevision == "rev-2" && len(latest.Notes) > 0 && latest.Notes[0].Triage != nil
	})
}

// Three consecutive transport failures open the breaker; while it is open no
// further call is made.
func TestCircuitBreakerOpensAfterThreeTransportFailures(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(w, "upstream unavailable", http.StatusBadGateway)
	}))
	defer server.Close()

	clock := newFakeClock()
	engine, _ := newTestEngine(t, Options{
		Resolve:    func() model.Resolved { return assistResolved(server.URL) },
		HTTPClient: server.Client(),
		Workers:    1,
		Now:        clock.now,
		Cooldown:   time.Minute,
	})
	engine.Start(context.Background())
	world := brokenWorld("rev-1")
	for i := 0; i < 3; i++ {
		engine.Observe(world)
		waitFor(t, "triage attempt finished", func() bool {
			engine.mu.Lock()
			defer engine.mu.Unlock()
			return len(engine.pending) == 0
		})
		clock.advance(time.Second)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("provider calls = %d, want 3", got)
	}
	if !engine.breakerOpen(clock.now()) {
		t.Fatal("breaker did not open after three transport failures")
	}
	engine.Observe(world)
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 3 {
		t.Fatalf("provider calls = %d while the breaker was open", got)
	}
	// After the cooldown the engine tries again.
	clock.advance(2 * time.Minute)
	engine.Observe(world)
	waitFor(t, "retry after cooldown", func() bool { return calls.Load() == 4 })
}

// A bad answer is not a transport failure: it is recorded once, with its fixed
// reason, and does not open the breaker.
func TestBadResponseIsRecordedWithoutOpeningTheBreaker(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"not a decision"}}]}`))
	}))
	defer server.Close()
	engine, store := newTestEngine(t, Options{
		Resolve:    func() model.Resolved { return assistResolved(server.URL) },
		HTTPClient: server.Client(),
		Workers:    1,
	})
	engine.Start(context.Background())
	engine.Observe(brokenWorld("rev-1"))
	waitFor(t, "unavailable recorded", func() bool {
		doc, err := store.Load(testRoot)
		if err != nil || len(doc.Notes) == 0 {
			return false
		}
		for _, note := range doc.Notes {
			if note.Triage == nil || note.Triage.UnavailableReason == "" {
				return false
			}
		}
		return true
	})
	doc, _ := store.Load(testRoot)
	if doc.Notes[0].Triage.UnavailableReason != model.ReasonBadResponse {
		t.Fatalf("reason = %q", doc.Notes[0].Triage.UnavailableReason)
	}
	if engine.breakerOpen(engine.now()) {
		t.Fatal("a bad answer opened the transport breaker")
	}
}

// Reconcile eligibility is a bounded rule, not a model's opinion.
func TestReconcileEligibilityIsBounded(t *testing.T) {
	confident, human := 0.9, 0.1
	world := brokenWorld("rev-1")
	resolved := assistResolved("https://example.invalid")
	resolved.Mode = string(model.ModeReconcile)
	job := triageJob{root: testRoot, world: world, resolved: resolved}
	result := model.WorldTriageResult{NeedsHuman: human, Confidence: &confident, NextOption: "restart_service"}

	stale := Note{Service: "web", Kind: runtimecore.DriftCodeStale, Safety: runtimecore.SafetyLocalRestart}
	if !reconcileEligible(stale, job, result) {
		t.Fatal("a confident local restart was not eligible")
	}
	crashLoop := Note{Service: "worker", Kind: runtimecore.DriftRepeatedCrash, Safety: runtimecore.SafetyLocalRestart}
	if reconcileEligible(crashLoop, job, result) {
		t.Fatal("a crash loop was eligible for reconciliation")
	}
	humanDecision := Note{Service: "api", Kind: runtimecore.DriftPortOccupied, Safety: runtimecore.SafetyHumanDecision}
	if reconcileEligible(humanDecision, job, result) {
		t.Fatal("a human decision was eligible for reconciliation")
	}
	needsHuman := 0.6
	if reconcileEligible(stale, job, model.WorldTriageResult{NeedsHuman: needsHuman, Confidence: &confident}) {
		t.Fatal("a note needing a person was eligible")
	}
	low := 0.4
	if reconcileEligible(stale, job, model.WorldTriageResult{NeedsHuman: human, Confidence: &low}) {
		t.Fatal("an unconfident answer was eligible")
	}
	assist := job
	assist.resolved.Mode = string(model.ModeAssist)
	if reconcileEligible(stale, assist, result) {
		t.Fatal("assist mode marked a note eligible")
	}
}

// ------------------------------------------------------------- mid-turn ---

func TestTakeMidTurnBriefDeliversOncePerRevision(t *testing.T) {
	clock := newFakeClock()
	engine, store := newTestEngine(t, Options{Now: clock.now})
	engine.Observe(brokenWorld("rev-1"))

	brief, ok := store.TakeMidTurnBrief(testRoot)
	if !ok {
		t.Fatal("no brief for a broken world")
	}
	if brief.Revision != "rev-1" || !strings.Contains(brief.Text, "crashed with exit code 1") {
		t.Fatalf("brief = %+v", brief)
	}
	if strings.Contains(brief.Text, "supersecret") || strings.Contains(brief.Text, "/Users/alice") {
		t.Fatalf("brief leaked private text: %q", brief.Text)
	}
	if len([]rune(brief.Text)) > maxBriefRunes {
		t.Fatalf("brief is %d runes", len([]rune(brief.Text)))
	}

	if _, ok := store.TakeMidTurnBrief(testRoot); ok {
		t.Fatal("the same revision was delivered twice")
	}

	clock.advance(time.Second)
	engine.Observe(brokenWorld("rev-2"))
	brief, ok = store.TakeMidTurnBrief(testRoot)
	if !ok || brief.Revision != "rev-2" {
		t.Fatalf("a new revision was not delivered: %+v ok=%v", brief, ok)
	}

	// A healthy world has nothing to say, even though the document changed.
	engine.Observe(healthyWorld("rev-3"))
	if _, ok := store.TakeMidTurnBrief(testRoot); ok {
		t.Fatal("a healthy world produced a brief")
	}
}

func TestTakeMidTurnBriefOnAMissingDocument(t *testing.T) {
	store := NoteStore{Home: t.TempDir()}
	if _, ok := store.TakeMidTurnBrief(testRoot); ok {
		t.Fatal("a worktree with no notes produced a brief")
	}
}

func TestStopLeavesNoGoroutinesAndDropsTheQueue(t *testing.T) {
	var calls atomic.Int32
	server := triageServer(t, &calls, func(int32) string {
		time.Sleep(20 * time.Millisecond)
		return `{"task_relevant":0.9,"needs_human":0.2,"next_option":"inspect_logs","confidence":0.9,"probabilities":{"continue_without_action":0.1,"inspect_logs":0.8,"restart_service":0.1},"summary":"x"}`
	})
	defer server.Close()
	engine, _ := newTestEngine(t, Options{
		Resolve:    func() model.Resolved { return assistResolved(server.URL) },
		HTTPClient: server.Client(),
		Workers:    2,
	})
	engine.Start(context.Background())
	engine.Observe(brokenWorld("rev-1"))
	engine.Stop()
	// Stopping twice is safe, and a stopped engine accepts an observe without
	// enqueuing anything.
	engine.Stop()
	engine.Observe(brokenWorld("rev-2"))
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if engine.cancel != nil || len(engine.pending) != 0 {
		t.Fatalf("engine after Stop: cancel=%v pending=%d", engine.cancel != nil, len(engine.pending))
	}
}

func TestEngineWithoutHomeDoesNothing(t *testing.T) {
	engine := New(Options{})
	engine.Start(context.Background())
	defer engine.Stop()
	engine.Observe(brokenWorld("rev-1"))
	if _, ok := engine.Store().TakeMidTurnBrief(testRoot); ok {
		t.Fatal("an engine without a home wrote notes")
	}
}

// Scoped mid-turn delivery marks only the notes in that scope so another scope
// still sees its own findings, and parallel PostToolUse calls on the same
// revision deliver at most once.
func TestTakeMidTurnBriefForMarksOnlyScopedNotesAndDeduplicatesConcurrently(t *testing.T) {
	clock := newFakeClock()
	engine, store := newTestEngine(t, Options{Now: clock.now})
	engine.Observe(brokenWorld("rev-1"))

	// "api" impacts "web", so scoping to "api" delivers api/web notes while
	// leaving "worker"'s crash loop undelivered.
	briefAPI, ok := store.TakeMidTurnBriefFor(testRoot, []string{"api"})
	if !ok || !strings.Contains(briefAPI.Text, "api") {
		t.Fatalf("scoped api brief = %+v ok=%v", briefAPI, ok)
	}
	if _, dup := store.TakeMidTurnBriefFor(testRoot, []string{"api"}); dup {
		t.Fatal("api scope delivered the same revision twice")
	}
	briefWorker, ok := store.TakeMidTurnBriefFor(testRoot, []string{"worker"})
	if !ok || !strings.Contains(briefWorker.Text, "worker") {
		t.Fatalf("worker note was wrongly marked delivered by api scope: %+v ok=%v", briefWorker, ok)
	}

	// On a new revision, parallel PostToolUse calls race on the same store;
	// exactly one goroutine may deliver the brief.
	clock.advance(time.Second)
	engine.Observe(brokenWorld("rev-2"))
	const callers = 8
	var delivered atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, ok := store.TakeMidTurnBrief(testRoot); ok {
				delivered.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	if got := delivered.Load(); got != 1 {
		t.Fatalf("concurrent mid-turn deliveries = %d, want 1", got)
	}
}
