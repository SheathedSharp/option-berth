package attn

import (
	"os"
	"testing"
)

func TestGlanceAtReportsKindWithoutEvidence(t *testing.T) {
	home := t.TempDir()
	root := "/tmp/example-worktree"
	missing, err := GlanceAt(home, root)
	if err != nil || missing.Present {
		t.Fatalf("missing artifact = %+v %v", missing, err)
	}

	body := `{
		"schema":"oberth.attn/v1",
		"event_id":"event-one",
		"state_revision":"rev-one",
		"worktree_root":"` + root + `",
		"generated_at":"2026-11-20T09:52:57.238605Z",
		"fresh_until":"2026-11-20T10:02:57.238605Z",
		"first_seen":"2026-11-20T09:52:43Z",
		"last_seen":"2026-11-20T09:52:57Z",
		"event":{"kind":"ready_timeout","service":"secret-service","reason":"ready_timeout","at":"2026-11-20T09:37:57Z"},
		"evidence":[{"ref":"secret-ref","log_path":"/secret/log"}],
		"options":[{"id":"inspect_logs"}]
	}`
	if err := os.MkdirAll(home+"/attention", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home, root), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := GlanceAt(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.EventKind != "ready_timeout" || got.GeneratedAt != "2026-11-20T09:52:57Z" {
		t.Fatalf("glance = %+v", got)
	}
	if got.ObservedAfter("2026-11-20T06:39:18.618736Z") {
		t.Fatal("earlier observation looked like it followed the artifact")
	}
	if !got.ObservedAfter("2026-11-20T09:52:57.238605Z") && !got.ObservedAfter("2026-11-20T11:00:00Z") {
		t.Fatal("same-time or later observation should count as followed")
	}
	if got.ObservedAfter("2026-11-20T11:00:00Z") == false {
		t.Fatal("later observation should count as followed")
	}
}

func TestGlanceAtHidesUnknownKindAndDoesNotRequireFreshness(t *testing.T) {
	home := t.TempDir()
	root := "/tmp/stale-worktree"
	body := `{
		"schema":"oberth.attn/v1",
		"event_id":"event-two",
		"state_revision":"rev-two",
		"worktree_root":"` + root + `",
		"generated_at":"2026-11-20T09:52:57Z",
		"fresh_until":"2026-11-20T10:02:57Z",
		"first_seen":"2026-11-20T09:52:43Z",
		"last_seen":"2026-11-20T09:52:57Z",
		"event":{"kind":"private-kind","at":"2026-11-20T09:37:57Z"},
		"evidence":[],
		"options":[]
	}`
	if err := os.MkdirAll(home+"/attention", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home, root), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := GlanceAt(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.EventKind != "" {
		t.Fatalf("unknown kind leaked or missing presence: %+v", got)
	}
}

func TestGlanceAtSameSecondEarlierObservationIsNotFollowed(t *testing.T) {
	home := t.TempDir()
	root := "/tmp/same-second-worktree"
	body := `{
		"schema":"oberth.attn/v1",
		"event_id":"event-three",
		"state_revision":"rev-three",
		"worktree_root":"` + root + `",
		"generated_at":"2026-11-20T09:52:57.800000Z",
		"fresh_until":"2026-11-20T10:02:57.800000Z",
		"first_seen":"2026-11-20T09:52:57.800000Z",
		"last_seen":"2026-11-20T09:52:57.800000Z",
		"event":{"kind":"ready_timeout","at":"2026-11-20T09:52:57.800000Z"},
		"evidence":[],
		"options":[]
	}`
	if err := os.MkdirAll(home+"/attention", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(home, root), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := GlanceAt(home, root)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Present || got.GeneratedAt != "2026-11-20T09:52:57Z" {
		t.Fatalf("display timestamp = %+v", got)
	}
	if got.ObservedAfter("2026-11-20T09:52:57.100000Z") {
		t.Fatal("an earlier observation in the same second was reported as already followed")
	}
	if !got.ObservedAfter("2026-11-20T09:52:57.800000Z") {
		t.Fatal("an observation at the exact publish time should count as followed")
	}
	if !got.ObservedAfter("2026-11-20T09:52:57.900000Z") {
		t.Fatal("a later observation in the same second should count as followed")
	}
}
