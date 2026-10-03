package ports

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// Enrich populates process identity and classifies the port type. This is fast
// and always runs.
func Enrich(pp []ListeningPort) {
	_ = EnrichContext(context.Background(), pp)
}

// EnrichContext enriches caller-owned rows. On cancellation the caller must
// discard the round; completed mutations are not rolled back. System commands
// inherit the parent deadline and retain their existing individual ceilings.
// Filesystem/PEB calls are checked between operations, not forcibly detached.
func EnrichContext(ctx context.Context, pp []ListeningPort) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(pp) == 0 {
		return nil
	}
	// On Windows a row's command line and parent arrive together from the CIM
	// query, and tasklist fills names for rows it could not answer for.
	// Unix learns all of it from the cached process table inside
	// enrichDisplayNameSignals, so no per-tick `ps -p` runs here.
	if runtime.GOOS == "windows" {
		pids := collectPIDs(pp)
		pidStrs := make([]string, len(pids))
		for i, p := range pids {
			pidStrs[i] = strconv.Itoa(p)
		}
		observed := beginProcessObservation()
		commands := batchGetCommandsWindowsContext(ctx, pidStrs)
		if err := ctx.Err(); err != nil {
			return err
		}
		fillProcessNamesWindowsContext(ctx, pp, commands)
		if err := ctx.Err(); err != nil {
			return err
		}
		rememberObservedScanParents(commands, observed)
		for i := range pp {
			if info, ok := commands[pp[i].PID]; ok {
				pp[i].Command = info.command
				// Windows learns the parent from the same CIM query that
				// fetched the command line, so the ancestry walk costs no
				// second process table.
				if info.ppid > 0 {
					pp[i].PPID = info.ppid
				}
				// started_at is never gated by --stats: the contract publishes
				// it on every row (contract §21). EnrichStats refines the same
				// field with the raw ps lstart it parses anyway.
				if pp[i].StartedAt == "" {
					pp[i].StartedAt = info.startedAt
				}
			}
		}
	}

	for i := range pp {
		if pp[i].Type != PortTypeDocker {
			pp[i].Type = ClassifyPort(pp[i].Port)
		}
	}

	// Collect process identity and cwds so DisplayName
	// can resolve meaningful names without doing any I/O itself.
	if err := enrichDisplayNameSignalsContext(ctx, pp); err != nil {
		return err
	}

	// Whether that cwd still exists is the leftover case: the process holds its
	// port while the directory behind it is gone. It has to run after the
	// signals, which is what fills Cwd in, and it is code's answer, not a
	// model's — a client only labels the row with it.
	for i := range pp {
		if err := ctx.Err(); err != nil {
			return err
		}
		pp[i].CwdGone = cwdGone(pp[i].Cwd)
	}
	return ctx.Err()
}

// cwdGone answers whether the directory a process was started in is gone. An
// empty cwd means nobody ever learned it, which is not the same thing.
func cwdGone(cwd string) bool {
	if cwd == "" {
		return false
	}
	_, err := os.Stat(cwd)
	return errors.Is(err, os.ErrNotExist)
}

// EnrichStats populates CPU, memory, threads, uptime, state, and connections.
// For Docker containers it uses pre-fetched dockerStats.
// For native processes it batches all PIDs into a single ps call.
// Called only when --stats is requested.
func EnrichStats(pp []ListeningPort, dockerStats map[string]*DockerStatsEntry) {
	_ = EnrichStatsContext(context.Background(), pp, dockerStats)
}

