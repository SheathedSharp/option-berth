package ports

import (
	"maps"
	"os"
	"runtime"
	"testing"
	"time"
)

// The Unix process table reaches the engine as one `ps` call, so these tests
// describe what the parser has to survive: a command line full of spaces, rows
// that are not processes, and a start time printed in the host's zone without
// saying which zone that is.

func TestParseStartTime(t *testing.T) {
	// lstart carries no zone, so only the date and time prefix is comparable.
	if got := parseStartTime("Tue Nov  3 08:15:00 2026"); len(got) < 19 || got[:19] != "2026-11-03T08:15:00" {
		t.Fatalf("lstart became %q", got)
	}
	if got := parseStartTime("2026-10-01T12:00:00Z"); got != "2026-10-01T12:00:00Z" {
		t.Fatalf("an RFC3339 timestamp became %q", got)
	}
	if got := parseStartTime("not a time"); got != "" {
		t.Fatalf("unparseable input became %q, want empty", got)
	}
}

func TestParseProcessTableKeepsCommandsWhole(t *testing.T) {
	out := ` 4321   1 Tue Nov  3 08:15:00 2026 /usr/bin/python3  worker.py --queue default
 87  4321 Tue Nov  3 08:16:02 2026 /Applications/Example App.app/Contents/MacOS/Example App
 this line is not a process row
 90   1 Tue Nov  3 08:17:00 2026
`
	got := parseProcessTable(out)
	if len(got) != 2 {
		t.Fatalf("parseProcessTable returned %d rows, want 2: %+v", len(got), got)
	}
	if want := "/usr/bin/python3  worker.py --queue default"; got[4321].command != want {
		t.Errorf("command = %q, want %q", got[4321].command, want)
	}
	if got[4321].ppid != 1 {
		t.Errorf("ppid = %d, want 1", got[4321].ppid)
	}
	if got[4321].startedAt == "" {
		t.Error("the raw lstart was dropped")
	}
	if want := "/Applications/Example App.app/Contents/MacOS/Example App"; got[87].command != want {
		t.Errorf("command = %q, want %q", got[87].command, want)
	}
}

func TestEnrichSetsStartedAtWithoutStats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps table this exercises belongs to the Unix path")
	}
	rows := []ListeningPort{{Port: 1, PID: os.Getpid(), Process: "option-berth.test"}}
	Enrich(rows) // EnrichStats is not called: started_at must not depend on stats

	if rows[0].StartedAt == "" {
		t.Fatal("Enrich left started_at empty for the current process")
	}
	started, err := time.Parse(time.RFC3339, rows[0].StartedAt)
	if err != nil {
		t.Fatalf("started_at %q is not RFC3339: %v", rows[0].StartedAt, err)
	}
	if age := time.Since(started); age < 0 || age > 24*time.Hour {
		t.Errorf("started_at %s is %s away from now; want a plausible start time", rows[0].StartedAt, age)
	}
	if rows[0].Uptime != "" || rows[0].CPUPercent != 0 {
		t.Errorf("Enrich collected stats it was not asked for: uptime=%q cpu=%v", rows[0].Uptime, rows[0].CPUPercent)
	}
}

func TestUptimeReadsLstartInTheHostZone(t *testing.T) {
	for _, zone := range []string{"Europe/Berlin", "America/Denver", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone database: %v", err)
		}
		t.Run(zone, func(t *testing.T) {
			previous := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = previous })

			start := time.Date(2026, 11, 3, 8, 15, 0, 0, loc)
			lstart := start.Format("Mon Jan  2 15:04:05 2006")

			// Reading lstart as UTC would put the start a whole offset away and
			// turn a just-started process into a negative uptime.
			for _, tc := range []struct {
				elapsed time.Duration
				want    string
			}{
				{15 * time.Second, "15s"},
				{90 * time.Minute, "1h30m"},
			} {
				if got := computeUptimeAt(lstart, start.Add(tc.elapsed)); got != tc.want {
					t.Errorf("uptime %s after the start = %q, want %q", tc.elapsed, got, tc.want)
				}
			}
			// started_at and uptime must describe the same instant.
			if got := parseStartTime(lstart); got != start.Format(time.RFC3339) {
				t.Errorf("started_at = %q, want %q", got, start.Format(time.RFC3339))
			}
		})
	}
}

func TestUptimeFloorsAtZeroAndStaysEmptyWhenUnknown(t *testing.T) {
	start := time.Date(2026, 11, 3, 8, 15, 0, 0, time.Local)
	lstart := start.Format("Mon Jan  2 15:04:05 2006")
	if got := computeUptimeAt(lstart, start.Add(-2*time.Hour)); got != "0s" {
		t.Fatalf("uptime with a backwards clock = %q, want 0s", got)
	}
	if got := computeUptimeAt("not a time", time.Now()); got != "" {
		t.Fatalf("uptime of an unparseable start = %q, want empty", got)
	}
}

// parseTasklist is the fallback that names a process when the CIM query cannot:
// one unreadable row costs only that row.
func TestParseTasklistSkipsUnreadableRows(t *testing.T) {
	out := "\"Worker.exe\",\"4242\",\"Console\",\"1\",\"12,000 K\"\r\n" +
		"\"System Idle Process\",\"0\",\"Services\",\"0\",\"8 K\"\r\n" +
		"\"broken row\"\r\n" +
		"\"BadPid.exe\",\"not-a-pid\",\"Console\",\"1\",\"1 K\"\r\n"

	got := parseTasklist(out)
	want := map[int]string{4242: "Worker.exe", 0: "System Idle Process"}
	if !maps.Equal(got, want) {
		t.Fatalf("parseTasklist = %v, want %v", got, want)
	}
}
