package ports

import "testing"

func TestCIMRetainsBirthWhenCommandAndParentAreHidden(t *testing.T) {
	rows := parseCIMProcesses(`"ProcessId","ParentProcessId","StartedAt","CommandLine"
"42","0","2026-01-01T00:00:00Z",""
"43","0","not-a-time",""
`)
	got, ok := rows[42]
	if !ok || got.startedAt != "2026-01-01T00:00:00Z" || got.command != "" || got.ppid != 0 {
		t.Fatalf("partial birth evidence discarded or invented: %+v", rows)
	}
	if _, ok := rows[43]; ok {
		t.Fatal("invalid empty identity accepted")
	}
}
