package runner

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

func TestAppendDeliveryTelemetryDoesNotWriteFeedbackLabels(t *testing.T) {
	home := t.TempDir()
	project := t.TempDir()
	AppendDeliveryTelemetry(home, project, "event-1", "revision-1")
	records, err := model.ReadTelemetry(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records=%d, want 1", len(records))
	}
	rec := records[0]
	if rec.Kind != "lifecycle" || rec.Source != "claude-hook" || rec.Status != "delivered" {
		t.Fatalf("unexpected delivery record: %+v", rec)
	}
	if rec.EventID != "event-1" || rec.StateRevision != "revision-1" {
		t.Fatalf("missing finding identity: %+v", rec)
	}
	if rec.Feedback != nil {
		t.Fatalf("must not write human feedback labels: %+v", rec.Feedback)
	}
	report := model.BuildQualityReport(records, time.Time{}, "", time.Now().UTC())
	if report.FeedbackRecords != 0 || report.AssessmentRecords != 0 {
		t.Fatalf("delivery counted as feedback or assessment: feedback=%d assessments=%d", report.FeedbackRecords, report.AssessmentRecords)
	}
	if report.RecordsRead != 1 {
		t.Fatalf("records_read=%d", report.RecordsRead)
	}
}
