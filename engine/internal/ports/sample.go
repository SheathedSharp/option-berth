package ports

import (
	"context"
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
	if ctx.Err() != nil {
		return out
	}
	args := pidArguments(pids)
	if len(args) == 0 {
		return out
	}
	if runtime.GOOS == "windows" {
		return sampleProcStatsWindows(ctx, args)
	}
	return sampleProcStatsPS(ctx, args)
}

// pidArguments renders the pids one batch command takes. Duplicates and
// non-positive values are dropped; the order is the caller's.
func pidArguments(pids []int) []string {
	unique := uniquePIDs(pids)
	args := make([]string, len(unique))
	for i, pid := range unique {
		args[i] = strconv.Itoa(pid)
	}
	return args
}

// sampleProcStatsPS samples through one `ps` invocation. The columns asked for
// depend on the platform, so the decoder does too: macOS ps has no thread
// column, and the scan tick supplies that count separately.
func sampleProcStatsPS(ctx context.Context, pidArgs []string) map[int]ProcSample {
	out := map[int]ProcSample{}
	sampleCmd, stopSample := boundedContext(ctx, "ps", "-o", psSampleFormat(), "-p", strings.Join(pidArgs, ","))
	defer stopSample()
	raw, err := sampleCmd.Output()
	if err != nil || ctx.Err() != nil {
		return out
	}

	decode := decodeLinuxSample
	if runtime.GOOS == "darwin" {
		decode = decodeDarwinSample
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		fields := strings.Fields(line)
		pid, ok := samplePID(fields)
		if !ok {
			continue
		}
		if sample, ok := decode(fields[1:]); ok {
			out[pid] = sample
		}
	}
	return out
}

// psSampleFormat is the column list one sample asks for, in `ps -o` syntax.
func psSampleFormat() string {
	if runtime.GOOS == "darwin" {
		return "pid=,%cpu=,rss=,state=,lstart="
	}
	return "pid=,%cpu=,rss=,nlwp=,state=,lstart="
}

// samplePID reads the pid ps printed for one row. A row without one is not a
// process this sample was asked about.
func samplePID(fields []string) (int, bool) {
	if len(fields) < 2 {
		return 0, false
	}
	pid, err := strconv.Atoi(fields[0])
	return pid, err == nil && pid > 0
}

// sampleProcStatsWindows samples through one Get-Process call. The output is
// CSV, so it is read through the same named columns as the scan's process
// query: a Select-Object order change must not move a start time into the
// memory field.
func sampleProcStatsWindows(ctx context.Context, pidArgs []string) map[int]ProcSample {
	out := map[int]ProcSample{}
	psCmd := fmt.Sprintf(
		"Get-Process -Id %s -ErrorAction SilentlyContinue | Select-Object Id,CPU,WorkingSet64,@{N='ThreadCount';E={$_.Threads.Count}},@{N='StartTime';E={$_.StartTime.ToString('o')}} | ConvertTo-Csv -NoTypeInformation",
		strings.Join(pidArgs, ","),
	)
	sampleCmd, stopSample := boundedContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	defer stopSample()
	raw, err := sampleCmd.Output()
	if err != nil || ctx.Err() != nil {
		return out
	}

	table := parseCSVTable(string(raw))
	for _, row := range table.rows {
		pid, sample, ok := decodeWindowsSample(table, row)
		if ok {
			out[pid] = sample
		}
	}
	return out
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
