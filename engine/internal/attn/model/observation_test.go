package model

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func reportJSON(t *testing.T, records []TelemetryRecord, since time.Time, filter string, now time.Time) map[string]any {
	t.Helper()
	b, err := json.Marshal(BuildQualityReport(records, since, filter, now))
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}
func TestQualityCountsSingleAssessmentsBeforeSessionExit(t *testing.T) {
	now := time.Now().UTC()
	records := []TelemetryRecord{{Kind: "assessment", RecordedAt: now.Format(time.RFC3339Nano), ModelCalls: 1}, {Kind: "assessment", RecordedAt: now.Format(time.RFC3339Nano), Cached: true}}
	doc := reportJSON(t, records, time.Time{}, "", now)
	if doc["model_calls"] != float64(1) || doc["cache_hits"] != float64(1) {
		t.Fatalf("totals = %v, want one call and one cache hit", doc)
	}
}
func TestQualityDoesNotCallHistoricalSubscriptionActive(t *testing.T) {
	now := time.Now().UTC()
	doc := reportJSON(t, []TelemetryRecord{{Kind: "lifecycle", Status: "subscribed", SessionID: "old", RecordedAt: now.Add(-time.Hour).Format(time.RFC3339Nano)}}, time.Time{}, "", now)
	a, ok := doc["activity"].(map[string]any)
	if !ok || a["state"] != "recent" || a["observing_sessions"] != float64(0) {
		t.Fatalf("activity = %v, want historical, not observing", a)
	}
}

func TestObservationCountsLeaseAndFiltering(t *testing.T) {
	now := time.Now().UTC()
	at := now.Add(-time.Second).Format(time.RFC3339Nano)
	poll := TelemetryRecord{Kind: "assessment", RecordedAt: at, Project: "one", WorktreeID: "worktree-one", SessionID: "session-one", PerPoll: true, ObservationUntil: now.Add(10 * time.Second).Format(time.RFC3339Nano), Status: "continue", ModelCalls: 1}
	cache := poll
	cache.Cached = true
	cache.ModelCalls = 0
	cache.RecordedAt = now.Format(time.RFC3339Nano)
	records := []TelemetryRecord{poll, cache}
	report := BuildQualityReport(records, time.Time{}, "one", now)
	if report.ModelCalls != 1 || report.CacheHits != 1 || report.Activity.ObservingSessions != 1 || report.Activity.State != "observing" {
		t.Fatalf("live = %+v", report)
	}
	stopped := TelemetryRecord{Kind: "lifecycle", RecordedAt: now.Format(time.RFC3339Nano), Project: "one", SessionID: "session-one", Status: "stopped", PerPoll: true, ModelCalls: 1, CacheHits: 1}
	records = append(records, stopped)
	report = BuildQualityReport(records, time.Time{}, "one", now)
	if report.ModelCalls != 1 || report.CacheHits != 1 || report.Activity.ObservingSessions != 0 {
		t.Fatalf("after stop = %+v", report)
	}
	report = BuildQualityReport(records, time.Time{}, "other", now)
	if report.RecordsRead != 0 || report.Activity.State != "no_session" || report.ModelCalls != 0 {
		t.Fatalf("cross-project = %+v", report)
	}
	report = BuildQualityReport(records, now.Add(time.Millisecond), "one", now)
	if report.RecordsRead != 0 || report.ModelCalls != 0 {
		t.Fatalf("window = %+v", report)
	}
	report = BuildQualityReport([]TelemetryRecord{poll}, time.Time{}, "worktree-one", now.Add(time.Minute))
	if report.Activity.ObservingSessions != 0 || report.Activity.State != "recent" {
		t.Fatalf("expired observation = %+v", report.Activity)
	}
}
func TestLegacySessionCountsAreNotDoubleCountedOrInventedInPartialWindows(t *testing.T) {
	now := time.Now().UTC()
	start := TelemetryRecord{Kind: "lifecycle", RecordedAt: now.Add(-time.Hour).Format(time.RFC3339Nano), SessionID: "legacy", Status: "subscribed"}
	a := TelemetryRecord{Kind: "assessment", RecordedAt: now.Format(time.RFC3339Nano), SessionID: "legacy", ModelCalls: 1, Cached: true}
	stop := TelemetryRecord{Kind: "lifecycle", RecordedAt: now.Format(time.RFC3339Nano), SessionID: "legacy", Status: "stopped", ModelCalls: 2, CacheHits: 3}
	report := BuildQualityReport([]TelemetryRecord{start, a, stop}, time.Time{}, "", now)
	if report.ModelCalls != 2 || report.CacheHits != 3 {
		t.Fatalf("legacy = %+v", report)
	}
	report = BuildQualityReport([]TelemetryRecord{start, a, stop}, now.Add(-time.Minute), "", now)
	if report.ModelCalls != 1 || report.CacheHits != 1 || !report.LegacyCountsUnallocated {
		t.Fatalf("partial legacy = %+v", report)
	}
}
func TestObservationNoEventAndErrorsStayExplicit(t *testing.T) {
	now := time.Now().UTC()
	r := TelemetryRecord{Kind: "observation", Status: "clear", PerPoll: true, SessionID: "s", RecordedAt: now.Format(time.RFC3339Nano), ObservationUntil: now.Add(time.Second).Format(time.RFC3339Nano)}
	report := BuildQualityReport([]TelemetryRecord{r}, time.Time{}, "", now)
	if !report.Activity.NoEvent || report.Activity.State != "observing" || report.AssessmentRecords != 0 || report.ModelCalls != 0 {
		t.Fatalf("no event = %+v", report)
	}
	r.Status = "error"
	r.Reason = "private-text-must-not-be-shown"
	report = BuildQualityReport([]TelemetryRecord{r}, time.Time{}, "", now)
	if report.Activity.State != "failed" || report.Activity.Reason != "runner_error" {
		t.Fatalf("error = %+v", report.Activity)
	}
	r.Status = "private-text-must-not-be-shown"
	report = BuildQualityReport([]TelemetryRecord{r}, time.Time{}, "", now)
	if report.Activity.LastStatus != "unavailable" {
		t.Fatalf("unsafe status = %+v", report.Activity)
	}
}

func TestActivitySummaryExplainsQuietAndFailedStatesWithoutPrivateText(t *testing.T) {
	if !strings.Contains(ActivitySummary(ActivityMetric{State: "no_session"}), "permission alone") {
		t.Fatal("missing no-session explanation")
	}
	if !strings.Contains(ActivitySummary(ActivityMetric{State: "observing", ObservingSessions: 1, NoEvent: true}), "no eligible event") {
		t.Fatal("missing quiet-session explanation")
	}
	if !strings.Contains(ActivitySummary(ActivityMetric{State: "recent"}), "no fresh observation") {
		t.Fatal("historical activity looked active")
	}
	for _, a := range []ActivityMetric{{State: "failed", Reason: "private-path"}, {State: "observing", LastStatus: "private-path"}} {
		if strings.Contains(ActivitySummary(a), "private-path") {
			t.Fatal("private text rendered")
		}
	}
}
