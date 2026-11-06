package ports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// decodeDarwinSample reads one macOS `ps` row: %cpu, rss, state and the five
// tokens of lstart. Threads are not in this call — `ps -M` supplies that count
// on the scan tick — so it stays zero and callers read zero as unchanged.
func decodeDarwinSample(columns []string) (ProcSample, bool) {
	const width = 3 + lstartFields
	if len(columns) < width {
		return ProcSample{}, false
	}
	sample := ProcSample{
		CPUPercent: percent(columns[0]),
		MemoryRSS:  kilobytes(columns[1]),
		State:      decodeState(columns[2]),
	}
	return withStart(sample, strings.Join(columns[3:width], " ")), true
}

// decodeLinuxSample reads one Linux `ps` row, where nlwp follows memory.
func decodeLinuxSample(columns []string) (ProcSample, bool) {
	const width = 4 + lstartFields
	if len(columns) < width {
		return ProcSample{}, false
	}
	sample := ProcSample{
		CPUPercent:  percent(columns[0]),
		MemoryRSS:   kilobytes(columns[1]),
		ThreadCount: integer(columns[2]),
		State:       decodeState(columns[3]),
	}
	return withStart(sample, strings.Join(columns[4:width], " ")), true
}

// decodeWindowsSample reads one Get-Process row through its named columns.
// A row that names a pid but carries none of the numbers is not a sample:
// writing zeroes would read as facts about a process the query could not see.
func decodeWindowsSample(table csvTable, row []string) (int, ProcSample, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(table.value(row, "id")))
	if err != nil || pid <= 0 {
		return 0, ProcSample{}, false
	}
	sample := ProcSample{
		CPUPercent:  percent(strings.TrimSpace(table.value(row, "cpu"))),
		MemoryRSS:   byteCount(strings.TrimSpace(table.value(row, "workingset64"))),
		ThreadCount: integer(strings.TrimSpace(table.value(row, "threadcount"))),
		State:       "running",
	}
	if started := strings.TrimSpace(table.value(row, "starttime")); started != "" {
		sample = withStart(sample, started)
	}
	if sample.CPUPercent == 0 && sample.MemoryRSS == 0 && sample.ThreadCount == 0 && sample.StartTime == "" {
		return 0, ProcSample{}, false
	}
	return pid, sample, true
}

// withStart fills the raw start text and the two forms derived from it. An
// unparsable value still survives as text: started_at is only published when
// the instant parses, and uptime is only meaningful when it does.
func withStart(sample ProcSample, started string) ProcSample {
	started = strings.TrimSpace(started)
	if started == "" {
		return sample
	}
	sample.StartTime = started
	sample.Uptime = computeUptime(started)
	sample.StartedAt = parseStartTime(started)
	return sample
}

// percent reads ps's %cpu column. An unreadable value is unknown, not evidence
// that the process used no CPU.
func percent(raw string) float64 {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0
	}
	return value
}

// kilobytes converts ps's RSS column into bytes.
func kilobytes(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value * 1024
}

// byteCount reads a count that is already in bytes, as Get-Process reports.
func byteCount(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// integer reads a plain count such as nlwp or a thread count.
func integer(raw string) int {
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0
	}
	return value
}

// countThreadsDarwin counts each pid's threads from one `ps -M`.
//
// macOS has no batched thread-count column (`nlwp` is a Linux extension), so
// this used to be a fork per pid — eighteen inside the scan lock on the
// machine it was measured on. `ps -M` takes a pid list and prints one line per
// process followed by one per thread, which is all the count needs.
func countThreadsDarwin(pids []int) map[int]int {
	return countThreadsDarwinContext(context.Background(), pids)
}

func countThreadsDarwinContext(ctx context.Context, pids []int) map[int]int {
	counts := map[int]int{}
	if len(pids) == 0 || ctx.Err() != nil {
		return counts
	}
	wanted := make(map[int]bool, len(pids))
	pidArgs := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 || wanted[pid] {
			continue
		}
		wanted[pid] = true
		pidArgs = append(pidArgs, strconv.Itoa(pid))
	}
	threadsCmd, stopThreads := boundedContext(ctx, "ps", "-M", "-p", strings.Join(pidArgs, ","))
	defer stopThreads()
	out, err := threadsCmd.Output()
	if err != nil || ctx.Err() != nil {
		return counts
	}
	return countPSThreads(string(out), wanted)
}

