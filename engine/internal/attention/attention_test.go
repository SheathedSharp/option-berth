package attention

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestClassifyIgnoresNormalStop(t *testing.T) {
	f := Facts{Services: []Service{{Name: "api", LastExit: &Exit{Reason: "stopped"}}}}
	if _, ok := Classify(f); ok {
		t.Fatal("normal stop produced an attention event")
	}
}

func TestClassifyHealthFailure(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	f := Facts{
		ObservedAt: now, WorktreeRoot: "/tmp/worktree",
		Services: []Service{{Name: "api", Running: true, HealthStatus: "fail", HealthCode: 503, HealthReason: "unhealthy"}},
	}
	events := ClassifyAll(f)
	if len(events) != 1 || events[0].Kind != EventHealthFailed || events[0].Service != "api" || events[0].Reason != "unhealthy" {
		t.Fatalf("events = %+v, want one health failure", events)
	}
	a, ok := Derive(f, now, time.Minute)
	if !ok || a.Event.Kind != EventHealthFailed || len(a.Options) != 3 || a.Options[0].ID != "inspect_logs" {
		t.Fatalf("artifact = %+v, ok = %v, want bounded health recovery options", a, ok)
	}
}

func TestClassifyWithoutClockFactIsDeterministic(t *testing.T) {
	declared := 3000
	f := Facts{WorktreeRoot: "/tmp/repo", Services: []Service{{Name: "api", Running: true, DeclaredPort: &declared}}, Listeners: []Listener{{Port: 9000, Cwd: "/tmp/repo"}}}
	first, second := ClassifyAll(f), ClassifyAll(f)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("classification changed without a clock fact: first=%+v second=%+v", first, second)
	}
}

