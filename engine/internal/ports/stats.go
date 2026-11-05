package ports

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// statLayout says where each measurement sits in the field list the platform's
// process listing produces. Every platform reports the same facts, so there is
// one parser and three layouts rather than three parsers that drift apart.
type statLayout struct {
	minFields int
	cpuField  int
	rssField  int
	rssScale  int64 // bytes per reported unit: ps counts KB, PowerShell bytes
	// threadField is -1 where the listing does not carry a thread count and the
	// caller has to pay a separate fork for it (macOS).
	threadField int
	// stateField is -1 where no state column exists; impliedState is what the
	// platform's answer means then.
	stateField   int
	impliedState string
	// startField is the first field of the start time, and startJoins says
	// whether that time spans the remaining fields (`ps` prints it with spaces)
	// or is a single token (PowerShell prints ISO 8601).
	startField int
	startJoins bool
}

var (
	// `ps -o %cpu=,rss=,state=,lstart=` on macOS. The thread count is not in
	// this listing: macOS needs a separate `ps -M`, so the caller decides whether
	// that fork is worth paying for.
	darwinStats = statLayout{
		minFields: 8, cpuField: 0, rssField: 1, rssScale: 1024,
		threadField: -1, stateField: 2,
		startField: 3, startJoins: true,
	}
	// The same listing with `nlwp` folded in: `%cpu rss nlwp state lstart...`.
	linuxStats = statLayout{
		minFields: 9, cpuField: 0, rssField: 1, rssScale: 1024,
		threadField: 2, stateField: 3,
		startField: 4, startJoins: true,
	}
	// PowerShell's Get-Process select: CPU, WorkingSet64, ThreadCount,
	// StartTime. There is no state column, and a running process is the only
	// thing Get-Process lists here.
	windowsStats = statLayout{
		minFields: 4, cpuField: 0, rssField: 1, rssScale: 1,
		threadField: 2, stateField: -1, impliedState: "running",
		startField: 3,
	}
)

// parseStats fills in the per-process measurements a listing row carries.
func parseStats(p *ListeningPort, fields []string, layout statLayout) {
	if len(fields) < layout.minFields {
		return
	}
	if value, err := strconv.ParseFloat(strings.TrimSpace(fields[layout.cpuField]), 64); err == nil {
		p.CPUPercent = value
	}
	if value, err := strconv.ParseInt(strings.TrimSpace(fields[layout.rssField]), 10, 64); err == nil {
		p.MemoryRSS = value * layout.rssScale
	}
	if layout.threadField >= 0 {
		if value, err := strconv.Atoi(strings.TrimSpace(fields[layout.threadField])); err == nil {
			p.ThreadCount = value
		}
	}
	if layout.stateField >= 0 {
		p.State = decodeState(fields[layout.stateField])
	} else {
		p.State = layout.impliedState
	}

	start := strings.TrimSpace(fields[layout.startField])
	if layout.startJoins {
		start = strings.Join(fields[layout.startField:], " ")
	}
	if start == "" {
		return
	}
	p.StartTime = start
	p.Uptime = computeUptime(start)
	if p.StartedAt == "" {
		p.StartedAt = parseStartTime(start)
	}
}

// The three entry points the per-platform samplers call. They exist so the
// layout stays a detail of this file.
func parseDarwinStats(p *ListeningPort, fields []string)  { parseStats(p, fields, darwinStats) }
func parseLinuxStats(p *ListeningPort, fields []string)   { parseStats(p, fields, linuxStats) }
func parseWindowsStats(p *ListeningPort, fields []string) { parseStats(p, fields, windowsStats) }

// countThreadsDarwin counts each pid's threads from a single `ps -M`.
//
// macOS has no batched thread-count column (`nlwp` is a Linux ps extension), so
// this used to be one fork per pid — eighteen of them on the machine this was
// measured on, inside the lock that serializes a scan against the stats tick.
// `ps -M` accepts a pid list and prints one line per process followed by one per
// thread, which is all the counting needs.
func countThreadsDarwin(pids []int) map[int]int {
	return countThreadsDarwinContext(context.Background(), pids)
}

func countThreadsDarwinContext(ctx context.Context, pids []int) map[int]int {
	counts := map[int]int{}
	if len(pids) == 0 || ctx.Err() != nil {
		return counts
	}
	requested := make(map[int]bool, len(pids))
	arguments := make([]string, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 || requested[pid] {
			continue
		}
		requested[pid] = true
		arguments = append(arguments, strconv.Itoa(pid))
	}
	threadsCmd, stopThreads := boundedContext(ctx, "ps", "-M", "-p", strings.Join(arguments, ","))
	defer stopThreads()
	out, err := threadsCmd.Output()
	if err != nil || ctx.Err() != nil {
		return counts
	}
	return tallyPSThreads(string(out), requested)
}

