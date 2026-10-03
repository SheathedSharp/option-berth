package ports

import (
	"os"
	"runtime"
	"testing"
	"time"
)

func TestParseStartTime(t *testing.T) {
	if got := parseStartTime("Wed Mar 18 10:30:00 2026"); got != "2026-03-18T10:30:00Z" {
		// ps prints local time; compare only the date/time prefix.
		if len(got) < 19 || got[:19] != "2026-03-18T10:30:00" {
			t.Fatalf("parseStartTime(lstart) = %q", got)
		}
	}
	if got := parseStartTime("2026-09-02T09:12:41+02:00"); got != "2026-09-02T09:12:41+02:00" {
		t.Fatalf("parseStartTime(rfc3339) = %q", got)
	}
	if got := parseStartTime("nonsense"); got != "" {
		t.Fatalf("parseStartTime(garbage) = %q", got)
	}
}

// TestParseProcessTable covers the cached always-on table that carries the
// command line, parent and process start time in one ps call. The command may
// contain any whitespace, so it is taken as the remainder of the line, not as
// fields.
func TestParseProcessTable(t *testing.T) {
	out := ` 1234   1 Wed Mar 18 10:30:00 2026 /usr/bin/node   server.js --port 3000
 567  42 Wed Mar 18 09:00:01 2026 /Applications/Some App.app/Contents/MacOS/Some App
 bad line
 99   1 Wed Mar 18 09:00:01 2026
`
	got := parseProcessTable(out)
	if len(got) != 2 {
		t.Fatalf("parseProcessTable returned %d rows, want 2: %+v", len(got), got)
	}
	if want := "/usr/bin/node   server.js --port 3000"; got[1234].cmd != want {
		t.Errorf("command = %q, want %q", got[1234].cmd, want)
	}
	if got[1234].ppid != 1 {
		t.Errorf("ppid = %d, want 1", got[1234].ppid)
	}
	if got[1234].startedAt == "" {
		t.Errorf("startedAt = %q, want the raw lstart", got[1234].startedAt)
	}
	if want := "/Applications/Some App.app/Contents/MacOS/Some App"; got[567].cmd != want {
		t.Errorf("command = %q, want %q", got[567].cmd, want)
	}
}

// TestEnrichPopulatesStartedAtWithoutStats is the smoke-test regression:
// started_at was null in every state.snapshot row because only EnrichStats
// filled it. Enrich alone must set it, for the process running this test.
func TestEnrichPopulatesStartedAtWithoutStats(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the ps table this exercises is the Unix path")
	}
	pp := []ListeningPort{{Port: 1, PID: os.Getpid(), Process: "option-berth.test"}}
	Enrich(pp) // no EnrichStats: started_at is not gated by --stats

	if pp[0].StartedAt == "" {
		t.Fatal("Enrich left started_at empty for the current process")
	}
	started, err := time.Parse(time.RFC3339, pp[0].StartedAt)
	if err != nil {
		t.Fatalf("started_at %q is not RFC3339: %v", pp[0].StartedAt, err)
	}
	if age := time.Since(started); age < 0 || age > 24*time.Hour {
		t.Errorf("started_at %s is %s away from now; want a plausible start time", pp[0].StartedAt, age)
	}
	if pp[0].Uptime != "" || pp[0].CPUPercent != 0 {
		t.Errorf("Enrich collected stats it was not asked for: uptime=%q cpu=%v", pp[0].Uptime, pp[0].CPUPercent)
	}
}

// TestUptimeIsLocalTimeNotUTC pins the fix for a negative `stats.uptime`: `ps`
// prints lstart in local time with no zone, and parsing it as UTC put the start
// instant a whole UTC offset away — so a process that had been up for fifteen
// seconds in a +02:00 zone reported "-7185s" while `started_at`, which already
// parsed in the local zone, was right.
func TestUptimeIsLocalTimeNotUTC(t *testing.T) {
	for _, zone := range []string{"Europe/Copenhagen", "America/Los_Angeles", "UTC"} {
		loc, err := time.LoadLocation(zone)
		if err != nil {
			t.Skipf("no zone database: %v", err)
		}
		t.Run(zone, func(t *testing.T) {
			prev := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = prev })

			started := time.Date(2026, 9, 6, 10, 30, 0, 0, loc)
			lstart := started.Format("Mon Jan  2 15:04:05 2006")

			if got := computeUptimeAt(lstart, started.Add(15*time.Second)); got != "15s" {
				t.Errorf("uptime 15s after the start = %q, want 15s", got)
			}
			if got := computeUptimeAt(lstart, started.Add(90*time.Minute)); got != "1h30m" {
				t.Errorf("uptime 90m after the start = %q, want 1h30m", got)
			}
			// started_at and uptime read the same instant, so they agree.
			if got := parseStartTime(lstart); got != started.Format(time.RFC3339) {
				t.Errorf("started_at = %q, want %q", got, started.Format(time.RFC3339))
			}
		})
	}
}

// TestUptimeNeverGoesNegative: a clock that steps back, or a start time a
// moment in the future, reads as 0s rather than as a negative duration.
func TestUptimeNeverGoesNegative(t *testing.T) {
	started := time.Date(2026, 9, 6, 10, 30, 0, 0, time.Local)
	lstart := started.Format("Mon Jan  2 15:04:05 2006")
	if got := computeUptimeAt(lstart, started.Add(-2*time.Hour)); got != "0s" {
		t.Fatalf("uptime with a backwards clock = %q, want 0s", got)
	}
}

// TestUptimeOfAnUnparseableStartIsEmpty, so a client can tell "not known" from
// "just started".
func TestUptimeOfAnUnparseableStartIsEmpty(t *testing.T) {
	if got := computeUptimeAt("nonsense", time.Now()); got != "" {
		t.Fatalf("uptime of garbage = %q, want empty", got)
	}
}

// TestParseTasklist: the fallback that names a process when the CIM query does
// not. A row it cannot read costs that row only.
func TestParseTasklist(t *testing.T) {
	out := "\"System Idle Process\",\"0\",\"Services\",\"0\",\"8 K\"\r\n" +
		"\"listener.exe\",\"7276\",\"Console\",\"1\",\"5,120 K\"\r\n" +
		"\"not a row\"\r\n" +
		"\"svchost.exe\",\"nope\",\"Services\",\"0\",\"9,000 K\"\r\n" +
		"\"node.exe\",\"3812\",\"Console\",\"1\",\"42,000 K\"\r\n"

	got := parseTasklist(out)
	want := map[int]string{0: "System Idle Process", 7276: "listener.exe", 3812: "node.exe"}
	if len(got) != len(want) {
		t.Fatalf("parseTasklist = %v, want %v", got, want)
	}
	for pid, name := range want {
		if got[pid] != name {
			t.Errorf("pid %d = %q, want %q", pid, got[pid], name)
		}
	}
}