// EnrichStatsContext binds the full scan's process, thread and connection
// commands to the same owner. Rows belong to the caller and must be discarded
// on error, as with EnrichContext; ordinary missing metadata stays partial.
func EnrichStatsContext(ctx context.Context, pp []ListeningPort, dockerStats map[string]*DockerStatsEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Apply Docker stats
	if dockerStats != nil {
		for i := range pp {
			if pp[i].Type == PortTypeDocker && pp[i].DockerContainer != "" {
				if stats, ok := dockerStats[pp[i].DockerContainer]; ok {
					pp[i].CPUPercent = stats.CPUPercent
					pp[i].MemoryRSS = stats.MemoryRSS
					pp[i].ThreadCount = stats.PIDs
					pp[i].State = stats.State
					pp[i].Uptime = stats.Uptime
				}
			}
		}
	}

	// Batch native process stats into a single ps call
	batchEnrichProcessStatsContext(ctx, pp)
	if err := ctx.Err(); err != nil {
		return err
	}

	// Connection counts, from one listing of the machine's TCP connections
	// rather than one call per port.
	applyConnectionCountsContext(ctx, pp)
	return ctx.Err()
}

// DockerStatsEntry holds pre-fetched per-container stats.
type DockerStatsEntry struct {
	CPUPercent float64
	MemoryRSS  int64
	PIDs       int
	State      string
	Uptime     string
}

// procInfo is what one always-on ps call reports per listening process: its
// full command line and when it started. Both are unconditional — `started_at`
// is not a stat (contract §21).
type procInfo struct {
	command   string
	startedAt string // RFC3339, "" when ps did not report a parsable time
	ppid      int    // 0 when the source did not report a parent
}

// lstartFields is how many whitespace-separated tokens `ps -o lstart=` emits:
// "Mon Jan  2 15:04:05 2006". (The Unix process table parser lives in
// displayname_signals.go beside the cache it feeds.)

// fillProcessNamesWindows names the rows the CIM query left blank. tasklist
// needs no WMI service and no elevation, so it answers when Get-CimInstance
// does not, and a pid it cannot name simply stays unnamed: one process failing
// to resolve must not cost the others their identity.
func fillProcessNamesWindows(pp []ListeningPort, commands map[int]procInfo) {
	fillProcessNamesWindowsContext(context.Background(), pp, commands)
}

func fillProcessNamesWindowsContext(ctx context.Context, pp []ListeningPort, commands map[int]procInfo) {
	missing := false
	for i := range pp {
		if pp[i].Process == "" && commands[pp[i].PID].command == "" {
			missing = true
			break
		}
	}
	if !missing || ctx.Err() != nil {
		return
	}

	tasklistCmd, stopTasklist := boundedContext(ctx, "tasklist", "/NH", "/FO", "CSV")
	defer stopTasklist()
	out, err := tasklistCmd.Output()
	if err != nil || ctx.Err() != nil {
		return
	}
	names := parseTasklist(string(out))
	for i := range pp {
		if pp[i].Process != "" {
			continue
		}
		if name, ok := names[pp[i].PID]; ok {
			pp[i].Process = name
		}
	}
}

// parseTasklist reads `tasklist /NH /FO CSV` into pid -> image name. Rows it
// cannot parse are skipped; the ones it can are still returned.
func parseTasklist(out string) map[int]string {
	names := make(map[int]string)
	r := csv.NewReader(strings.NewReader(strings.TrimSpace(out)))
	r.FieldsPerRecord = -1
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			// One unreadable line is one process we cannot name, not a reason
			// to forget the ones already read.
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue
			}
			return names
		}
		if len(rec) < 2 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			continue
		}
		if name := strings.TrimSpace(rec[0]); name != "" {
			names[pid] = name
		}
	}
}

// batchGetCommandsWindows fetches command lines and start times via PowerShell
// Get-CimInstance on Windows.
func batchGetCommandsWindows(pidStrs []string) map[int]procInfo {
	observed := beginProcessObservation()
	result := batchGetCommandsWindowsContext(context.Background(), pidStrs)
	rememberObservedScanParents(result, observed)
	return result
}

