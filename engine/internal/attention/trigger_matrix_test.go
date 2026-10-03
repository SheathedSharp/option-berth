package attention

import (
	"encoding/json"
	"slices"
	"testing"
	"time"
)

// This labeled matrix measures deterministic event publication. It is not a
// Jev accuracy or probability-calibration dataset: no model is consulted and
// the labels express the project's runtime boundaries rather than opinions
// about whether an agent should interrupt a person.
func TestAttentionTriggerPrecisionMatrix(t *testing.T) {
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	root := "/tmp/attention-matrix/main"
	declared, actual := 18090, 18091
	running := Service{Name: "api", Running: true, RunID: "live-1", DeclaredPort: &declared, ActualPort: &declared}
	failure := &Exit{Reason: "crashed", Code: 1, RunID: "crash-3", At: now.Add(-time.Minute)}
	facts := func(services ...Service) Facts {
		return Facts{ObservedAt: now, WorktreeRoot: root, Services: services}
	}
	type matrixRow struct {
		name  string
		facts Facts
		want  []EventKind
	}
	rows := []matrixRow{
		{name: "declared_not_started", facts: facts(Service{Name: "api"})},
		{name: "normal_running", facts: facts(running)},
		{name: "normal_exit", facts: facts(Service{Name: "worker", LastExit: &Exit{Reason: "exited", Code: 0, RunID: "normal-exit", At: now}})},
		{name: "normal_stop", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "stopped", Code: 137, RunID: "normal-stop", At: now}})},
		{name: "log_poll_changed_observation_only", facts: facts(running)},
		{name: "single_crash", facts: facts(Service{Name: "api", LastExit: failure}), want: []EventKind{EventServiceFailed}},
		{name: "start_failed", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "start_failed", Code: 1, RunID: "missing", At: now}}), want: []EventKind{EventServiceFailed}},
		{name: "dependency_timeout", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "dependency_timeout", Code: 1, RunID: "dep-timeout", At: now}}), want: []EventKind{EventServiceFailed}},
		{name: "dependency_not_ready", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "dependency_not_ready", Code: 1, RunID: "dep-missing", At: now}}), want: []EventKind{EventServiceFailed}},
		{name: "port_occupied", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "port_occupied", Code: 1, RunID: "occupied", At: now}}), want: []EventKind{EventPortOccupied}},
		{name: "ready_timeout_flag", facts: facts(Service{Name: "api", Running: true, ReadyTimedOut: true}), want: []EventKind{EventReadyTimeout}},
		{name: "ready_timeout_exit", facts: facts(Service{Name: "api", LastExit: &Exit{Reason: "ready_timeout", Code: 1, RunID: "timeout", At: now}}), want: []EventKind{EventReadyTimeout}},
		{name: "manifest_runtime_port_mismatch", facts: facts(Service{Name: "api", Running: true, DeclaredPort: &declared, ActualPort: &actual}), want: []EventKind{EventManifestConflict}},
		{name: "manifest_runtime_missing_listener", facts: facts(Service{Name: "api", Running: true, DeclaredPort: &declared}), want: []EventKind{EventManifestConflict}},
		{name: "declared_listener", facts: Facts{ObservedAt: now, WorktreeRoot: root, Services: []Service{running}, Listeners: []Listener{{Port: declared, Cwd: root}}}},
		{name: "undeclared_worktree_listener", facts: Facts{ObservedAt: now, WorktreeRoot: root, Listeners: []Listener{{Port: 19000, Cwd: root + "/server"}}}, want: []EventKind{EventUndeclared}},
		{name: "undeclared_foreign_worktree_listener", facts: Facts{ObservedAt: now, WorktreeRoot: root, Listeners: []Listener{{Port: 19000, ProjectRoot: "/tmp/attention-matrix/linked"}}}},
		{name: "undeclared_sibling_prefix_listener", facts: Facts{ObservedAt: now, WorktreeRoot: root, Listeners: []Listener{{Port: 19000, Cwd: root + "-other"}}}},
		{name: "unattributed_listener", facts: Facts{ObservedAt: now, WorktreeRoot: root, Listeners: []Listener{{Port: 19000}}}},
		{name: "machine_available", facts: Facts{ObservedAt: now, WorktreeRoot: root, Machine: []Machine{{Name: "db", Port: 5432, Listening: true}}}},
		{name: "machine_unavailable", facts: Facts{ObservedAt: now, WorktreeRoot: root, Machine: []Machine{{Name: "db", Port: 5432}}}, want: []EventKind{EventMachineOffline}},
	}
	repeated := facts(Service{Name: "api", LastExit: failure})
	repeated.History = []ExitHistory{
		{Service: "api", Reason: "crashed", RunID: "crash-1", At: now.Add(-9 * time.Minute)},
		{Service: "api", Reason: "crashed", RunID: "crash-2", At: now.Add(-8 * time.Minute)},
		{Service: "api", Reason: "crashed", RunID: "crash-3", At: failure.At},
	}
	rows = append(rows, matrixRow{name: "three_crashes_inside_window", facts: repeated, want: []EventKind{EventRepeatedCrash}})
	windowExpired := repeated
	windowExpired.History = append([]ExitHistory(nil), repeated.History...)
	windowExpired.History[0].At = now.Add(-11 * time.Minute)
	rows = append(rows, matrixRow{name: "third_crash_outside_window", facts: windowExpired, want: []EventKind{EventServiceFailed}})
	duplicateHistory := facts(Service{Name: "api", LastExit: failure})
	duplicateHistory.History = []ExitHistory{
		{Service: "api", Reason: "crashed", RunID: "crash-3", At: failure.At},
		{Service: "api", Reason: "crashed", RunID: "crash-3", At: failure.At},
	}
	rows = append(rows, matrixRow{name: "duplicate_history_is_one_crash", facts: duplicateHistory, want: []EventKind{EventServiceFailed}})
	foreignHistory := facts(Service{Name: "api", LastExit: failure})
	foreignHistory.History = []ExitHistory{
		{Service: "worker", Reason: "crashed", RunID: "w-1", At: now.Add(-time.Minute)},
		{Service: "worker", Reason: "crashed", RunID: "w-2", At: now.Add(-2 * time.Minute)},
	}
	rows = append(rows, matrixRow{name: "other_service_crashes_do_not_escalate", facts: foreignHistory, want: []EventKind{EventServiceFailed}})

	metrics := struct {
		Rows                   int `json:"rows"`
		TruePositive           int `json:"true_positive"`
		TrueNegative           int `json:"true_negative"`
		FalsePositive          int `json:"false_positive"`
		FalseNegative          int `json:"false_negative"`
		KindMismatches         int `json:"kind_mismatches"`
		SameEventReads         int `json:"same_event_reads"`
		DedupedReads           int `json:"deduped_reads"`
		DuplicateEventFailures int `json:"duplicate_event_failures"`
		NewRunChecks           int `json:"new_run_checks"`
		NewRunIdentityFailures int `json:"new_run_identity_failures"`
	}{Rows: len(rows)}
	for _, row := range rows {
		artifact, gotTrigger := Derive(row.facts, now, time.Minute)
		wantTrigger := len(row.want) != 0
		switch {
		case gotTrigger && wantTrigger:
			metrics.TruePositive++
		case !gotTrigger && !wantTrigger:
			metrics.TrueNegative++
		case gotTrigger && !wantTrigger:
			metrics.FalsePositive++
		case !gotTrigger && wantTrigger:
			metrics.FalseNegative++
		}
		gotKinds := make([]EventKind, 0, len(artifact.Events))
		for _, event := range artifact.Events {
			gotKinds = append(gotKinds, event.Kind)
		}
		if !slices.Equal(gotKinds, row.want) {
			metrics.KindMismatches++
			t.Errorf("%s: events=%v, want=%v", row.name, gotKinds, row.want)
		}
		if !wantTrigger || !gotTrigger {
			continue
		}
		for poll := 1; poll <= 5; poll++ {
			metrics.SameEventReads++
			repeatFacts := row.facts
			repeatFacts.ObservedAt = now.Add(time.Duration(poll) * time.Second)
			repeat, exists := Derive(repeatFacts, repeatFacts.ObservedAt, time.Minute)
			if exists && repeat.EventID == artifact.EventID && repeat.StateRevision == artifact.StateRevision {
				metrics.DedupedReads++
			} else {
				metrics.DuplicateEventFailures++
				t.Errorf("%s: unchanged poll %d created a new identity or lost the event", row.name, poll)
			}
		}
	}
	oldFacts := facts(Service{Name: "api", LastExit: failure})
	oldArtifact, _ := Derive(oldFacts, now, time.Minute)
	newExit := *failure
	newExit.RunID, newExit.At = "crash-4", now
	newFacts := facts(Service{Name: "api", LastExit: &newExit})
	newArtifact, exists := Derive(newFacts, now, time.Minute)
	metrics.NewRunChecks++
	if !exists || oldArtifact.EventID == newArtifact.EventID || oldArtifact.StateRevision == newArtifact.StateRevision {
		metrics.NewRunIdentityFailures++
		t.Error("a new failed run was deduplicated as the previous run")
	}
	encoded, _ := json.Marshal(metrics)
	t.Logf("deterministic_trigger_metrics=%s", encoded)
	if metrics.FalsePositive != 0 || metrics.FalseNegative != 0 || metrics.KindMismatches != 0 || metrics.DuplicateEventFailures != 0 || metrics.NewRunIdentityFailures != 0 {
		t.Fatalf("trigger matrix failed: %s", encoded)
	}
}
