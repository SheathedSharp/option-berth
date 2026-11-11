//go:build !windows

package cmd

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

func TestPrintLogTargetsNamesEachSource(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	out := captureStdout(t, func() {
		printLogTargets([]string{"/var/log/api.log", "/proc/42/fd/2 (stderr)"})
	})
	const want = "  /var/log/api.log\n  /proc/42/fd/2 (stderr)\n\n"
	if out != want {
		t.Fatalf("header = %q, want %q", out, want)
	}
}

func TestFollowArgsFollowTheLogsFlag(t *testing.T) {
	prev := logsFollow
	t.Cleanup(func() { logsFollow = prev })

	logsFollow = false
	if got := followArgs(); got != nil {
		t.Errorf("followArgs() = %v with following off, want none", got)
	}
	logsFollow = true
	if got := followArgs(); len(got) != 1 || got[0] != "-f" {
		t.Errorf("followArgs() = %v with following on, want -f", got)
	}
}
