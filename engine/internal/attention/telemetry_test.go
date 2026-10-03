package attention

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestTelemetryAppendIsPrivateAndPathFree(t *testing.T) {
	home := t.TempDir()
	needsHuman := 0.8
	record := TelemetryRecord{
		Kind: "assessment", Source: "runner", Project: ProjectLabel("/Users/alice/private/example-worker"),
		WorktreeID: WorktreeID("/Users/alice/private/example-worker"), EventID: "event-1", StateRevision: "rev-1",
		EventKind: string(EventServiceFailed), Status: "human_required", Reason: "explicit_choice_required",
		Assessment: &Assessment{NeedsHuman: &needsHuman, NextOption: "restart_service", Probabilities: map[string]float64{"restart_service": 1}},
	}
	path, err := AppendTelemetry(home, record)
	if err != nil {
		t.Fatal(err)
	}
	if path != TelemetryPath(home) {
		t.Fatalf("path = %q, want %q", path, TelemetryPath(home))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("quality directory mode = %o, want 700", dirInfo.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "/Users/alice/private") || strings.Contains(string(data), "task") {
		t.Fatalf("telemetry contains an absolute path or task text: %s", data)
	}
	got, err := ReadTelemetry(home)
	if err != nil || len(got) != 1 || got[0].EventID != "event-1" || got[0].Assessment == nil {
		t.Fatalf("records = %+v, err=%v", got, err)
	}
}

func TestTelemetryRejectsFreeFormAssessmentFields(t *testing.T) {
	value := .5
	for _, assessment := range []*Assessment{
		{NextOption: "run_shell"},
		{Model: "/Users/alice/private/model", NeedsHuman: &value},
	} {
		if _, err := AppendTelemetry(t.TempDir(), TelemetryRecord{Kind: "assessment", Source: "test", EventID: "e", StateRevision: "r", Assessment: assessment}); err == nil {
			t.Fatalf("unsafe assessment was accepted: %+v", assessment)
		}
	}
}

func TestNewFeedbackRecordRequiresUsefulFixedValues(t *testing.T) {
	if _, err := NewFeedbackRecord(FeedbackInput{EventID: "e", StateRevision: "r"}, time.Now()); err == nil {
		t.Fatal("empty feedback was accepted")
	}
	falseValue := false
	record, err := NewFeedbackRecord(FeedbackInput{
		EventID: "e", StateRevision: "r", HumanNeeded: &falseValue,
		UserDecision: "continued", ActionStatus: "not_run", ChosenOption: "continue_without_action",
	}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if record.Kind != "feedback" || record.Feedback == nil || record.Feedback.HumanNeeded == nil || *record.Feedback.HumanNeeded {
		t.Fatalf("record = %+v", record)
	}
	if _, err := NewFeedbackRecord(FeedbackInput{EventID: "e", StateRevision: "r", ChosenOption: "run_shell"}, time.Now()); err == nil {
		t.Fatal("unknown option feedback was accepted")
	}
}

func TestBuildQualityReportSeparatesLabelsFromUnlabelledObservations(t *testing.T) {
	base := time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC)
	truth := true
	falseTruth := false
	assessment := func(event, status string, needs bool, option string, offset int) TelemetryRecord {
		return TelemetryRecord{
			Schema: TelemetrySchema, RecordID: event + "-assessment", RecordedAt: base.Add(time.Duration(offset) * time.Minute).Format(time.RFC3339Nano),
			Kind: "assessment", Source: "runner", Project: "example-worker", WorktreeID: "worktree-1", EventID: event,
			StateRevision: "rev-" + event, ContextID: "ctx-" + event, EventKind: string(EventServiceFailed), Status: status,
			FirstSeen: base.Format(time.RFC3339Nano), Decision: &TelemetryDecision{NeedsHuman: needs, Reason: "test"},
			Assessment: &Assessment{NextOption: option, Probabilities: map[string]float64{option: 1}},
		}
	}
	feedback := func(event string, human *bool, chosen, action string, offset int) TelemetryRecord {
		return TelemetryRecord{
			Schema: TelemetrySchema, RecordID: event + "-feedback", RecordedAt: base.Add(time.Duration(offset) * time.Minute).Format(time.RFC3339Nano),
			Kind: "feedback", Source: "agent", Project: "example-worker", WorktreeID: "worktree-1", EventID: event,
			StateRevision: "rev-" + event, ContextID: "ctx-" + event,
			Feedback: &TelemetryFeedback{HumanNeeded: human, ChosenOption: chosen, ActionStatus: action},
		}
	}
	records := []TelemetryRecord{
		assessment("tp", "human_required", true, "restart_service", 1), feedback("tp", &truth, "restart_service", "succeeded", 2),
		assessment("tn", "continue", false, "inspect_logs", 3), feedback("tn", &falseTruth, "inspect_logs", "not_run", 4),
		assessment("fp", "human_required", true, "inspect_logs", 5), feedback("fp", &falseTruth, "inspect_logs", "not_run", 6),
		assessment("fn", "continue", false, "inspect_logs", 7), feedback("fn", &truth, "inspect_logs", "failed", 8),
		assessment("unlabelled", "human_required", true, "inspect_logs", 9),
	}
	report := BuildQualityReport(records, time.Time{}, "example-worker", base.Add(time.Hour))
	if report.UniqueCases != 5 || report.LabeledCases != 4 {
		t.Fatalf("case counts = %+v", report)
	}
	if report.UnlabeledCases != 1 || report.LabelCoverage == nil || *report.LabelCoverage != .8 {
		t.Fatalf("label coverage = %+v/%v", report.UnlabeledCases, report.LabelCoverage)
	}
	want := ClassificationMetric{TruePositive: 1, TrueNegative: 1, FalsePositive: 1, FalseNegative: 1}
	report.HumanDecision.Precision = nil
	report.HumanDecision.Recall = nil
	report.HumanDecision.F1 = nil
	if !reflect.DeepEqual(report.HumanDecision, want) {
		t.Fatalf("confusion = %+v, want %+v", report.HumanDecision, want)
	}
	if report.Recommendation.Samples != 4 || report.Recommendation.Matches != 4 || report.Recommendation.Rate == nil || *report.Recommendation.Rate != 1 {
		t.Fatalf("recommendation = %+v", report.Recommendation)
	}
	if report.Actions.Attempts != 2 || report.Actions.Succeeded != 1 || report.Actions.Failed != 1 || report.Actions.SuccessRate == nil || *report.Actions.SuccessRate != .5 {
		t.Fatalf("actions = %+v", report.Actions)
	}
	if report.Availability.Cases != 5 || report.Availability.Assessed != 5 || report.Availability.Missing != 0 || report.Availability.Rate == nil || *report.Availability.Rate != 1 {
		t.Fatalf("availability = %+v", report.Availability)
	}
	if report.DecisionReasons["test"] != 5 || report.RecommendedOptions["inspect_logs"] != 4 || report.RecommendedOptions["restart_service"] != 1 {
		t.Fatalf("diagnostic counts = reasons=%v options=%v", report.DecisionReasons, report.RecommendedOptions)
	}
	if report.HumanDecision.Precision == nil || *report.HumanDecision.Precision != .5 {
		// The local copy above deliberately cleared the pointers for the exact
		// confusion comparison; rebuild the report to check the derived rates.
		report = BuildQualityReport(records, time.Time{}, "example-worker", base.Add(time.Hour))
		if report.HumanDecision.Precision == nil || *report.HumanDecision.Precision != .5 || report.HumanDecision.Recall == nil || *report.HumanDecision.Recall != .5 || report.HumanDecision.F1 == nil || *report.HumanDecision.F1 != .5 {
			t.Fatalf("classification rates = %+v", report.HumanDecision)
		}
	}
}

func TestReadTelemetrySkipsMalformedLines(t *testing.T) {
	home := t.TempDir()
	path := TelemetryPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendTelemetry(home, TelemetryRecord{Kind: "lifecycle", Source: "test", Status: "subscribed"}); err != nil {
		t.Fatal(err)
	}
	records, err := ReadTelemetry(home)
	if err != nil || len(records) != 1 || records[0].Status != "subscribed" {
		t.Fatalf("records = %+v, err=%v", records, err)
	}
}

func TestTelemetryRotatesAtBoundedSize(t *testing.T) {
	home := t.TempDir()
	path := TelemetryPath(home)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, bytes.Repeat([]byte{'x'}, TelemetryMaxBytes), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := AppendTelemetry(home, TelemetryRecord{Kind: "lifecycle", Source: "test", Status: "subscribed"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(telemetryBackupPath(home)); err != nil {
		t.Fatalf("backup ledger missing: %v", err)
	}
	records, err := ReadTelemetry(home)
	if err != nil || len(records) != 1 || records[0].Status != "subscribed" {
		t.Fatalf("rotated records = %+v, err=%v", records, err)
	}
}

func TestBuildQualityReportCalibratesTypedProbabilities(t *testing.T) {
	now := time.Date(2026, 9, 27, 2, 0, 0, 0, time.UTC)
	needs := .25
	relevant := .75
	truthNeeds := false
	truthRelevant := true
	records := []TelemetryRecord{
		{
			Schema: TelemetrySchema, RecordID: "assessment", RecordedAt: now.Format(time.RFC3339Nano),
			Kind: "assessment", Source: "runner", Project: "example-worker", WorktreeID: "w", EventID: "e", StateRevision: "r", ContextID: "c",
			Status: "continue", Decision: &TelemetryDecision{NeedsHuman: false},
			Assessment: &Assessment{NeedsHuman: &needs, TaskRelevant: &relevant, NextOption: "inspect_logs", Probabilities: map[string]float64{"inspect_logs": 1}},
		},
		{
			Schema: TelemetrySchema, RecordID: "feedback", RecordedAt: now.Add(time.Second).Format(time.RFC3339Nano),
			Kind: "feedback", Source: "agent", Project: "example-worker", WorktreeID: "w", EventID: "e", StateRevision: "r", ContextID: "c",
			Feedback: &TelemetryFeedback{HumanNeeded: &truthNeeds, TaskRelevant: &truthRelevant},
		},
	}
	report := BuildQualityReport(records, time.Time{}, "example-worker", now.Add(time.Minute))
	if report.ModelNeedsHuman.Samples != 1 || report.ModelNeedsHuman.BrierScore == nil || *report.ModelNeedsHuman.BrierScore != .0625 {
		t.Fatalf("needs_human calibration = %+v", report.ModelNeedsHuman)
	}
	if report.ModelTaskRelevant.Samples != 1 || report.ModelTaskRelevant.BrierScore == nil || *report.ModelTaskRelevant.BrierScore != .0625 {
		t.Fatalf("task_relevant calibration = %+v", report.ModelTaskRelevant)
	}
}

func TestBuildQualityReportJoinsFeedbackIndependentOfLedgerOrder(t *testing.T) {
	now := time.Date(2026, 9, 27, 3, 0, 0, 0, time.UTC)
	truth := true
	// Deliberately put feedback first and omit context_id. This mirrors a
	// rotated/partially flushed ledger where append order is not a safe join
	// strategy.
	records := []TelemetryRecord{
		{
			Schema: TelemetrySchema, RecordID: "feedback", RecordedAt: now.Add(time.Minute).Format(time.RFC3339Nano),
			Kind: "feedback", Source: "agent", Project: "example-worker", WorktreeID: "w", EventID: "e", StateRevision: "r",
			Feedback: &TelemetryFeedback{HumanNeeded: &truth},
		},
		{
			Schema: TelemetrySchema, RecordID: "assessment", RecordedAt: now.Format(time.RFC3339Nano),
			Kind: "assessment", Source: "runner", Project: "example-worker", WorktreeID: "w", EventID: "e", StateRevision: "r", ContextID: "ctx",
			Status: "human_required", Decision: &TelemetryDecision{NeedsHuman: true},
			Assessment: &Assessment{NextOption: "restart_service", Probabilities: map[string]float64{"restart_service": 1}},
		},
	}
	report := BuildQualityReport(records, time.Time{}, "example-worker", now.Add(2*time.Minute))
	if report.UniqueCases != 1 || report.LabeledCases != 1 || report.HumanDecision.TruePositive != 1 {
		t.Fatalf("out-of-order feedback was not joined: %+v", report)
	}
}

func TestBuildQualityReportTracksResolutionLabels(t *testing.T) {
	now := time.Date(2026, 9, 27, 4, 0, 0, 0, time.UTC)
	resolved, unresolved := true, false
	assessment := func(event string, offset time.Duration) TelemetryRecord {
		return TelemetryRecord{
			Schema: TelemetrySchema, RecordID: event + "-assessment", RecordedAt: now.Add(offset).Format(time.RFC3339Nano),
			Kind: "assessment", Source: "runner", Project: "example-worker", WorktreeID: "w", EventID: event, StateRevision: event,
			Status: "human_required", Decision: &TelemetryDecision{NeedsHuman: true},
			Assessment: &Assessment{NextOption: "restart_service", Probabilities: map[string]float64{"restart_service": 1}},
		}
	}
	records := []TelemetryRecord{
		assessment("resolved", 0),
		{Schema: TelemetrySchema, RecordID: "resolved-feedback", RecordedAt: now.Add(time.Minute).Format(time.RFC3339Nano), Kind: "feedback", Source: "agent", Project: "example-worker", WorktreeID: "w", EventID: "resolved", StateRevision: "resolved", Feedback: &TelemetryFeedback{HumanNeeded: &resolved, Resolved: &resolved}},
		assessment("unresolved", 2*time.Minute),
		{Schema: TelemetrySchema, RecordID: "unresolved-feedback", RecordedAt: now.Add(3 * time.Minute).Format(time.RFC3339Nano), Kind: "feedback", Source: "agent", Project: "example-worker", WorktreeID: "w", EventID: "unresolved", StateRevision: "unresolved", Feedback: &TelemetryFeedback{HumanNeeded: &resolved, Resolved: &unresolved}},
	}
	report := BuildQualityReport(records, time.Time{}, "example-worker", now.Add(4*time.Minute))
	if report.Resolution.Samples != 2 || report.Resolution.Resolved != 1 || report.Resolution.Unresolved != 1 || report.Resolution.Rate == nil || *report.Resolution.Rate != .5 {
		t.Fatalf("resolution = %+v", report.Resolution)
	}
}

func TestBuildQualityReportExplainsSessionCoverage(t *testing.T) {
	now := time.Date(2026, 9, 27, 5, 0, 0, 0, time.UTC)
	records := []TelemetryRecord{
		{Schema: TelemetrySchema, RecordID: "start", RecordedAt: now.Format(time.RFC3339Nano), Kind: "lifecycle", Source: "subscription", Project: "example-worker", SessionID: "session-1", Status: "subscribed"},
		{Schema: TelemetrySchema, RecordID: "stop", RecordedAt: now.Add(time.Minute).Format(time.RFC3339Nano), Kind: "lifecycle", Source: "subscription", Project: "example-worker", SessionID: "session-1", Status: "stopped", Polls: 12, EventsSeen: 1, JevCalls: 1, CacheHits: 3, NoEventPolls: 11},
	}
	report := BuildQualityReport(records, time.Time{}, "example-worker", now.Add(2*time.Minute))
	want := SessionMetric{Started: 1, Stopped: 1, Polls: 12, EventsSeen: 1, JevCalls: 1, CacheHits: 3, NoEventPolls: 11}
	if report.Sessions != want {
		t.Fatalf("sessions = %+v, want %+v", report.Sessions, want)
	}
}
