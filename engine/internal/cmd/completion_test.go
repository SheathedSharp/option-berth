package cmd

import (
	"errors"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/spf13/cobra"
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

// stubCompletionScan replaces the listener scan for one test and reports whether
// the command asked for it.
func stubCompletionScan(t *testing.T, scan func() ([]ports.ListeningPort, error)) *bool {
	t.Helper()
	called := false
	prev := scanCompletionPorts
	scanCompletionPorts = func() ([]ports.ListeningPort, error) {
		called = true
		return scan()
	}
	t.Cleanup(func() { scanCompletionPorts = prev })
	return &called
}

// TestCompletePortOffersEveryListener: with nothing typed yet the command scans
// and offers one candidate per listener, formatted port<TAB>name.
func TestCompletePortOffersEveryListener(t *testing.T) {
	stubCompletionScan(t, func() ([]ports.ListeningPort, error) {
		return []ports.ListeningPort{
			{Port: 3000, Process: "node", Display: "web"},
			{Port: 5432, Process: "postgres"},
		}, nil
	})

	got, directive := completePort(&cobra.Command{}, nil, "")
	want := []string{"3000\tweb", "5432\tpostgres"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("completions = %#v, want %#v", got, want)
	}
	if directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("directive = %v, want NoFileComp", directive)
	}
}

// TestCompletePortReportsABlindScan: a scan that could not read the listener
// table is an error directive, never an empty successful candidate list.
func TestCompletePortReportsABlindScan(t *testing.T) {
	called := stubCompletionScan(t, func() ([]ports.ListeningPort, error) {
		return nil, errors.New("cannot read the listener table")
	})

	got, directive := completePort(&cobra.Command{}, nil, "")
	if !*called {
		t.Fatal("the command answered without scanning")
	}
	if got != nil {
		t.Fatalf("completions = %v, want none", got)
	}
	if directive != cobra.ShellCompDirectiveError {
		t.Fatalf("directive = %v, want Error", directive)
	}
}

// TestCompletePortDoesNotScanOnceTheArgumentIsThere: the shell asks again for
// later words, and the <port> word is already chosen — rescanning there would
// spend a machine-wide scan to offer candidates the command cannot accept.
func TestCompletePortDoesNotScanOnceTheArgumentIsThere(t *testing.T) {
	called := stubCompletionScan(t, func() ([]ports.ListeningPort, error) {
		t.Error("completion scanned after the port argument was given")
		return nil, nil
	})

	got, directive := completePort(&cobra.Command{}, []string{"3000"}, "")
	if *called {
		t.Fatal("the scan ran for an already-complete argument list")
	}
	if got != nil || directive != cobra.ShellCompDirectiveNoFileComp {
		t.Fatalf("got %v, %v; want no candidates and NoFileComp", got, directive)
	}
}
