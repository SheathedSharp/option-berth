package cmd

// `oberth attn status --json` is read at task entry by an agent that came for one
// fact: is a runner observing this task, and is anything happening. The decision
// document stays small; the full quality ledger is one explicit flag away.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
)

// attnStatusDocument runs `oberth attn status --json [args…]` against an
// isolated home whose ledger already holds one per-poll assessment, and returns
// the decoded document.
func attnStatusDocument(t *testing.T, args ...string) map[string]any {
	t.Helper()
	resetAttnStatusFlags(t)
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN", "")
	t.Setenv("BERTH_ATTN_KEY", "")
	t.Setenv("BERTH_ATTN_ENDPOINT", "")
	t.Setenv("BERTH_ATTN_MODEL", "")
	seedAttnLedger(t, home)

	rootCmd.SetArgs(append([]string{"attn", "status", "--json"}, args...))
	var err error
	out := captureStdout(t, func() { err = rootCmd.Execute() })
	if err != nil {
		t.Fatalf("attn status --json %v: %v\n%s", args, err, out)
	}
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("stdout is not the status document: %v\n%s", err, out)
	}
	return doc
}

// seedAttnLedger writes one live observation lease and one assessment for the
// same session, so the report has both activity and a case to count.
func seedAttnLedger(t *testing.T, home string) {
	t.Helper()
	now := time.Now().UTC()
	record := model.TelemetryRecord{
		Kind: "assessment", Project: "example-worker", SessionID: "session-one", PerPoll: true,
		Status: "clear", EventID: "event-one", StateRevision: "rev-one", ModelCalls: 1,
		RecordedAt:       now.Format(time.RFC3339Nano),
		ObservationUntil: now.Add(30 * time.Second).Format(time.RFC3339Nano),
	}
	if _, err := model.AppendTelemetry(home, record); err != nil {
		t.Fatal(err)
	}
}

// resetAttnStatusFlags restores the package flags and, just as important, the
// pflag "changed" bit: the tree is built once per process, so a `--since` one
// test passed would otherwise keep implying the full report for the next one.
func resetAttnStatusFlags(t *testing.T) {
	t.Helper()
	savedJSON, savedQuality := attnJSONFlag, attnQualityFlag
	savedSince, savedProject := attnStatusSinceFlag, attnQualityProjectFlag
	t.Cleanup(func() {
		attnJSONFlag, attnQualityFlag = savedJSON, savedQuality
		attnStatusSinceFlag, attnQualityProjectFlag = savedSince, savedProject
		for _, name := range []string{"json", "quality", "since", "quality-project"} {
			if flag := attnStatusCmd.Flags().Lookup(name); flag != nil {
				flag.Changed = false
			}
		}
		rootCmd.SetArgs(nil)
	})
	attnJSONFlag, attnQualityFlag = false, false
	attnStatusSinceFlag, attnQualityProjectFlag = 168*time.Hour, ""
}

func attnStatusQualityObject(t *testing.T, doc map[string]any) map[string]any {
	t.Helper()
	quality, ok := doc["quality"].(map[string]any)
	if !ok {
		t.Fatalf("quality = %v, want an object", doc["quality"])
	}
	return quality
}

// The default document carries the consent decision and the counters that
// answer "is a runner observing, and is anything happening" — nothing else.
func TestAttnStatusJSONDefaultKeepsOnlyTheDecisionFields(t *testing.T) {
	doc := attnStatusDocument(t)

	for _, field := range []string{"enabled", "endpoint", "model", "timeout_ms", "key_source", "consent_source"} {
		if _, ok := doc[field]; !ok {
			t.Errorf("the decision document is missing consent field %q: %v", field, doc)
		}
	}

	quality := attnStatusQualityObject(t, doc)
	activity, ok := quality["activity"].(map[string]any)
	if !ok || activity["state"] != "observing" {
		t.Fatalf("quality.activity = %v, want the fresh lease the attaching skill reads", quality["activity"])
	}
	for field, want := range map[string]float64{
		"records_read": 1, "assessment_records": 1, "model_calls": 1, "cache_hits": 0,
		"feedback_records": 0, "unique_cases": 1, "labeled_cases": 0, "unlabeled_cases": 1,
	} {
		got, present := quality[field]
		if !present || got != want {
			t.Errorf("quality.%s = %v (present %t), want %v; zero is a fact, not an omission", field, got, present, want)
		}
	}
	if _, ok := quality["sessions"].(map[string]any); !ok {
		t.Errorf("quality.sessions = %v, want the session counts", quality["sessions"])
	}
	for _, buried := range []string{
		"human_decision", "model_needs_human", "model_task_relevant", "recommendation",
		"actions", "resolution", "detection_latency", "assessment_availability",
		"status_counts", "event_counts", "project_counts", "decision_reason_counts",
		"recommended_option_counts", "model_counts", "user_decision_counts", "label_coverage",
	} {
		if _, present := quality[buried]; present {
			t.Errorf("the default document still carries the quality block %q", buried)
		}
	}
}

// The full ledger is still one flag away, with the same shape it always had.
func TestAttnStatusQualityFlagReturnsTheFullReport(t *testing.T) {
	doc := attnStatusDocument(t, "--quality")
	quality := attnStatusQualityObject(t, doc)

	for _, field := range []string{
		"schema", "generated_at", "source", "window_start", "activity",
		"human_decision", "model_needs_human", "model_task_relevant", "recommendation",
		"actions", "resolution", "detection_latency", "assessment_availability",
		"status_counts", "event_counts", "project_counts", "decision_reason_counts",
		"recommended_option_counts", "model_counts", "user_decision_counts",
		"label_coverage", "sessions",
	} {
		if _, ok := quality[field]; !ok {
			t.Errorf("--quality is missing quality.%s: %v", field, quality)
		}
	}
	if quality["model_calls"] != float64(1) || quality["assessment_records"] != float64(1) {
		t.Errorf("the full report lost the counts: %v", quality)
	}
}

// A filter with no subject is meaningless: asking for a window or a project is
// asking for the ledger those narrow.
func TestAttnStatusQualityFiltersImplyTheFullReport(t *testing.T) {
	for _, args := range [][]string{
		{"--since", "24h"},
		{"--since", "0"},
		{"--quality-project", "example-worker"},
	} {
		t.Run(args[0]+args[1], func(t *testing.T) {
			quality := attnStatusQualityObject(t, attnStatusDocument(t, args...))
			if _, ok := quality["human_decision"]; !ok {
				t.Fatalf("attn status --json %v did not imply --quality: %v", args, quality)
			}
			if _, ok := quality["sessions"].(map[string]any); !ok {
				t.Fatalf("attn status --json %v lost the full report shape: %v", args, quality)
			}
		})
	}
}
