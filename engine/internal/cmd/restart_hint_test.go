package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// The refusal hint is the recorded run as a start line: without cwd, group,
// name and port the caller would have to remember all four after the process
// died.
func TestAdHocStartCommandUsesTheRecordedRun(t *testing.T) {
	port := 8082
	rec := rpc.RunRecord{
		Group: "ai-employee-server", Name: "finance-tax-web", Cwd: "/work/web",
		Cmd: "yarn dev", PortHint: &port, Status: "exited",
	}
	want := "cd /work/web && oberth start -d --group ai-employee-server --name finance-tax-web --port 8082 -- yarn dev"
	if got := adHocStartCommand(rec); got != want {
		t.Fatalf("start line = %q, want %q", got, want)
	}

	want = "oberth start -d --group g --name n"
	if got := adHocStartCommand(rpc.RunRecord{Group: "g", Name: "n"}); got != want {
		t.Fatalf("bare start line = %q, want %q", got, want)
	}
}

func TestAdHocRestartHintPicksTheNewestMatchingRun(t *testing.T) {
	older := rpc.RunRecord{ID: "old", Group: "g", Name: "web", Cwd: "/old",
		Cmd: "old cmd", StartedAt: "2026-11-13T09:00:00Z", Status: "exited"}
	newer := rpc.RunRecord{ID: "new", Group: "g", Name: "web", Cwd: "/new",
		Cmd: "new cmd", StartedAt: "2026-11-13T10:00:00Z", Status: "exited"}
	otherGroup := rpc.RunRecord{ID: "other", Group: "elsewhere", Name: "web", Cwd: "/other",
		Cmd: "other cmd", StartedAt: "2026-11-13T11:00:00Z", Status: "exited"}

	hint := adHocRestartHint([]rpc.RunRecord{older, newer, otherGroup}, []string{"web"}, "g")
	if !strings.Contains(hint, "/new") || strings.Contains(hint, "/old") || strings.Contains(hint, "/other") {
		t.Fatalf("hint = %q, want the newest run in the caller's group", hint)
	}

	live := newer
	live.Status = "running"
	hint = adHocRestartHint([]rpc.RunRecord{live}, []string{"web"}, "g")
	if !strings.HasPrefix(hint, "this ad-hoc run is still running;") {
		t.Fatalf("live hint = %q, want the stop-first wording", hint)
	}

	if got := adHocRestartHint([]rpc.RunRecord{newer}, []string{"missing"}, "g"); got != "" {
		t.Fatalf("unknown name hint = %q, want none", got)
	}
}
