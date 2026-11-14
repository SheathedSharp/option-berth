package runner

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

type fixtureTransport func(*http.Request) (*http.Response, error)

func (f fixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func observationClient(calls *int) model.Client {
	return model.Client{Endpoint: "https://fixture.invalid/decisions", Model: "fixture", APIKey: "synthetic-only", HTTPClient: &http.Client{Transport: fixtureTransport(func(*http.Request) (*http.Response, error) {
		*calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"fixture","answers":{"task_relevant":{"type":"noul","noul":0.9},"needs_human":{"type":"noul","noul":0.1}}}`))}, nil
	})}}
}
func TestRecordedAttemptPreservesCallAfterRefreshFailure(t *testing.T) {
	s, _ := runnerFixture(t)
	home := t.TempDir()
	calls, reads := 0, 0
	task := model.TaskContext{Task: "fixture task", AuthorizedOptions: []string{"inspect_logs"}}
	_, err := AssessRecorded(context.Background(), "unused", s.Scope.Root, home, observationClient(&calls), task, func(context.Context, string, string) (Status, error) {
		reads++
		if reads > 1 {
			return Status{}, errors.New("private project error")
		}
		return s, nil
	}, "")
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	rows, e := model.ReadTelemetry(home)
	if e != nil || len(rows) != 1 || rows[0].ModelCalls != 1 || rows[0].Status != "error" {
		t.Fatalf("rows=%+v err=%v", rows, e)
	}
	b, _ := os.ReadFile(model.TelemetryPath(home))
	if strings.Contains(string(b), "private project") || strings.Contains(string(b), "fixture task") {
		t.Fatal("private text entered telemetry")
	}
}
func TestRecordedEarlyFailureIsVisibleAndNotAModelCall(t *testing.T) {
	home := t.TempDir()
	calls := 0
	_, err := AssessRecorded(context.Background(), "unused", t.TempDir(), home, observationClient(&calls), model.TaskContext{}, func(context.Context, string, string) (Status, error) {
		return Status{}, errors.New("status read failed")
	}, "")
	rows, e := model.ReadTelemetry(home)
	if err == nil || e != nil || len(rows) != 1 || calls != 0 || rows[0].ModelCalls != 0 || rows[0].Status != "error" {
		t.Fatalf("rows=%+v err=%v/%v calls=%d", rows, err, e, calls)
	}
}
func TestCallsAndCacheAreVisibleBeforeSubscriptionEnds(t *testing.T) {
	s, _ := runnerFixture(t)
	home := t.TempDir()
	calls := 0
	task := model.TaskContext{Task: "fixture task", AuthorizedOptions: []string{"inspect_logs"}}
	client := observationClient(&calls)
	read := func(context.Context, string, string) (Status, error) { return s, nil }
	out, err := AssessRecorded(context.Background(), "unused", s.Scope.Root, home, client, task, read, "")
	if err != nil || !out.ModelCalled || calls != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
	}
	AppendLifecycleTelemetry(home, s.Scope.Root, "subscribed", "subscription", nil)
	for i := 0; i < 2; i++ {
		out, err = Auto(context.Background(), "unused", s.Scope.Root, client, task, read)
		if err != nil || !out.Cached || out.ModelCalled {
			t.Fatalf("cache=%+v err=%v", out, err)
		}
		out.SessionID = "subscription"
		AppendSubscriptionTelemetry(home, s.Scope.Root, task, out, 0, 30*time.Second)
	}
	rows, err := model.ReadTelemetry(home)
	if err != nil {
		t.Fatal(err)
	}
	report := model.BuildQualityReport(rows, time.Time{}, "", time.Now())
	if report.ModelCalls != 1 || report.CacheHits != 2 || report.Activity.ObservingSessions != 1 {
		t.Fatalf("live=%+v", report)
	}
	AppendLifecycleTelemetry(home, s.Scope.Root, "stopped", "subscription", &SubscriptionStats{ModelCalls: 0, CacheHits: 2})
	rows, _ = model.ReadTelemetry(home)
	report = model.BuildQualityReport(rows, time.Time{}, "", time.Now())
	if report.ModelCalls != 1 || report.CacheHits != 2 || report.Activity.ObservingSessions != 0 {
		t.Fatalf("stopped=%+v", report)
	}
}
func TestNoEventPollsObserveWithoutCallingAndRevokeOnDisable(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	task := model.TaskContext{Task: "fixture", AuthorizedOptions: []string{"inspect_logs"}}
	calls, polls, observations := 0, 0, 0
	enabled := true
	read := WhileConfigured(func() model.Resolved {
		return model.Resolved{Enabled: enabled, Endpoint: "https://fixture.invalid/decisions", Model: "fixture", Key: "synthetic-only"}
	}, observationClient(&calls), func(context.Context, string, string) (Status, error) {
		polls++
		s := Status{}
		s.Scope.Root = root
		return s, nil
	})
	AppendLifecycleTelemetry(home, root, "subscribed", "owned", nil)
	stats, err := SubscribeWithStats(context.Background(), "unused", root, time.Millisecond, observationClient(&calls), task, read, "owned", func(out Output, latency time.Duration) {
		observations++
		AppendSubscriptionTelemetry(home, root, task, out, latency, time.Millisecond)
		if observations == 1 {
			rows, _ := model.ReadTelemetry(home)
			report := model.BuildQualityReport(rows, time.Time{}, "", time.Now())
			if !report.Activity.NoEvent || report.ModelCalls != 0 || report.AssessmentRecords != 0 {
				t.Errorf("quiet=%+v", report)
			}
			enabled = false
		}
	}, func(any) error { return nil })
	if err != nil || calls != 0 || polls != 1 || observations != 2 || stats.Unavailable != 1 {
		t.Fatalf("err=%v calls=%d polls=%d observations=%d stats=%+v", err, calls, polls, observations, stats)
	}
	rowsBeforeStop, readErr := model.ReadTelemetry(home)
	if readErr != nil || len(rowsBeforeStop) != 3 || rowsBeforeStop[2].Reason != model.ReasonDisabled {
		t.Fatalf("disabled observation missing: %+v err=%v", rowsBeforeStop, readErr)
	}
	AppendLifecycleTelemetry(home, root, "stopped", "owned", &stats)
	rows, _ := model.ReadTelemetry(home)
	report := model.BuildQualityReport(rows, time.Time{}, "", time.Now())
	if report.Activity.ObservingSessions != 0 || report.ModelCalls != 0 {
		t.Fatalf("disabled=%+v", report)
	}
}
func TestCancelledContextDoesNotAttemptTransport(t *testing.T) {
	s, _ := runnerFixture(t)
	calls := 0
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := Auto(ctx, "unused", s.Scope.Root, observationClient(&calls), model.TaskContext{Task: "fixture", AuthorizedOptions: []string{"inspect_logs"}}, func(context.Context, string, string) (Status, error) { return s, nil })
	if calls != 0 || out.ModelCalled {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, calls)
	}
}

func TestQualityPathFilterDoesNotMixSameNamedWorktrees(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	roots := []string{t.TempDir() + "/checkout", t.TempDir() + "/checkout"}
	for _, root := range roots {
		_, err := model.AppendTelemetry(home, model.TelemetryRecord{Kind: "assessment", EventID: "fixture-event", StateRevision: "fixture-revision", Project: "checkout", WorktreeID: model.WorktreeID(root), Status: "continue", ModelCalls: 1})
		if err != nil {
			t.Fatal(err)
		}
	}
	report, err := QualityReport(0, roots[0])
	if err != nil || report.ModelCalls != 1 || report.RecordsRead != 1 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	report, err = QualityReport(0, "checkout")
	if err != nil || report.ModelCalls != 2 {
		t.Fatalf("label report=%+v err=%v", report, err)
	}
}
func TestDisabledTaskRecordsReasonWithoutTransport(t *testing.T) {
	s, _ := runnerFixture(t)
	home := t.TempDir()
	calls := 0
	client := observationClient(&calls)
	client.APIKey = ""
	out, err := AssessRecorded(context.Background(), "unused", s.Scope.Root, home, client, model.TaskContext{Task: "fixture", AuthorizedOptions: []string{"inspect_logs"}}, func(context.Context, string, string) (Status, error) { return s, nil }, model.ReasonDisabled)
	if err != nil || out.Status != "unavailable" || out.Reason != model.ReasonDisabled || out.ModelCalled || calls != 0 {
		t.Fatalf("disabled=%+v err=%v calls=%d", out, err, calls)
	}
}

func TestConfigurationChangeStopsBeforeAnotherStatusOrCall(t *testing.T) {
	calls := 0
	expected := observationClient(&calls)
	base := model.Resolved{Enabled: true, Endpoint: expected.Endpoint, Model: expected.Model, Key: expected.APIKey}
	for _, field := range []string{"endpoint", "model", "key", "missing_key"} {
		t.Run(field, func(t *testing.T) {
			current := base
			switch field {
			case "endpoint":
				current.Endpoint = "https://other.invalid/decisions"
			case "model":
				current.Model = "changed"
			case "key":
				current.Key = "changed-synthetic"
			case "missing_key":
				current.Key = ""
			}
			reads := 0
			read := WhileConfigured(func() model.Resolved { return current }, expected, func(context.Context, string, string) (Status, error) { reads++; return Status{}, nil })
			_, err := read(context.Background(), "unused", ".")
			if !errors.Is(err, ErrConfigurationChanged) || reads != 0 || calls != 0 {
				t.Fatalf("err=%v reads=%d calls=%d", err, reads, calls)
			}
		})
	}
}

func TestPreflightCountsOnlyObservedTransportAttempts(t *testing.T) {
	home := t.TempDir()
	request := model.Preflight{RequestID: "fixture-preflight", WorktreeRoot: t.TempDir(), StateRevision: "fixture-revision"}
	RecordPreflightAssessment(home, request, model.TaskContext{}, nil, model.WithReason(model.ReasonNoKey, model.ErrUnavailable), false)
	RecordPreflightAssessment(home, request, model.TaskContext{}, nil, model.WithReason(model.ReasonNetwork, model.ErrUnavailable), true)
	rows, err := model.ReadTelemetry(home)
	if err != nil || len(rows) != 2 || rows[0].ModelCalls != 0 || rows[1].ModelCalls != 1 || rows[1].Reason != model.ReasonNetwork {
		t.Fatalf("preflight=%+v err=%v", rows, err)
	}
}
