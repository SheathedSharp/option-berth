package ports

import (
	"context"
	"encoding/csv"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// ProcSample is one process's live resource usage, keyed by pid rather than by
// socket. The daemon's stats-only tick reads it for the pids a snapshot already
// names: no port scan, no attribution, no health behind it.
//
// ThreadCount is 0 when the platform could not report it in the same call.
// macOS is the case that matters: counting threads needs an extra `ps -M`
// listing, so it stays on the scan tick and a sample carries 0, which callers
// read as "unchanged", not as "no threads".
type ProcSample struct {
	CPUPercent float64
	MemoryRSS  int64

	// Thread count and the raw start time, as ps printed it.
	ThreadCount int
	StartTime   string

	// Current process state and uptime.
	Uptime string
	State  string

	// RFC3339 start time; "" when the raw value did not parse.
	StartedAt string
}

// SampleProcStats reads cpu, memory, state and uptime for the given pids in
// one `ps` call (one PowerShell call on Windows). A pid the OS no longer knows
// is absent from the result: a process that exited between the listing scan
// and this sample must not report zeroes that read like facts.
func SampleProcStats(pids []int) map[int]ProcSample {
	return SampleProcStatsContext(context.Background(), pids)
}

// SampleProcStatsContext is a cancellable native sampling round. Cancellation
// yields no observations, never zero-valued replacements for existing facts.
func SampleProcStatsContext(ctx context.Context, pids []int) map[int]ProcSample {
	out := make(map[int]ProcSample, len(pids))
	if len(pids) == 0 || ctx.Err() != nil {
		return out
	}
	pidStrs := uniquePIDStrings(pids)
	if len(pidStrs) == 0 {
		return out
	}
	if runtime.GOOS == "windows" {
		return sampleProcStatsWindows(ctx, pidStrs)
	}
	return sampleProcStatsPS(ctx, pidStrs)
}

// uniquePIDStrings drops non-positive and repeated pids, keeping the caller's
// order.
func uniquePIDStrings(pids []int) []string {
	seen := make(map[int]bool, len(pids))
	out := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, strconv.Itoa(pid))
	}
	return out
}

// sampleProcStatsPS is the macOS/Linux sample: one `ps` for every pid at once.
func sampleProcStatsPS(ctx context.Context, pidStrs []string) map[int]ProcSample {
	out := map[int]ProcSample{}

	format := "pid=,%cpu=,rss=,nlwp=,state=,lstart="
	if runtime.GOOS == "darwin" {
		// No nlwp on BSD ps; the extra thread listing stays on the full scan.
		format = "pid=,%cpu=,rss=,state=,lstart="
	}
	sampleCmd, stopSample := boundedContext(ctx, "ps", "-o", format, "-p", strings.Join(pidStrs, ","))
	defer stopSample()
	raw, err := sampleCmd.Output()
	if err != nil || ctx.Err() != nil {
		return out
	}

	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		pid, sample, ok := parsePSSampleLine(line)
		if ok {
			out[pid] = sample
		}
	}
	return out
}

// parsePSSampleLine reads one `ps` row into a pid and its sample.
func parsePSSampleLine(line string) (int, ProcSample, bool) {
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 2 {
		return 0, ProcSample{}, false
	}
	pid, err := strconv.Atoi(fields[0])
	if err != nil {
		return 0, ProcSample{}, false
	}
	return pid, parseSampleFields(pid, fields[1:]), true
}

// parseSampleFields runs the platform's ps-field parser for one pid.
func parseSampleFields(pid int, fields []string) ProcSample {
	scratch := ListeningPort{PID: pid}
	if runtime.GOOS == "darwin" {
		parseDarwinStats(&scratch, fields)
	} else {
		parseLinuxStats(&scratch, fields)
	}
	return sampleOf(scratch)
}

// sampleProcStatsWindows is the same sample from one Get-Process call.
func sampleProcStatsWindows(ctx context.Context, pidStrs []string) map[int]ProcSample {
	out := map[int]ProcSample{}

	psCmd := fmt.Sprintf(
		"Get-Process -Id %s -ErrorAction SilentlyContinue | Select-Object Id,CPU,WorkingSet64,@{N='ThreadCount';E={$_.Threads.Count}},@{N='StartTime';E={$_.StartTime.ToString('o')}} | ConvertTo-Csv -NoTypeInformation",
		strings.Join(pidStrs, ","),
	)
	sampleCmd, stopSample := boundedContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	defer stopSample()
	raw, err := sampleCmd.Output()
	if err != nil || ctx.Err() != nil {
		return out
	}

	r := csv.NewReader(strings.NewReader(strings.TrimSpace(string(raw))))
	r.FieldsPerRecord = -1
	records, err := r.ReadAll()
	if err != nil || ctx.Err() != nil {
		return out
	}
	for index, record := range records {
		if index == 0 {
			continue // header
		}
		if pid, sample, usable := parseWindowsSample(record); usable {
			out[pid] = sample
		}
	}
	return out
}

// parseWindowsSample reads one Get-Process CSV row.
func parseWindowsSample(record []string) (int, ProcSample, bool) {
	if len(record) < 5 {
		return 0, ProcSample{}, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(record[0]))
	if err != nil {
		return 0, ProcSample{}, false
	}
	scratch := ListeningPort{PID: pid}
	parseWindowsStats(&scratch, record[1:])
	return pid, sampleOf(scratch), true
}

// sampleOf lifts the fields the per-OS parsers wrote onto a scratch row.
func sampleOf(row ListeningPort) ProcSample {
	return ProcSample{
		CPUPercent:  row.CPUPercent,
		MemoryRSS:   row.MemoryRSS,
		ThreadCount: row.ThreadCount,
		StartTime:   row.StartTime,
		Uptime:      row.Uptime,
		State:       row.State,
		StartedAt:   row.StartedAt,
	}
}

// MatchesStart is the shared birth-identity rule for full and stats-only
// observations. Missing evidence remains unknown; known contradictory or
// unparsable evidence cannot update a different runtime's resource facts.
func (s ProcSample) MatchesStart(startedAt string) bool {
	if startedAt == "" || s.StartedAt == "" || startedAt == s.StartedAt {
		return true
	}
	before, err := time.Parse(time.RFC3339Nano, startedAt)
	if err != nil {
		return false
	}
	observed, err := time.Parse(time.RFC3339Nano, s.StartedAt)
	return err == nil && before.Equal(observed)
}

// Apply writes only identity-compatible samples onto a listening row. A zero
// thread count means this call did not report one. Existing birth evidence is
// retained, and an absent timestamp is filled without claiming proven identity.
func (sample ProcSample) Apply(row *ListeningPort) {
	if !sample.MatchesStart(row.StartedAt) {
		return
	}
	row.CPUPercent = sample.CPUPercent
	row.MemoryRSS = sample.MemoryRSS
	row.State = sample.State
	row.Uptime = sample.Uptime
	row.StartTime = sample.StartTime
	if sample.ThreadCount > 0 {
		row.ThreadCount = sample.ThreadCount
	}
	if row.StartedAt == "" {
		row.StartedAt = sample.StartedAt
	}
}
