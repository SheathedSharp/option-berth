package ports

import (
	"strings"
	"testing"
)

// Windows has no /proc and no `ps -A`, so the parents ride along on the CIM
// query the scan already makes. The CSV parsing and the table it feeds are
// ordinary Go, so they are tested everywhere.

// cimSample mirrors the output of `Get-CimInstance Win32_Process … | Select-Object
// ProcessId,ParentProcessId,@{N='StartedAt';…},CommandLine | ConvertTo-Csv
// -NoTypeInformation`: a normal process, a protected one with an empty
// CommandLine, and a command line carrying quotes, spaces and commas.
const cimSample = "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n" +
	"\"2222\",\"1200\",\"2026-10-03T09:00:00.0000000+02:00\",\"\"\"C:\\work\\berth-itest\\listener.exe\"\" 59028\"\r\n" +
	"\"2223\",\"1200\",\"2026-10-03T09:00:01.0000000+02:00\",\"\"\r\n" +
	"\"777\",\"512\",\"2026-10-03T08:59:00.0000000+02:00\",\"C:\\Windows\\system32\\svchost.exe -k RPCSS -p, -s RpcEptMapper\"\r\n"

func TestParseCIMProcessesReadsACapturedSample(t *testing.T) {
	rows := parseCIMProcesses(cimSample)
	if len(rows) != 3 {
		t.Fatalf("parseCIMProcesses returned %d rows: %+v", len(rows), rows)
	}
	want := `"C:\work\berth-itest\listener.exe" 59028`
	if rows[2222].command != want {
		t.Errorf("command = %q, want %q", rows[2222].command, want)
	}
	if rows[2222].ppid != 1200 {
		t.Errorf("ppid = %d, want 1200", rows[2222].ppid)
	}
	if rows[2222].startedAt == "" {
		t.Error("startedAt was dropped")
	}
	// A protected process withholds its command line but still reports a
	// parent; dropping the row would cost the ancestry walk that parent.
	if row, found := rows[2223]; !found || row.ppid != 1200 || row.command != "" {
		t.Errorf("the command-less row = %+v, found=%v; want ppid 1200", row, found)
	}
	// Commas inside a quoted command line are not column separators.
	if !strings.Contains(rows[777].command, "-k RPCSS -p, -s RpcEptMapper") {
		t.Errorf("svchost command = %q, want its commas", rows[777].command)
	}
}

// The parser must not depend on the order Select-Object emits; a positional
// parser read a parent pid as a start time as soon as the query grew a column.
func TestParseCIMProcessesFindsColumnsByName(t *testing.T) {
	reordered := "\"CommandLine\",\"StartedAt\",\"ProcessId\",\"ParentProcessId\"\r\n" +
		"\"node worker.js\",\"2026-10-03T09:00:00.0000000+02:00\",\"2222\",\"1200\"\r\n"
	rows := parseCIMProcesses(reordered)
	if len(rows) != 1 {
		t.Fatalf("parseCIMProcesses returned %d rows: %+v", len(rows), rows)
	}
	if rows[2222].command != "node worker.js" || rows[2222].ppid != 1200 {
		t.Errorf("row = %+v, want node worker.js with ppid 1200", rows[2222])
	}
}

// One unreadable row must not cost every other process its identity, which is
// what an all-or-nothing ReadAll did.
func TestParseCIMProcessesSurvivesOneBadRow(t *testing.T) {
	withGarbage := "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n" +
		"\"2222\",\"1200\",\"\",\"node worker.js\"\r\n" +
		"\"nope\",\"x\",\"\",\"skipped: unparsable pid\"\r\n" +
		"\"2224\",\"0\",\"\",\"\"\r\n" +
		"\"2225\",\"1200\",\"\",\"python -m http.server\"\r\n"
	rows := parseCIMProcesses(withGarbage)
	if _, found := rows[2222]; !found {
		t.Error("the row before the bad one was lost")
	}
	if _, found := rows[2225]; !found {
		t.Error("the row after the bad one was lost")
	}
	if _, found := rows[2224]; found {
		t.Error("a row with neither command nor parent was kept")
	}
}

func TestParseCIMProcessesHandlesNoOutput(t *testing.T) {
	for _, input := range []string{"", "   \r\n", "\"ProcessId\",\"ParentProcessId\",\"StartedAt\",\"CommandLine\"\r\n"} {
		if rows := parseCIMProcesses(input); len(rows) != 0 {
			t.Errorf("parseCIMProcesses(%q) = %v, want empty", input, rows)
		}
	}
	if rows := parseCIMProcesses("\"Name\",\"Handle\"\r\n\"node\",\"2222\"\r\n"); len(rows) != 0 {
		t.Errorf("an unknown header produced %v, want empty", rows)
	}
}

func TestRememberScanParentsFeedsTheTable(t *testing.T) {
	restore := scanParentTable.Load()
	t.Cleanup(func() { scanParentTable.Store(restore) })
	scanParentTable.Store(nil)

	if parents := scanParents(); len(parents) != 0 {
		t.Fatalf("scanParents before any scan = %v, want empty", parents)
	}

	rememberScanParents(map[int]procInfo{
		2222: {command: "node", ppid: 1200},
		2223: {command: "python", ppid: 1200},
		2224: {command: "orphan"}, // no parent reported: nothing to record
	})
	parents := scanParents()
	if len(parents) != 2 || parents[2222] != 1200 || parents[2223] != 1200 {
		t.Fatalf("scanParents = %v, want {2222:1200, 2223:1200}", parents)
	}

	// The caller gets a copy: mutating it must not poison the next reader.
	parents[2222] = 9999
	if again := scanParents(); again[2222] != 1200 {
		t.Errorf("the table was aliased: %v", again)
	}

	// A scan that learned nothing leaves the last good table in place.
	rememberScanParents(map[int]procInfo{})
	if again := scanParents(); len(again) != 2 {
		t.Errorf("an empty scan cleared the table: %v", again)
	}
}
