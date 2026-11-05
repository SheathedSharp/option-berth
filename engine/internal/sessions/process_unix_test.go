//go:build !windows

package sessions

import "testing"

func TestParsePSKeepsTheCommandIntact(t *testing.T) {
	table := parsePS("  100     1 /usr/bin/claude code\n 200   100 zsh\nnot a row\n\n  x   1 broken\n")
	if len(table) != 2 {
		t.Fatalf("got %d rows, want 2: %+v", len(table), table)
	}
	if table[0].PID != 100 || table[0].PPID != 1 {
		t.Errorf("first row ids = %d/%d", table[0].PID, table[0].PPID)
	}
	if table[0].Command != "/usr/bin/claude code" {
		t.Errorf("command = %q, want the whole trailing text", table[0].Command)
	}
	if table[1].Command != "zsh" {
		t.Errorf("second command = %q", table[1].Command)
	}
}

func TestProcessTableIsUsableOnThisHost(t *testing.T) {
	table := processTable()
	if len(table) == 0 {
		t.Skip("ps produced no process table here")
	}
	for _, row := range table {
		if row.Command == "" {
			t.Fatalf("process %d has no command", row.PID)
		}
	}
}
