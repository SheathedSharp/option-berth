package ports

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseStartTime(t *testing.T) {
	got := parseStartTime("Thu Apr  9 08:15:00 2026")
	if got != "2026-04-09T08:15:00Z" {
		// ps prints local time; compare only the date/time prefix.
		if len(got) < 19 || got[:19] != "2026-04-09T08:15:00" {
			t.Fatalf("parseStartTime(lstart) = %q", got)
		}
	}
	if got := parseStartTime("2026-10-03T11:22:33+02:00"); got != "2026-10-03T11:22:33+02:00" {
		t.Fatalf("parseStartTime(rfc3339) = %q", got)
	}
	if got := parseStartTime("not a time"); got != "" {
		t.Fatalf("parseStartTime(garbage) = %q", got)
	}
}

// The always-on table carries command, parent and start time in one ps call.
// A command contains arbitrary whitespace, so it is the remainder of the line,
// not a set of fields.
func TestParseProcessTable(t *testing.T) {
	out := ` 4321   1 Thu Apr  9 08:15:00 2026 /usr/local/bin/node server.js --port 4321
 765  21 Thu Apr  9 07:05:01 2026 /Applications/Another App.app/Contents/MacOS/Another App
 garbage row
 88   1 Thu Apr  9 07:05:01 2026
`
	table := parseProcessTable(out)
	if len(table) != 2 {
		t.Fatalf("parseProcessTable returned %d rows, want 2: %+v", len(table), table)
	}
	if want := "/usr/local/bin/node server.js --port 4321"; table[4321].cmd != want {
		t.Errorf("command = %q, want %q", table[4321].cmd, want)
	}
	if table[4321].ppid != 1 {
		t.Errorf("ppid = %d, want 1", table[4321].ppid)
	}
	if table[4321].startedAt == "" {
		t.Errorf("startedAt = %q, want the raw lstart", table[4321].startedAt)
	}
	if want := "/Applications/Another App.app/Contents/MacOS/Another App"; table[765].cmd != want {
		t.Errorf("command = %q, want %q", table[765].cmd, want)
	}
}

// started_at is not a stat: Enrich alone must fill it, for the process running
// this test.
func TestEnrichFillsStartedAtWithoutStats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps table this exercises is the Unix path")
	}
	rows := []ListeningPort{{Port: 1, PID: os.Getpid(), Process: "option-berth.test"}}
	Enrich(rows) // no EnrichStats

	row := rows[0]
	if row.StartedAt == "" {
		t.Fatal("Enrich left started_at empty for the current process")
	}
	started, err := time.Parse(time.RFC3339, row.StartedAt)
	if err != nil {
		t.Fatalf("started_at %q is not RFC3339: %v", row.StartedAt, err)
	}
	if age := time.Since(started); age < 0 || age > 24*time.Hour {
		t.Errorf("started_at %s is %s from now; want a plausible start", row.StartedAt, age)
	}
	if row.Uptime != "" || row.CPUPercent != 0 {
		t.Errorf("Enrich collected stats it was not asked for: uptime=%q cpu=%v",
			row.Uptime, row.CPUPercent)
	}
}

// ps prints lstart in local time with no zone. Reading it as UTC moved the
// start by a whole offset, which once produced a negative stats.uptime.
func TestUptimeUsesLocalTimeNotUTC(t *testing.T) {
	for _, zoneName := range []string{"Europe/Copenhagen", "America/Los_Angeles", "UTC"} {
		loc, err := time.LoadLocation(zoneName)
		if err != nil {
			t.Skipf("no zone database: %v", err)
		}
		t.Run(zoneName, func(t *testing.T) {
			previous := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = previous })

			started := time.Date(2026, 10, 3, 10, 30, 0, 0, loc)
			lstart := started.Format("Mon Jan  2 15:04:05 2006")

			if got := computeUptimeAt(lstart, started.Add(15*time.Second)); got != "15s" {
				t.Errorf("uptime after 15s = %q, want 15s", got)
			}
			if got := computeUptimeAt(lstart, started.Add(90*time.Minute)); got != "1h30m" {
				t.Errorf("uptime after 90m = %q, want 1h30m", got)
			}
			if got := parseStartTime(lstart); got != started.Format(time.RFC3339) {
				t.Errorf("started_at = %q, want %q", got, started.Format(time.RFC3339))
			}
		})
	}
}

// A clock that steps back, or a future start, reads as 0s, never negative.
func TestUptimeNeverGoesNegative(t *testing.T) {
	started := time.Date(2026, 10, 3, 10, 30, 0, 0, time.Local)
	lstart := started.Format("Mon Jan  2 15:04:05 2006")
	if got := computeUptimeAt(lstart, started.Add(-2*time.Hour)); got != "0s" {
		t.Fatalf("uptime with a backwards clock = %q, want 0s", got)
	}
}

// "Not known" is distinct from "just started".
func TestUptimeOfAnUnparseableStartIsEmpty(t *testing.T) {
	if got := computeUptimeAt("not a time", time.Now()); got != "" {
		t.Fatalf("uptime of garbage = %q, want empty", got)
	}
}

// The tasklist fallback names processes the CIM query did not; a row it cannot
// read costs that row only.
func TestParseTasklist(t *testing.T) {
	out := "\"Idle\",\"0\",\"Services\",\"0\",\"8 K\"\r\n" +
		"\"daemon.exe\",\"8123\",\"Console\",\"1\",\"5,120 K\"\r\n" +
		"\"not a row\"\r\n" +
		"\"svc.exe\",\"nope\",\"Services\",\"0\",\"9,000 K\"\r\n" +
		"\"worker.exe\",\"9456\",\"Console\",\"1\",\"42,000 K\"\r\n"

	images := parseTasklist(out)
	want := map[int]string{0: "Idle", 8123: "daemon.exe", 9456: "worker.exe"}
	if len(images) != len(want) {
		t.Fatalf("parseTasklist = %v, want %v", images, want)
	}
	for pid, image := range want {
		if images[pid] != image {
			t.Errorf("pid %d = %q, want %q", pid, images[pid], image)
		}
	}
}
