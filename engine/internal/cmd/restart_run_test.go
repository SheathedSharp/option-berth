package cmd

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

func TestSelectNamedRunPrefersAnExactIDOverAName(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-1", Group: "demo", Name: "run-1", Status: "running"},
		{ID: "other", Group: "demo", Name: "web", Status: "running"},
	}
	rec, err := selectNamedRun(records, "demo", "run-1")
	if err != nil || rec.ID != "run-1" {
		t.Fatalf("select = %+v/%v, want the exact id", rec, err)
	}
}

func TestSelectNamedRunRejectsAnAmbiguousLiveName(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "run-1", Group: "demo", Name: "web", Status: "running"},
		{ID: "run-2", Group: "demo", Name: "web", Status: "running"},
	}
	_, err := selectNamedRun(records, "demo", "web")
	code, msg, hint := describe(err)
	if code != "ambiguous" || msg == "" || hint == "" {
		t.Fatalf("code=%q msg=%q hint=%q, want an ambiguous refusal with the ids", code, msg, hint)
	}
}

func TestSelectNamedRunFallsBackToTheNewestFinishedGeneration(t *testing.T) {
	records := []rpc.RunRecord{
		{ID: "old", Group: "demo", Name: "web", Status: "exited", StartedAt: "2026-11-16T08:00:00Z"},
		{ID: "new", Group: "demo", Name: "web", Status: "exited", StartedAt: "2026-11-16T09:00:00Z"},
	}
	rec, err := selectNamedRun(records, "demo", "web")
	if err != nil || rec.ID != "new" {
		t.Fatalf("select = %+v/%v, want the newest finished generation", rec, err)
	}
}

func TestSelectNamedRunScopesToTheGroup(t *testing.T) {
	records := []rpc.RunRecord{{ID: "elsewhere", Group: "other", Name: "web", Status: "running"}}
	_, err := selectNamedRun(records, "demo", "web")
	code, _, _ := describe(err)
	if code != "not_found" {
		t.Fatalf("code = %q, want not_found in this group", code)
	}
}