func batchGetCommandsWindowsContext(ctx context.Context, pidStrs []string) map[int]procInfo {
	result := make(map[int]procInfo)
	if len(pidStrs) == 0 || ctx.Err() != nil {
		return result
	}

	// Build WMI filter: "ProcessId=123 or ProcessId=456"
	var conditions []string
	for _, p := range pidStrs {
		conditions = append(conditions, "ProcessId="+p)
	}
	filter := strings.Join(conditions, " or ")

	// ParentProcessId rides along on the query that was already being made:
	// Windows has no `ps -A` to build a process table from, and spawning a
	// second PowerShell per scan to learn one parent pid would cost more than
	// everything else the scan does put together.
	psCmd := fmt.Sprintf(
		"Get-CimInstance Win32_Process -Filter '%s' | Select-Object ProcessId,ParentProcessId,@{N='StartedAt';E={$_.CreationDate.ToString('o')}},CommandLine | ConvertTo-Csv -NoTypeInformation",
		filter,
	)

	cimCmd, stopCIM := boundedContext(ctx, "powershell", "-NoProfile", "-Command", psCmd)
	defer stopCIM()
	out, err := cimCmd.Output()
	if err != nil || ctx.Err() != nil {
		return result
	}

	result = parseCIMProcesses(string(out))
	return result
}

// parseCIMProcesses reads the CSV Get-CimInstance produced.
//
// It finds its columns by name from the header rather than by position. The
// query asks for ProcessId, ParentProcessId, StartedAt and CommandLine, and
// Select-Object emits them in that order — but a positional parser turns any
// future edit to that Select-Object list, or a PowerShell that orders or names
// things differently, into silently misread rows: a parent pid read as a start
// time, a command line read from the wrong column. Reading the header is one
// map and removes the entire class.
//
// It is also tolerant per row. The previous parser called ReadAll and returned
// nothing at all when any single line failed to parse, so one process with an
// odd command line cost every other process on the machine its identity — and
// on Windows identity is what decides whether a port is shown and which group
// it joins. A row that cannot be read is skipped; the ones that can are kept.
//
// A row with no command line still counts: on Windows the command line is the
// field most often withheld (another user's process, a protected one), and
// dropping the row with it would throw away the parent pid and the start time
// that did come back.
func parseCIMProcesses(out string) map[int]procInfo {
	result := map[int]procInfo{}
	text := strings.TrimSpace(strings.TrimPrefix(out, "\ufeff"))
	if text == "" {
		return result
	}
	r := csv.NewReader(strings.NewReader(text))
	r.FieldsPerRecord = -1
	r.LazyQuotes = true

	var cols map[string]int
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue // one unreadable line, not the end of the batch
			}
			return result
		}
		if len(rec) == 0 {
			continue
		}
		// PowerShell 5.1 emits a `#TYPE …` line unless -NoTypeInformation is
		// passed. It is, but skipping the line costs nothing and a missing
		// flag would otherwise be read as the header.
		if strings.HasPrefix(strings.TrimSpace(rec[0]), "#TYPE") {
			continue
		}
		if cols == nil {
			cols = cimColumns(rec)
			if _, ok := cols["processid"]; !ok {
				return result // not a header we understand
			}
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(field(rec, cols, "processid")))
		if err != nil || pid <= 0 {
			continue
		}
		ppid, _ := strconv.Atoi(strings.TrimSpace(field(rec, cols, "parentprocessid")))
		cmd := strings.TrimSpace(field(rec, cols, "commandline"))
		startedAt := parseStartTime(field(rec, cols, "startedat"))
		// Protected processes may expose birth evidence but neither their
		// parent nor command. Preserve the fact we have; do not invent others.
		if cmd == "" && ppid <= 0 && startedAt == "" {
			continue
		}
		result[pid] = procInfo{command: cmd, startedAt: startedAt, ppid: ppid}
	}
}

// cimColumns maps a CSV header to column indexes, lowercased and trimmed so
// the lookup does not depend on PowerShell's capitalisation.
func cimColumns(header []string) map[string]int {
	cols := make(map[string]int, len(header))
	for i, name := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		if key == "" {
			continue
		}
		if _, taken := cols[key]; !taken {
			cols[key] = i
		}
	}
	return cols
}