func TestDeriveFailureHasBoundedOptionsAndEvidence(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	f := Facts{StateRevision: "184", WorktreeRoot: "/tmp/demo", Branch: "feature/attention", ObservedAt: now, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed", Code: 1, At: now}}}}
	a, ok := Derive(f, now, time.Minute)
	if !ok || a.Event.Kind != EventServiceFailed {
		t.Fatalf("artifact = %+v, ok = %v", a, ok)
	}
	if len(a.Options) != 4 || a.Options[0].ID != "inspect_logs" || a.Options[1].ID != "inspect_manifest" {
		t.Fatalf("options = %+v", a.Options)
	}
	if len(a.Evidence) != 2 || a.Evidence[0].Ref == "" {
		t.Fatalf("evidence = %+v", a.Evidence)
	}
	if a.EventID == "" || a.FreshUntil == "" {
		t.Fatalf("artifact identity/freshness missing: %+v", a)
	}
	if a.Branch != "feature/attention" {
		t.Fatalf("branch = %q, want worktree branch", a.Branch)
	}
	if a.FirstSeen != a.GeneratedAt || a.LastSeen != a.GeneratedAt || a.RecoveredAt != "" {
		t.Fatalf("lifecycle = first %q last %q recovered %q, want active timestamps", a.FirstSeen, a.LastSeen, a.RecoveredAt)
	}
	if len(a.Events) != 1 || a.Events[0].Kind != EventServiceFailed {
		t.Fatalf("events = %+v, want one service failure", a.Events)
	}
}

func TestClassifyPortsAndManifestConflicts(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	declared, actual := 3000, 3001
	root := "/tmp/worktree"
	f := Facts{
		ObservedAt: now, WorktreeRoot: root,
		Services: []Service{{Name: "api", Running: true, DeclaredPort: &declared, ActualPort: &actual}},
		Listeners: []Listener{
			{Port: 9000, Cwd: root},
			{Port: 9001, Cwd: "/tmp/other-worktree"},
		},
	}
	events := ClassifyAll(f)
	if len(events) != 2 {
		t.Fatalf("events = %+v, want manifest conflict and one local undeclared listener", events)
	}
	if events[0].Kind != EventManifestConflict || events[0].Reason != "declared_port_mismatch" ||
		events[0].DeclaredPort == nil || *events[0].DeclaredPort != declared ||
		events[0].ActualPort == nil || *events[0].ActualPort != actual {
		t.Fatalf("manifest event = %+v", events[0])
	}
	if events[1].Kind != EventUndeclared || events[1].Port != 9000 {
		t.Fatalf("undeclared event = %+v", events[1])
	}
	artifact, ok := Derive(f, now, time.Minute)
	if !ok || len(artifact.Options) != 4 || artifact.Options[0].ID != "inspect_manifest" {
		t.Fatalf("artifact options = %+v, ok=%v", artifact.Options, ok)
	}
	starting := Facts{ObservedAt: now, WorktreeRoot: root, Services: []Service{{Name: "api", Running: true, StartedAt: now.Format(time.RFC3339), DeclaredPort: &declared}}}
	if events := ClassifyAll(starting); len(events) != 0 {
		t.Fatalf("startup without observed listener = %+v, want no inferred conflict", events)
	}
}

func TestFailureEventCarriesRunEvidence(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	f := Facts{
		ObservedAt: now, WorktreeRoot: "/tmp/worktree",
		Services: []Service{{Name: "api", LogPath: "/private/tmp/oberth/api.log", LastExit: &Exit{Reason: "crashed", Code: 17, RunID: "run-7", At: now}}},
	}
	a, ok := Derive(f, now, time.Minute)
	if !ok || a.Event.RunID != "run-7" || a.Event.ExitCode == nil || *a.Event.ExitCode != 17 {
		t.Fatalf("event = %+v, want run and exit evidence", a.Event)
	}
	if len(a.Evidence) != 2 || a.Evidence[1].Ref != "logs:api:run-7" || a.Evidence[1].LogPath != f.Services[0].LogPath {
		t.Fatalf("evidence = %+v, want run-bound local log evidence", a.Evidence)
	}
}

func TestUndeclaredListenerNeedsScopedPath(t *testing.T) {
	f := Facts{WorktreeRoot: "/tmp/worktree", Listeners: []Listener{{Port: 9000}}}
	if events := ClassifyAll(f); len(events) != 0 {
		t.Fatalf("pathless listener = %+v, want no machine-wide inference", events)
	}
	f.Listeners[0].Cwd = "/tmp/worktree/subdir"
	events := ClassifyAll(f)
	if len(events) != 1 || events[0].Kind != EventUndeclared {
		t.Fatalf("scoped listener = %+v, want undeclared event", events)
	}
}

func TestClassifyRepeatedCrashUsesBoundedHistoryWindow(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	current := &Exit{Reason: "crashed", RunID: "run-3", At: now.Add(-time.Minute)}
	f := Facts{
		ObservedAt: now, WorktreeRoot: "/tmp/worktree", CrashWindow: 10 * time.Minute, CrashThreshold: 3,
		Services: []Service{{Name: "api", LastExit: current}},
		History: []ExitHistory{
			{Service: "api", Reason: "crashed", RunID: "run-1", At: now.Add(-9 * time.Minute)},
			{Service: "api", Reason: "crashed", RunID: "run-2", At: now.Add(-8 * time.Minute)},
			{Service: "api", Reason: "crashed", RunID: "old", At: now.Add(-11 * time.Minute)},
		},
	}
	events := ClassifyAll(f)
	if len(events) != 1 || events[0].Kind != EventRepeatedCrash || events[0].Count != 3 {
		t.Fatalf("events = %+v, want repeated crash count 3", events)
	}
	f.History = f.History[:1]
	events = ClassifyAll(f)
	if len(events) != 1 || events[0].Kind != EventServiceFailed {
		t.Fatalf("events after history shrink = %+v, want one ordinary failure", events)
	}
}

func TestRecoverRetainsFirstSeenAndMarksRecovery(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	f := Facts{StateRevision: "1", WorktreeRoot: "/tmp/worktree", ObservedAt: now, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "start_failed", At: now}}}}
	active, ok := Derive(f, now, time.Minute)
	if !ok {
		t.Fatal("expected active artifact")
	}
	recovered := Recover(active, now.Add(time.Minute), time.Minute)
	if recovered.FirstSeen != active.FirstSeen || recovered.RecoveredAt == "" || recovered.LastSeen != recovered.RecoveredAt {
		t.Fatalf("recovered lifecycle = first %q last %q recovered %q", recovered.FirstSeen, recovered.LastSeen, recovered.RecoveredAt)
	}
	if recovered.EventID != active.EventID || recovered.Assessment != nil {
		t.Fatalf("recovered artifact identity/assessment = %q/%+v", recovered.EventID, recovered.Assessment)
	}
}

