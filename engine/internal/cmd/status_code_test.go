package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestPrintStatusServicesShowsChangedCodeAndNextStep(t *testing.T) {
	changed := true
	files := 2
	hint := "oberth restart --only api"
	doc := statusDocument{Worktree: state.Group{Services: []state.Service{{
		Name: "api", Running: true,
		CodeChangedSinceStart: &changed, CodeChangedFiles: &files, CodeHint: &hint,
	}}}}
	out := captureStdout(t, func() { printStatus(doc) })
	for _, want := range []string{"code changed since start (2 files)", hint} {
		if !strings.Contains(out, want) {
			t.Fatalf("output %q lacks %q", out, want)
		}
	}
}
