package ports

import (
	"testing"
	"time"
)

// The join is shared by both platforms; these are its rules, not one
// collector's. A field the row already carries wins, because this table is a
// second observation rather than a correction.
func TestApplyProcessFactsFillsOnlyMissingEvidence(t *testing.T) {
	table := map[int]processFacts{
		42: {ppid: 7, command: "/usr/bin/node server.js", startedAt: "Thu Apr  9 08:15:00 2026"},
		7:  {command: "/bin/sh launcher"},
	}
	row := ListeningPort{PID: 42, Command: "already observed", StartedAt: "2026-01-01T00:00:00Z"}
	applyProcessFacts(&row, table[42], table)

	if row.PPID != 7 {
		t.Errorf("PPID = %d, want 7", row.PPID)
	}
	if row.Command != "already observed" {
		t.Errorf("Command = %q, an existing field was overwritten", row.Command)
	}
	if row.StartedAt != "2026-01-01T00:00:00Z" {
		t.Errorf("StartedAt = %q, an existing field was overwritten", row.StartedAt)
	}
	if row.ParentCmd != "/bin/sh launcher" {
		t.Errorf("ParentCmd = %q, want the parent's command", row.ParentCmd)
	}
}

// A pid outside the table is unknown, not an empty command line.
func TestApplyProcessFactsDoesNotInventAParentCommand(t *testing.T) {
	row := ListeningPort{PID: 42}
	applyProcessFacts(&row, processFacts{ppid: 7}, map[int]processFacts{})
	if row.PPID != 7 {
		t.Errorf("PPID = %d, want the observed parent", row.PPID)
	}
	if row.ParentCmd != "" || row.Command != "" || row.StartedAt != "" {
		t.Fatalf("an empty observation invented facts: %+v", row)
	}
}

// started_at is published as RFC3339 whichever collector reported it.
func TestApplyProcessFactsParsesEitherStartForm(t *testing.T) {
	forms := map[string]processFacts{
		"unix lstart": {ppid: 1, startedAt: "Thu Apr  9 08:15:00 2026"},
		"windows cim": {ppid: 1, startedAt: "2026-04-09T08:15:00+02:00"},
		"unparsable":  {ppid: 1, startedAt: "not a time"},
	}
	for name, facts := range forms {
		row := ListeningPort{PID: 42}
		applyProcessFacts(&row, facts, map[int]processFacts{})
		if name == "unparsable" {
			if row.StartedAt != "" {
				t.Errorf("%s: StartedAt = %q, want empty", name, row.StartedAt)
			}
			continue
		}
		if _, err := time.Parse(time.RFC3339, row.StartedAt); err != nil {
			t.Errorf("%s: StartedAt = %q, not RFC3339: %v", name, row.StartedAt, err)
		}
	}
}

// One parse serves both facts: ProcessStart reads the same raw text the join
// reads, without formatting it to a string and parsing it again.
func TestProcessStartParsesTheRememberedText(t *testing.T) {
	restore := scanParentTable.Load()
	t.Cleanup(func() { scanParentTable.Store(restore) })

	rememberProcessFacts(map[int]processFacts{
		5151: {ppid: 1, startedAt: "2026-04-09T08:15:00+02:00"},
		5252: {ppid: 1}, // no start time recorded
	})
	if started, ok := ProcessStart(5151); !ok || started.Format(time.RFC3339) != "2026-04-09T08:15:00+02:00" {
		t.Fatalf("ProcessStart = %v, ok %v", started, ok)
	}
	if _, ok := ProcessStart(5252); ok {
		t.Fatal("a pid without a start time reported one")
	}
}