// countPSThreads counts the `ps -M` lines belonging to each requested pid.
//
// A process line is "USER PID TT %CPU …" and a thread line is "PID %CPU …"
// with user and tty blank, so the pid is in the first or the second column.
// Both count, matching the single-pid version (every line but the header).
func countPSThreads(out string, wanted map[int]bool) map[int]int {
	counts := map[int]int{}
	owner := 0
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	for index, line := range lines {
		columns := strings.Fields(line)
		if len(columns) == 0 {
			continue
		}
		if index == 0 && strings.EqualFold(columns[0], "USER") {
			continue // header
		}
		switch {
		case len(columns) > 1 && requestedPID(wanted, columns[1]):
			owner, _ = strconv.Atoi(columns[1])
		case requestedPID(wanted, columns[0]):
			owner, _ = strconv.Atoi(columns[0])
		case owner == 0:
			continue
		}
		counts[owner]++
	}
	return counts
}

// requestedPID reports whether a column is one of the pids asked about.
func requestedPID(wanted map[int]bool, column string) bool {
	pid, err := strconv.Atoi(column)
	return err == nil && wanted[pid]
}

// stateWords maps ps's single-character state to a readable word.
var stateWords = map[byte]string{
	'R': "running",
	'S': "sleeping",
	'D': "disk sleep",
	'T': "stopped",
	'Z': "zombie",
	'I': "idle",
	'U': "uninterruptible",
}

// decodeState turns ps's single-character state into a readable word.
func decodeState(raw string) string {
	if raw == "" {
		return ""
	}
	if word, known := stateWords[raw[0]]; known {
		return word
	}
	return strings.ToLower(raw)
}

// startTimeLayouts are the shapes a start time arrives in: `ps -o lstart`
// (with one or two spaces before a single-digit day) and the ISO-8601 instant
// PowerShell reports.
var startTimeLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"Mon Jan 2 15:04:05 2006",
	"Mon Jan  2 15:04:05 2006",
}

// startTime parses a raw process start time. `ps` prints local time with no
// zone, so the zoneless layouts are read in the local zone: reading them as UTC
// moved the instant by a whole offset, which is how uptime once came back as
// "-7185s" while started_at (already local) was right. One parser now serves
// both fields, so they cannot disagree.
func startTime(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	for _, layout := range startTimeLayouts {
		if instant, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return instant, true
		}
	}
	return time.Time{}, false
}

// parseStartTime converts a raw start time to RFC3339, or "" when it cannot be
// parsed. The contract publishes started_at as RFC3339.
func parseStartTime(value string) string {
	instant, ok := startTime(value)
	if !ok {
		return ""
	}
	return instant.Format(time.RFC3339)
}

// computeUptime is how long ago a process started, for a human.
func computeUptime(started string) string { return computeUptimeAt(started, time.Now()) }

// computeUptimeAt is computeUptime against an explicit clock, so tests do not
// race real time.
func computeUptimeAt(started string, now time.Time) string {
	instant, ok := startTime(started)
	if !ok {
		return ""
	}
	return formatDuration(now.Sub(instant))
}

// formatDuration renders a concise duration. A negative one — a clock that
// stepped back, or a future start — reads as 0s, never a negative uptime.
func formatDuration(duration time.Duration) string {
	if duration < 0 {
		duration = 0
	}
	if duration < time.Minute {
		return fmt.Sprintf("%ds", int(duration.Seconds()))
	}
	if duration < time.Hour {
		return fmt.Sprintf("%dm%ds", int(duration.Minutes()), int(duration.Seconds())%60)
	}
	if duration < 24*time.Hour {
		return fmt.Sprintf("%dh%dm", int(duration.Hours()), int(duration.Minutes())%60)
	}
	return fmt.Sprintf("%dd%dh", int(duration.Hours())/24, int(duration.Hours())%24)
}

// FormatBytes renders a human-readable byte size.
func FormatBytes(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1fG", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%dB", bytes)
	}
}
