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
// socket. It is what the daemon's stats-only tick reads for the pids a
// snapshot already names: no port scan behind it, no attribution, no health.
//
// ThreadCount is 0 when the platform could not report it in the same call.
// macOS is the case that matters: counting threads needs an additional `ps -M`
// listing, so it stays on the scan tick and a sample carries 0, which callers
// read as "unchanged" rather than as "no threads".
type ProcSample struct {
	CPUPercent  float64
	MemoryRSS   int64
	ThreadCount int
	StartTime   string // raw, as ps printed it
	Uptime      string
	State       string
	StartedAt   string // RFC3339, "" when the start time did not parse
}

// SampleProcStats reads cpu, memory, state and uptime for the given pids in a
// single `ps` call (one PowerShell call on Windows). A pid the OS no longer
// knows about is simply absent from the result: a process that exited between
// the scan that listed it and this sample must not report zeroes that read
// like facts.
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
	pidStrs := make([]string, 0, len(pids))
	seen := make(map[int]bool, len(pids))
	for _, pid := range pids {
		if pid <= 0 || seen[pid] {
			continue
		}
		seen[pid] = true
		pidStrs = append(pidStrs, strconv.Itoa(pid))
	}
	if len(pidStrs) == 0 {
		return out
	}
	if runtime.GOOS == "windows" {
		return sampleProcStatsWindows(ctx, pidStrs)
	}
	return sampleProcStatsPS(ctx, pidStrs)
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
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		var scratch ListeningPort
		scratch.PID = pid
		if runtime.GOOS == "darwin" {
			parseDarwinStats(&scratch, fields[1:])
		} else {
			parseLinuxStats(&scratch, fields[1:])
		}
		out[pid] = sampleOf(scratch)
	}
	return out
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
	for i, record := range records {
		if i == 0 || len(record) < 5 {
			continue // header, or a row Get-Process could not fill in
		}
		pid, err := strconv.Atoi(strings.TrimSpace(record[0]))
		if err != nil {
			continue
		}
		var scratch ListeningPort
		scratch.PID = pid
		parseWindowsStats(&scratch, record[1:])
		out[pid] = sampleOf(scratch)
	}
	return out
}

// sampleOf lifts the fields the per-OS parsers wrote onto a scratch row.
func sampleOf(p ListeningPort) ProcSample {
	return ProcSample{
		CPUPercent:  p.CPUPercent,
		MemoryRSS:   p.MemoryRSS,
		ThreadCount: p.ThreadCount,
		StartTime:   p.StartTime,
		Uptime:      p.Uptime,
		State:       p.State,
		StartedAt:   p.StartedAt,
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
func (s ProcSample) Apply(p *ListeningPort) {
	if !s.MatchesStart(p.StartedAt) {
		return
	}
	p.CPUPercent = s.CPUPercent
	p.MemoryRSS = s.MemoryRSS
	p.State = s.State
	p.Uptime = s.Uptime
	p.StartTime = s.StartTime
	if s.ThreadCount > 0 {
		p.ThreadCount = s.ThreadCount
	}
	if p.StartedAt == "" {
		p.StartedAt = s.StartedAt
	}
}