func TestDeriveRetainsAllAnomaliesAndStableIdentity(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	f := Facts{WorktreeRoot: "/tmp/demo", ObservedAt: now, Services: []Service{
		{Name: "worker", LastExit: &Exit{Reason: "crashed", Code: 1, RunID: "w1", At: now}},
		{Name: "api", LastExit: &Exit{Reason: "port_occupied", Code: 1, RunID: "a1", At: now}},
	}, Machine: []Machine{{Name: "db", Port: 5432, Listening: false}}}
	a, ok := Derive(f, now, time.Minute)
	if !ok || len(a.Events) != 3 {
		t.Fatalf("artifact = %+v, ok = %v; want three anomalies", a, ok)
	}
	if a.Event.Service != "api" || a.Events[2].Machine != "db" {
		t.Fatalf("events = %+v, want stable name order", a.Events)
	}
	if len(a.Evidence) != 5 {
		t.Fatalf("evidence = %+v, want evidence for all anomalies", a.Evidence)
	}
	f.Services[0], f.Services[1] = f.Services[1], f.Services[0]
	f.ObservedAt = now.Add(30 * time.Second)
	b, ok := Derive(f, f.ObservedAt, time.Minute)
	if !ok || a.EventID != b.EventID || a.StateRevision != b.StateRevision {
		t.Fatalf("identity/revision changed across reorder or observation time: %q/%q, %q/%q", a.EventID, a.StateRevision, b.EventID, b.StateRevision)
	}
}

