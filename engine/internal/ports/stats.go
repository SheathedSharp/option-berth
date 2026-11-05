package ports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseDarwinStats reads `%cpu rss state lstart…`, where lstart is the five
// tokens of "Mon Jan  2 15:04:05 2006".
func parseDarwinStats(row *ListeningPort, columns []string) {
	if len(columns) < 8 {
		return
	}
	setCPU(row, columns[0])
	setMemoryKilobytes(row, columns[1])
	row.State = decodeState(columns[2])

	started := strings.Join(columns[3:], " ")
	row.StartTime = started
	row.Uptime = computeUptime(started)
	if row.StartedAt == "" {
		row.StartedAt = parseStartTime(started)
	}
	// Thread count is not in this call: macOS needs a `ps -M` per pid, and the
	// caller decides whether that fork is worth paying for. The scan tick pays
	// it; the one-second stats tick does not.
}

// parseLinuxStats reads `%cpu rss nlwp state lstart…`.
func parseLinuxStats(row *ListeningPort, columns []string) {
	if len(columns) < 9 {
		return
	}
	setCPU(row, columns[0])
	setMemoryKilobytes(row, columns[1])
	setThreads(row, columns[2])
	row.State = decodeState(columns[3])

	started := strings.Join(columns[4:], " ")
	row.StartTime = started
	row.Uptime = computeUptime(started)
	if row.StartedAt == "" {
		row.StartedAt = parseStartTime(started)
	}
}

// parseWindowsStats reads the CSV fields Get-Process produced: CPU,
// WorkingSet64, ThreadCount, StartTime.
func parseWindowsStats(row *ListeningPort, columns []string) {
	if len(columns) < 4 {
		return
	}
	if value, err := strconv.ParseFloat(strings.TrimSpace(columns[0]), 64); err == nil {
		row.CPUPercent = value
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(columns[1]), 10, 64); err == nil {
		row.MemoryRSS = value // WorkingSet64 is already in bytes
	}
	if value, err := strconv.Atoi(strings.TrimSpace(columns[2])); err == nil {
		row.ThreadCount = value
	}
	row.State = "running"

	started := strings.TrimSpace(columns[3])
	if started == "" {
		return
	}
	row.StartTime = started
	// StartTime is ISO 8601, formatted with .ToString('o') in PowerShell.
	row.Uptime = computeUptime(started)
	if row.StartedAt == "" {
		row.StartedAt = parseStartTime(started)
	}
}

func setCPU(row *ListeningPort, raw string) {
	if value, err := strconv.ParseFloat(raw, 64); err == nil {
		row.CPUPercent = value
	}
}

// setMemoryKilobytes converts ps's kilobyte RSS into bytes.
func setMemoryKilobytes(row *ListeningPort, raw string) {
	if value, err := strconv.ParseInt(raw, 10, 64); err == nil {
		row.MemoryRSS = value * 1024
	}
}

func setThreads(row *ListeningPort, raw string) {
	if value, err := strconv.Atoi(raw); err == nil {
		row.ThreadCount = value
	}
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
