package runtimecore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFirstErrorFromExitRespectsOffsetAndStripsANSI(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "auth.log")
	prev := "ERROR: stale error from previous run\n"
	curr := "booting auth\n\x1b[31mFATAL: missing JWT_SECRET\x1b[0m\n"
	if err := os.WriteFile(path, []byte(prev+curr), 0o600); err != nil {
		t.Fatal(err)
	}
	got := FirstErrorFromExit(path, int64(len(prev)), nil)
	if got != "FATAL: missing JWT_SECRET" {
		t.Fatalf("FirstErrorFromExit = %q, want %q", got, "FATAL: missing JWT_SECRET")
	}
}

func TestFirstErrorFromExitFallsBackToLastLinesWhenLogMissingOrLarge(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "rotated.log")
	got := FirstErrorFromExit(missing, 0, []string{"shutting down", "panic: nil pointer dereference"})
	if got != "panic: nil pointer dereference" {
		t.Fatalf("missing-log fallback = %q", got)
	}

	large := filepath.Join(t.TempDir(), "large.log")
	filler := strings.Repeat("ok line\n", int(DefaultFirstErrorLogBytes/8)+10)
	if err := os.WriteFile(large, []byte(filler+"ERROR: late crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got = FirstErrorFromExit(large, 0, []string{"ERROR: late crash"})
	if got != "ERROR: late crash" {
		t.Fatalf("large-log tail fallback = %q", got)
	}
}

func TestIsFailureExitReasonMatchesContract(t *testing.T) {
	for _, nonFailure := range []string{"", "stopped", "exited", "ended_unobserved"} {
		if IsFailureExitReason(nonFailure) {
			t.Errorf("IsFailureExitReason(%q) = true, want false", nonFailure)
		}
	}
	for _, failure := range []string{"crashed", "start_failed", "port_occupied", "ready_timeout"} {
		if !IsFailureExitReason(failure) {
			t.Errorf("IsFailureExitReason(%q) = false, want true", failure)
		}
	}
}
