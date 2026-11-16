package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/restartout"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// A warning is only for an observed change. Unknown stays unknown, and the
// --only filter keeps a restart from warning about a service it will not touch.
func TestRestartWarningsOnlyForObservedChanges(t *testing.T) {
	changed, unchanged := true, false
	files := 2
	codes := []rpc.ServiceCode{
		{Name: "api", Changed: &changed, ChangedFiles: &files},
		{Name: "worker", ExecutableChangedSinceStart: &changed},
		{Name: "idle", Changed: &unchanged},
		{Name: "unknown"},
	}
	got := restartWarnings(codes, nil)
	if len(got) != 2 {
		t.Fatalf("warnings = %+v, want api and worker", got)
	}
	if got[0].Service != "api" || !got[0].CodeChanged ||
		got[0].CodeChangedFiles == nil || *got[0].CodeChangedFiles != files ||
		got[0].ExecutableChanged {
		t.Fatalf("api warning = %+v", got[0])
	}
	if got[1].Service != "worker" || got[1].CodeChanged || !got[1].ExecutableChanged {
		t.Fatalf("worker warning = %+v", got[1])
	}
	if filtered := restartWarnings(codes, []string{"worker"}); len(filtered) != 1 || filtered[0].Service != "worker" {
		t.Fatalf("filtered = %+v, want only worker", filtered)
	}
}

func TestPrintRestartWarningsNamesTheFacts(t *testing.T) {
	noColor(t)
	files := 2
	out := captureStdout(t, func() {
		printRestartWarnings([]restartout.Warning{
			{Service: "api", CodeChanged: true, CodeChangedFiles: &files},
			{Service: "worker", ExecutableChanged: true},
		})
	})
	for _, want := range []string{
		"warning: api's sources changed since it started (2 files)",
		"warning: worker's executable was rebuilt after this run",
		"restart does not rebuild sources",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q:\n%s", want, out)
		}
	}
}
