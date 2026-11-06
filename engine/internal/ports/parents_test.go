package ports

import (
	"os"
	"runtime"
	"testing"
)

// The Linux shape: field 2 of /proc/<pid>/stat is the executable name in
// parentheses and may itself contain spaces and parentheses, so the parent is
// only found after the last ')'.
func TestParseProcStatPPID(t *testing.T) {
	cases := []struct {
		label string
		stat  string
		want  int
		ok    bool
	}{
		{
			label: "plain process",
			stat:  "5151 (listener) S 5100 5100 5100 0 -1 4194304 285 0 0 0 0 0 0 0 20 0 5 0 91234 0 0\n",
			want:  5100,
			ok:    true,
		},
		{
			label: "comm with spaces and parentheses",
			stat:  "812 (next-server (v) S 799 799 640 0 -1 4194560 9021 0 3 0 41 12 0 0 20 0 11 0 5512",
			want:  799,
			ok:    true,
		},
		{
			label: "comm that looks like a stat line",
			stat:  "77 (evil) 1 (x) R 5 5 5 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 42",
			want:  5,
			ok:    true,
		},
		{
			label: "init, whose parent is the kernel",
			stat:  "1 (systemd) S 0 1 1 0 -1 4194560 30112 0 0 0 90 250 0 0 20 0 1 0 12",
			want:  0,
			ok:    true,
		},
		{
			label: "truncated read",
			stat:  "5151 (listener) S",
			ok:    false,
		},
		{
			label: "no comm",
			stat:  "not a stat line",
			ok:    false,
		},
		{
			label: "non-numeric parent",
			stat:  "5151 (listener) S parent 5100 5100",
			ok:    false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			parent, ok := parseProcStatPPID(tc.stat)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && parent != tc.want {
				t.Errorf("ppid = %d, want %d", parent, tc.want)
			}
		})
	}
}

// End to end: whichever implementation the platform uses must include this
// test process and the parent it actually has.
func TestParentTableFindsThisProcess(t *testing.T) {
	table := ParentTable()
	if len(table) == 0 {
		t.Skip("no process table on this machine")
	}
	parent, found := table[os.Getpid()]
	if !found {
		t.Fatalf("the table has no entry for this process (pid %d)", os.Getpid())
	}
	if parent != os.Getppid() {
		t.Errorf("ppid = %d, want %d", parent, os.Getppid())
	}
	if runtime.GOOS == "linux" {
		if native := nativeParentTable(); len(native) == 0 {
			t.Error("/proc yielded no parent table on Linux")
		}
	}
}

// The run registry's identity check reads start times from the same scan table
// the ancestry walks use. A pid without a start time is unknown, never proof.
func TestProcessStartReadsTheRememberedTable(t *testing.T) {
	restore := scanParentTable.Load()
	t.Cleanup(func() { scanParentTable.Store(restore) })

	rememberProcessFacts(map[int]processFacts{
		5151: {ppid: 1, startedAt: "Thu Apr  9 08:15:00 2026"},
		5252: {ppid: 1}, // no start time recorded
	})

	started, ok := ProcessStart(5151)
	if !ok {
		t.Fatal("ProcessStart found no answer for a remembered pid")
	}
	if stamp := started.Format("2006-01-02T15:04:05"); stamp != "2026-04-09T08:15:00" {
		t.Fatalf("ProcessStart = %v, want 2026-04-09T08:15:00", started)
	}
	if _, ok := ProcessStart(5252); ok {
		t.Fatal("a pid without a start time reported one")
	}
	if _, ok := ProcessStart(999999); ok {
		t.Fatal("an unknown pid reported a start time")
	}
}