// field reads a named column, returning "" when the header did not have it or
// the row is short.
func field(rec []string, cols map[string]int, name string) string {
	i, ok := cols[name]
	if !ok || i >= len(rec) {
		return ""
	}
	return rec[i]
}

// batchEnrichProcessStats fetches CPU, memory, state and uptime for every
// non-Docker port. It goes through the same one-call sampler the daemon's
// stats-only tick uses, so a row enriched by a scan and a row refreshed
// between scans are filled from identical parsing.
//
// The sample is keyed by pid and applied to every row that pid owns. Keying
// the other way round — one *ListeningPort per pid — silently dropped the
// stats of every socket but the last when one process listened on several.
func batchEnrichProcessStats(pp []ListeningPort) {
	batchEnrichProcessStatsContext(context.Background(), pp)
}

func batchEnrichProcessStatsContext(ctx context.Context, pp []ListeningPort) {
	pids := make([]int, 0, len(pp))
	for i := range pp {
		if pp[i].Type != PortTypeDocker && pp[i].PID > 0 {
			pids = append(pids, pp[i].PID)
		}
	}
	if len(pids) == 0 || ctx.Err() != nil {
		return
	}

	samples := SampleProcStatsContext(ctx, pids)
	if ctx.Err() != nil {
		return
	}

	// macOS reports no thread count in the sampler's `ps`, so it takes a
	// second (batched) call. It runs here, on the scan tick, and never on the
	// 1 s stats tick.
	if runtime.GOOS == "darwin" {
		threads := countThreadsDarwinContext(ctx, pids)
		if ctx.Err() != nil {
			return
		}
		for pid, s := range samples {
			s.ThreadCount = threads[pid]
			samples[pid] = s
		}
	}

	for i := range pp {
		if pp[i].Type == PortTypeDocker || pp[i].PID <= 0 {
			continue
		}
		if s, ok := samples[pp[i].PID]; ok {
			s.Apply(&pp[i])
		}
	}
}

// collectPIDs returns unique non-zero PIDs from the port list.
func collectPIDs(pp []ListeningPort) []int {
	seen := make(map[int]bool)
	var pids []int
	for _, p := range pp {
		if p.PID > 0 && !seen[p.PID] {
			seen[p.PID] = true
			pids = append(pids, p.PID)
		}
	}
	return pids
}

// InTrash reports whether a path is inside a trash directory.
//
// It is a fact about where a process works, and it is worth surfacing on its
// own. A listener whose working directory is in the trash belongs to a project
// that has been deleted: the code is gone, nothing is going to restart it, and
// it is still holding its port. That is a leftover, and the useful thing to say
// about it is "you can kill this" — which is a different kind of statement from
// what the thing **is**. A redis in the trash is still a redis.
//
// So this is not a class of listener, and it must not become one. It is
// computed by code and shown as option-berth's own words, never asked of a model:
// a path either starts with a trash root or it does not.
func InTrash(path string) bool {
	if path == "" {
		return false
	}
	clean := filepath.Clean(path)
	sep := string(filepath.Separator)

	// The user's own trash: ~/.Trash on macOS, and the freedesktop layout on
	// Linux.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		for _, root := range []string{
			filepath.Join(home, ".Trash"),
			filepath.Join(home, ".local", "share", "Trash"),
		} {
			if clean == root || strings.HasPrefix(clean, root+sep) {
				return true
			}
		}
	}

	// A mounted volume's trash: /Volumes/<name>/.Trashes/<uid>/… . Deleting a
	// file on an external disk moves it to that disk, so the path stays under
	// /Volumes and the file is still there.
	if strings.HasPrefix(clean, "/Volumes/") {
		for _, seg := range strings.Split(clean, sep) {
			if seg == ".Trashes" {
				return true
			}
		}
	}
	return false
}

// ClassifyPort keeps native listeners in the user-owned evidence class. The
// product does not classify machine ports by privilege or system ownership.
func ClassifyPort(_ int) PortType { return PortTypeUser }
