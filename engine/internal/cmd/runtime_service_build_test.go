package cmd

import (
	"strings"
	"testing"
)

// The one failure this software cannot see from the inside: a runtime service left
// running from an older build answers with the old behavior, and its version
// string looks exactly the same.
func TestStaleRuntimeServiceNote(t *testing.T) {
	if note := staleRuntimeServiceNote("abc1234", "abc1234"); note != "" {
		t.Errorf("the same build needs no note, got %q", note)
	}

	note := staleRuntimeServiceNote("abc1234", "def5678")
	if !strings.Contains(note, "abc1234") || !strings.Contains(note, "def5678") {
		t.Errorf("the note should name both builds: %q", note)
	}
	if !strings.Contains(note, "oberth runtime service restart") {
		t.Errorf("the note should say what to do: %q", note)
	}

	// A runtime service built without the version stamps (a plain `go build`) cannot be
	// compared, and a note that says nothing useful is worse than none.
	for _, pair := range [][2]string{{"", "def5678"}, {"abc1234", ""}, {"", ""}} {
		if note := staleRuntimeServiceNote(pair[0], pair[1]); note != "" {
			t.Errorf("staleRuntimeServiceNote(%q, %q) = %q, want no note", pair[0], pair[1], note)
		}
	}
}