func TestWriteReadIsAtomicAndPrivate(t *testing.T) {
	home := t.TempDir()
	f := Facts{StateRevision: "1", WorktreeRoot: filepath.Join(home, "repo"), Services: []Service{{Name: "api", LastExit: &Exit{Reason: "start_failed"}}}}
	a, ok := Derive(f, time.Now(), 0)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID != a.EventID || got.Schema != Schema {
		t.Fatalf("read artifact = %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
}

func TestWritePreservesAssessmentOnlyForSameEventRevision(t *testing.T) {
	home := t.TempDir()
	now := time.Now().UTC()
	f := Facts{StateRevision: "1", WorktreeRoot: filepath.Join(home, "repo"), Services: []Service{{Name: "api", LastExit: &Exit{Reason: "start_failed"}}}}
	a, ok := Derive(f, now, time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	needsHuman := 0.8
	if err := UpdateAssessment(path, Assessment{NeedsHuman: &needsHuman, NextOption: "restart_service"}); err != nil {
		t.Fatal(err)
	}
	a2, _ := Derive(f, now.Add(time.Second), time.Minute)
	if _, err := Write(home, a2); err != nil {
		t.Fatal(err)
	}
	got, err := Read(path)
	if err != nil || got.Assessment == nil || got.Assessment.NextOption != "restart_service" {
		t.Fatalf("assessment after refresh = %+v, err=%v", got.Assessment, err)
	}
	updated := 0.2
	a2.Assessment = &Assessment{NeedsHuman: &updated, NextOption: "inspect_logs"}
	if _, err := Write(home, a2); err != nil {
		t.Fatal(err)
	}
	got, err = Read(path)
	if err != nil || got.Assessment == nil || got.Assessment.NeedsHuman == nil || *got.Assessment.NeedsHuman != updated {
		t.Fatalf("explicit assessment update was lost: %+v, err=%v", got.Assessment, err)
	}
	f.StateRevision = "2"
	a3, _ := Derive(f, now.Add(2*time.Second), time.Minute)
	if _, err := Write(home, a3); err != nil {
		t.Fatal(err)
	}
	got, err = Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.Assessment != nil {
		t.Fatalf("stale assessment survived revision change: %+v", got.Assessment)
	}
}

func TestReadFreshRejectsExpiredArtifact(t *testing.T) {
	now := time.Date(2026, 9, 27, 1, 2, 3, 0, time.UTC)
	home := t.TempDir()
	f := Facts{StateRevision: "1", WorktreeRoot: filepath.Join(home, "repo"), Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed"}}}}
	a, ok := Derive(f, now, time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFresh(path, now.Add(59*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFresh(path, now.Add(time.Minute)); err == nil {
		t.Fatal("deadline was accepted as fresh")
	}
	bad := a
	bad.FreshUntil = "not-a-time"
	if _, err := Write(home, bad); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadFresh(path, now); err == nil {
		t.Fatal("malformed deadline was accepted")
	}
}

func TestConcurrentWritesRemainReadable(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repo")
	f := Facts{StateRevision: "1", WorktreeRoot: root, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed"}}}}
	a, ok := Derive(f, time.Now().UTC(), time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := Write(home, a); err != nil {
				t.Errorf("Write: %v", err)
			}
		}()
	}
	wg.Wait()
	if _, err := Read(Path(home, root)); err != nil {
		t.Fatal(err)
	}
}

func TestCompareAndSwapWaitsForOtherProcessWriter(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repo")
	f := Facts{StateRevision: "1", WorktreeRoot: root, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed", RunID: "run-1"}}}}
	a, ok := Derive(f, time.Now().UTC(), time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestAttentionCASHelper", "--")
	startedPath := filepath.Join(home, "helper-started")
	cmd.Env = append(os.Environ(), "ATTENTION_CAS_HELPER=1", "ATTENTION_CAS_PATH="+path,
		"ATTENTION_CAS_STARTED="+startedPath, "ATTENTION_CAS_EVENT="+a.EventID, "ATTENTION_CAS_REVISION="+a.StateRevision)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(startedPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("CAS helper did not acquire lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	started := time.Now()
	if _, err := Write(home, a); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 150*time.Millisecond {
		t.Fatalf("Write bypassed cross-process lock: waited %s", elapsed)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("CAS helper: %v", err)
	}
}

func TestCompareAndSwapRejectsStaleIdentity(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repo")
	a, ok := Derive(Facts{StateRevision: "1", WorktreeRoot: root, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed"}}}}, time.Now().UTC(), time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	err = CompareAndSwap(path, "old-event", a.StateRevision, func(*Artifact) error { return nil })
	if !errors.Is(err, ErrStale) {
		t.Fatalf("CAS error = %v, want ErrStale", err)
	}
}

func TestCompareAndSwapRejectsIdentityMutation(t *testing.T) {
	home := t.TempDir()
	root := filepath.Join(home, "repo")
	a, ok := Derive(Facts{StateRevision: "1", WorktreeRoot: root, Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed"}}}}, time.Now().UTC(), time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	path, err := Write(home, a)
	if err != nil {
		t.Fatal(err)
	}
	err = CompareAndSwap(path, a.EventID, a.StateRevision, func(updated *Artifact) error {
		updated.EventID = "different"
		return nil
	})
	if !errors.Is(err, ErrStale) {
		t.Fatalf("CAS error = %v, want ErrStale", err)
	}
	got, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.EventID != a.EventID {
		t.Fatalf("artifact event id changed after rejected callback: %q", got.EventID)
	}
}

func TestAttentionCASHelper(t *testing.T) {
	if os.Getenv("ATTENTION_CAS_HELPER") != "1" {
		return
	}
	err := CompareAndSwap(os.Getenv("ATTENTION_CAS_PATH"), os.Getenv("ATTENTION_CAS_EVENT"), os.Getenv("ATTENTION_CAS_REVISION"), func(*Artifact) error {
		if marker := os.Getenv("ATTENTION_CAS_STARTED"); marker != "" {
			if err := os.WriteFile(marker, []byte("locked"), 0o600); err != nil {
				return err
			}
		}
		time.Sleep(250 * time.Millisecond)
		return nil
	})
	if err != nil && !errors.Is(err, ErrStale) {
		t.Fatal(err)
	}
}

func TestPathIsStableForWorktree(t *testing.T) {
	a := Path("/tmp/berth", "/tmp/repo")
	b := Path("/tmp/berth", "/tmp/repo")
	if a != b || filepath.Dir(a) != "/tmp/berth/attention" {
		t.Fatalf("paths = %q %q", a, b)
	}
}

func TestAttachAssessmentOnlyAcceptsFixedOptions(t *testing.T) {
	f := Facts{StateRevision: "1", WorktreeRoot: "/tmp/repo", Services: []Service{{Name: "api", LastExit: &Exit{Reason: "crashed"}}}}
	a, ok := Derive(f, time.Now(), time.Minute)
	if !ok {
		t.Fatal("expected artifact")
	}
	needsHuman := 0.9
	if err := AttachAssessment(&a, Assessment{
		NeedsHuman: &needsHuman,
		NextOption: "restart_service",
		Probabilities: map[string]float64{
			"inspect_logs": 0.2, "restart_service": 0.7, "continue_without_action": 0.1,
		},
		Model: "jev-1.13.0",
	}); err != nil {
		t.Fatal(err)
	}
	bad := Assessment{NextOption: "run_arbitrary_shell"}
	if err := AttachAssessment(&a, bad); err == nil {
		t.Fatal("unknown option was accepted")
	}
}
