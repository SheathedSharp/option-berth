package ports

import "testing"

// One listener is one row. The two addresses it may hold are a socket detail,
// and folding them here is what keeps the table, `--json` and the client on the
// same number of rows.
func TestMergeAddressesFoldsOneListenerIntoOneRow(t *testing.T) {
	rows := []ListeningPort{
		{Port: 6379, PID: 100, Process: "redis", BindAddress: "127.0.0.1", IPVersion: "IPv4"},
		{Port: 6379, PID: 100, Process: "redis", BindAddress: "::1", IPVersion: "IPv6"},
		{Port: 3000, PID: 200, Process: "node", BindAddress: "0.0.0.0", IPVersion: "IPv4"},
		{Port: 3000, PID: 200, Process: "node", BindAddress: "::", IPVersion: "IPv6"},
		{Port: 5432, PID: 300, Process: "postgres", BindAddress: "127.0.0.1", IPVersion: "IPv4"},
	}

	got := mergeAddresses(rows)
	if len(got) != 3 {
		t.Fatalf("rows = %d, want 3 (one per listener): %+v", len(got), got)
	}
	if got[0].BindAddress != "127.0.0.1" || got[0].IPVersion != "IPv4" {
		t.Errorf("the primary address should be the one that stays: %+v", got[0])
	}
	if len(got[0].BindAddresses) != 1 || got[0].BindAddresses[0] != "::1" {
		t.Errorf("the folded address should ride along: %+v", got[0].BindAddresses)
	}
	if len(got[1].BindAddresses) != 1 || got[1].BindAddresses[0] != "::" {
		t.Errorf("a wildcard pair folds the same way: %+v", got[1].BindAddresses)
	}
	// Absent, not empty: a listener that holds one address gains no field.
	if got[2].BindAddresses != nil {
		t.Errorf("a single-address listener should carry no list: %+v", got[2].BindAddresses)
	}
}

// Two processes on the same port are two listeners (only one of them can hold
// it, but a scan can see a socket mid-close) — folding them by port alone would
// hide one.
func TestMergeAddressesKeepsSeparateProcesses(t *testing.T) {
	rows := []ListeningPort{
		{Port: 8080, PID: 1, BindAddress: "127.0.0.1"},
		{Port: 8080, PID: 2, BindAddress: "127.0.0.1"},
	}
	if got := mergeAddresses(rows); len(got) != 2 {
		t.Fatalf("rows = %d, want 2: %+v", len(got), got)
	}
}
