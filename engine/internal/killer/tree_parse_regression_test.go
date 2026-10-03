package killer

import "testing"

func TestParseProcessTableDoesNotFindParentDigitsInsidePID(t *testing.T) {
	for _, tc := range []struct {
		row         string
		pid, parent int
	}{
		{"  100   1 /bin/sh  -c npm run dev", 100, 1},
		{"  4210	42	/bin/sh  -c npm run dev", 4210, 42},
		{"  55  55 /bin/sh  -c npm run dev", 55, 55},
	} {
		got := parseProcessTable(tc.row)[tc.pid]
		if got.PPID != tc.parent || got.Command != "/bin/sh  -c npm run dev" {
			t.Errorf("parse %q = %+v; want parent %d and exact command tail", tc.row, got, tc.parent)
		}
	}
}

func TestProcessNameHandlesWhitespaceOnlyCommand(t *testing.T) {
	if got := (ProcessTable{12: {PID: 12, Command: " \t \n"}}).Name(12); got != "" {
		t.Fatalf("whitespace-only command name = %q, want unknown", got)
	}
}