// tallyPSThreads counts the `ps -M` lines belonging to each requested pid.
//
// `ps -M` prints a process line ("USER PID TT %CPU ...") followed by one line
// per thread ("PID %CPU ...", with the user and tty columns blank). The pid is
// therefore in the second field on a process line and the first on a thread
// line; either way the line counts for whichever requested pid it names, and a
// thread line belongs to the process line above it.
func tallyPSThreads(output string, requested map[int]bool) map[int]int {
	tally := make(map[int]int, len(requested))
	current := 0
	for index, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || (index == 0 && strings.EqualFold(fields[0], "USER")) {
			continue
		}
		if owner, ok := requestedPID(fields, requested); ok {
			current = owner
		}
		if current == 0 {
			continue
		}
		tally[current]++
	}
	return tally
}

// requestedPID reports which of the requested pids a line names, trying the
// process-line column before the thread-line one.
func requestedPID(fields []string, requested map[int]bool) (int, bool) {
	candidates := []string{fields[0]}
	if len(fields) > 1 {
		candidates = []string{fields[1], fields[0]}
	}
	for _, field := range candidates {
		if pid, err := strconv.Atoi(field); err == nil && requested[pid] {
			return pid, true
		}
	}
	return 0, false
}

// stateNames maps the single-character process state `ps` prints to the words
// the contract uses. An unknown letter falls through to lower case, which is
// closer to the truth than an empty field.
var stateNames = map[byte]string{
	'R': "running",
	'S': "sleeping",
	'D': "disk sleep",
	'T': "stopped",
	'Z': "zombie",
	'I': "idle",
	'U': "uninterruptible",
}

// decodeState converts the single-character process state to a readable word.
func decodeState(raw string) string {
	if raw == "" {
		return ""
	}
	if name, known := stateNames[raw[0]]; known {
		return name
	}
	return strings.ToLower(raw)
}

// startTimeLayouts are the shapes a process start time arrives in: `ps -o
// lstart` (one or two spaces before a single-digit day) and the ISO-8601 instant
// PowerShell reports on Windows.
var startTimeLayouts = []string{
	// ps lstart, which pads a single-digit day with a second space.
	"Mon Jan  2 15:04:05 2006",
	"Mon Jan 2 15:04:05 2006",
	// PowerShell's .ToString('o').
	time.RFC3339Nano,
	time.RFC3339,
}

// startTime parses a raw process start time.
//
// `ps` prints local time with no zone, so a zoneless layout has to be read in
// the local zone: parsing it as UTC puts the instant a whole UTC offset away
// from the truth, which is how uptime once came back as "-7185s" for a process
// seconds old while started_at — already parsed locally — was right. Both
// fields now come from this one function, so they cannot disagree again.
func startTime(raw string) (time.Time, bool) {
	if raw = strings.TrimSpace(raw); raw == "" {
		return time.Time{}, false
	}
	for _, layout := range startTimeLayouts {
		if parsed, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

// parseStartTime renders a raw start time as RFC3339, the shape started_at has
// in the contract. An unparseable value yields "", never a wrong instant.
func parseStartTime(raw string) string {
	parsed, ok := startTime(raw)
	if !ok {
		return ""
	}
	return parsed.Format(time.RFC3339)
}

// computeUptime is how long ago a process started, rendered for a human.
func computeUptime(lstart string) string {
	return computeUptimeAt(lstart, time.Now())
}

// computeUptimeAt is computeUptime against an explicit clock, so the conversion
// is testable without racing real time.
func computeUptimeAt(lstart string, now time.Time) string {
	started, ok := startTime(lstart)
	if !ok {
		return ""
	}
	return formatDuration(now.Sub(started))
}

// formatDuration renders a duration for a human. A negative one — a clock that
// stepped back, or a start time a moment in the future — reads as 0s rather
// than as a nonsense negative uptime.
func formatDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// byteUnits are the thresholds FormatBytes switches at, largest first.
var byteUnits = []struct {
	scale  int64
	suffix string
}{
	{1 << 30, "G"},
	{1 << 20, "M"},
	{1 << 10, "K"},
}

// FormatBytes returns a human-readable byte size.
func FormatBytes(bytes int64) string {
	for _, unit := range byteUnits {
		if bytes >= unit.scale {
			return fmt.Sprintf("%.1f%s", float64(bytes)/float64(unit.scale), unit.suffix)
		}
	}
	return fmt.Sprintf("%dB", bytes)
}
