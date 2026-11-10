package cmd

import (
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// TestPortCompletionsCarryThePortAndItsName: the shell inserts the number but
// shows the name, so the two are separated by a tab and never reordered.
func TestPortCompletionsCarryThePortAndItsName(t *testing.T) {
	rows := []ports.ListeningPort{
		{Port: 3000, Process: "node", Display: "node"},
		{Port: 5432, Process: "postgres"},
	}
	got := portCompletions(rows)
	if len(got) != 2 {
		t.Fatalf("completions = %v, want one per row", got)
	}
	if got[0] != "3000\tnode" || got[1] != "5432\tpostgres" {
		t.Fatalf("completions = %#v, want port<TAB>name in row order", got)
	}
	if empty := portCompletions(nil); len(empty) != 0 {
		t.Fatalf("completions = %v for no rows, want none", empty)
	}
}
