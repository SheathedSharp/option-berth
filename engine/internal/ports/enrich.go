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

// EnrichStats fills CPU, memory, threads, uptime, state and connection
// counts. Docker rows take the pre-fetched dockerStats; native processes are
// batched into one ps call. It runs only when --stats was requested.
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
	applyDockerStats(pp, dockerStats)

	// Native process stats come from one batched ps call.
	batchEnrichProcessStatsContext(ctx, pp)
	if err := ctx.Err(); err != nil {
		return err
	}

	// Connection counts, from one listing of the machine's TCP connections
	// rather than one call per port.
	applyConnectionCountsContext(ctx, pp)
	return ctx.Err()
}

// DockerStatsEntry is one container's pre-fetched stats.
type DockerStatsEntry struct {
	CPUPercent float64
	MemoryRSS  int64

	// Process count, then the container's current state.
	PIDs   int
	State  string
	Uptime string
}

// applyDockerStats copies the pre-fetched container numbers onto Docker rows.
func applyDockerStats(pp []ListeningPort, dockerStats map[string]*DockerStatsEntry) {
	for i := range pp {
		if pp[i].Type != PortTypeDocker || pp[i].DockerContainer == "" {
			continue
		}
		entry, found := dockerStats[pp[i].DockerContainer]
		if !found {
			continue
		}
		pp[i].CPUPercent = entry.CPUPercent
		pp[i].MemoryRSS = entry.MemoryRSS
		pp[i].ThreadCount = entry.PIDs
		pp[i].State = entry.State
		pp[i].Uptime = entry.Uptime
	}
}

// procInfo is what one always-on process query reports per listener: the full
// command line and when it started. Both are unconditional — started_at is not
// a stat.
type procInfo struct {
	command   string
	startedAt string // RFC3339, "" when the source gave no parsable time
	ppid      int    // 0 when the source did not report a parent
}

// lstartFields is how many whitespace-separated tokens `ps -o lstart=` emits:
// "Mon Jan  2 15:04:05 2006". (The Unix process table parser lives in
// displayname_signals.go beside the cache it feeds.)

// fillProcessNamesWindows names the rows the CIM query left blank. tasklist
// needs neither WMI nor elevation, so it answers when Get-CimInstance does
// not; a pid it cannot name simply stays unnamed, and one failure must not
// cost the other processes their identity.
func fillProcessNamesWindows(pp []ListeningPort, commands map[int]procInfo) {
	fillProcessNamesWindowsContext(context.Background(), pp, commands)
}

// needsTasklist reports whether any row still lacks a name and the command
// query did not have one either.
func needsTasklist(pp []ListeningPort, commands map[int]procInfo) bool {
	for i := range pp {
		if pp[i].Process == "" && commands[pp[i].PID].command == "" {
			return true
		}
	}
	return false
}

func fillProcessNamesWindowsContext(ctx context.Context, pp []ListeningPort, commands map[int]procInfo) {
	if !needsTasklist(pp, commands) || ctx.Err() != nil {
		return
	}

	tasklistCmd, stopTasklist := boundedContext(ctx, "tasklist", "/NH", "/FO", "CSV")
	defer stopTasklist()
	out, err := tasklistCmd.Output()
	if err != nil || ctx.Err() != nil {
		return
	}
	images := parseTasklist(string(out))
	for i := range pp {
		if pp[i].Process != "" {
			continue
		}
		if image, found := images[pp[i].PID]; found {
			pp[i].Process = image
		}
	}
}

// parseTasklist reads `tasklist /NH /FO CSV` into pid -> image name. A row it
// cannot parse is skipped; the rest are still returned.
func parseTasklist(out string) map[int]string {
	names := make(map[int]string)
	reader := csv.NewReader(strings.NewReader(strings.TrimSpace(out)))
	reader.FieldsPerRecord = -1
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return names
		}
		if err != nil {
			// One unreadable line costs that process its name, not the ones
			// already read.
			var lineErr *csv.ParseError
			if errors.As(err, &lineErr) {
				continue
			}
			return names
		}
		if len(record) < 2 {
			continue
		}
		pid, convErr := strconv.Atoi(strings.TrimSpace(record[1]))
		if convErr != nil {
			continue
		}
		if image := strings.TrimSpace(record[0]); image != "" {
			names[pid] = image
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

	filter := wmiFilter(pidStrs)

	// ParentProcessId rides along on the query already being made: Windows has
	// no `ps -A` to build a table from, and a second PowerShell per scan for
	// one parent pid would cost more than everything else the scan does.
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

// wmiFilter builds the "ProcessId=123 or ProcessId=456" filter.
func wmiFilter(pidStrs []string) string {
	conditions := make([]string, len(pidStrs))
	for i, pid := range pidStrs {
		conditions[i] = "ProcessId=" + pid
	}
	return strings.Join(conditions, " or ")
}

// parseCIMProcesses reads the CSV Get-CimInstance produced.
//
// Columns are found by name from the header, not by position: a positional
// parser turns any change to the Select-Object list into silently misread rows.
// Parsing is also per row — one unreadable line costs that process its
// identity, never the rest of the batch.
//
// A row without a command line still counts: Windows withholds it for another
// user's or a protected process most often, and dropping the row would throw
// away the parent pid and start time that did arrive.
func parseCIMProcesses(out string) map[int]procInfo {
	records := map[int]procInfo{}
	text := strings.TrimSpace(strings.TrimPrefix(out, "\ufeff"))
	if text == "" {
		return records
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true

	var columns map[string]int
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			return records
		}
		if err != nil {
			var lineErr *csv.ParseError
			if errors.As(err, &lineErr) {
				continue // one unreadable line, not the end of the batch
			}
			return records
		}
		if len(record) == 0 {
			continue
		}
		// PowerShell 5.1 emits `#TYPE …` unless -NoTypeInformation is passed.
		// Skipping it costs nothing and a missing flag would otherwise turn it
		// into the header.
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#TYPE") {
			continue
		}
		if columns == nil {
			columns = cimColumns(record)
			if _, known := columns["processid"]; !known {
				return records // a header we do not understand
			}
			continue
		}
		if pid, info, usable := decodeCIMRow(record, columns); usable {
			records[pid] = info
		}
	}
}

// decodeCIMRow reads one CIM CSV row by column name. Protected processes may
// expose birth evidence but neither parent nor command; whatever fact is
// present is kept, and a row with none is skipped.
func decodeCIMRow(record []string, columns map[string]int) (int, procInfo, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(field(record, columns, "processid")))
	if err != nil || pid <= 0 {
		return 0, procInfo{}, false
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(field(record, columns, "parentprocessid")))
	command := strings.TrimSpace(field(record, columns, "commandline"))
	startedAt := parseStartTime(field(record, columns, "startedat"))
	if command == "" && ppid <= 0 && startedAt == "" {
		return 0, procInfo{}, false
	}
	return pid, procInfo{command: command, startedAt: startedAt, ppid: ppid}, true
}

// cimColumns maps a CSV header to column indexes, lowercased and trimmed so
// the lookup does not depend on PowerShell's capitalisation.
func cimColumns(header []string) map[string]int {
	columns := make(map[string]int, len(header))
	for index, name := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		if key == "" {
			continue
		}
		if _, exists := columns[key]; !exists {
			columns[key] = index
		}
	}
	return columns
}

// field reads a named column; "" means the header lacked it or the row is
// short.
func field(record []string, columns map[string]int, name string) string {
	at, known := columns[name]
	if !known || at >= len(record) {
		return ""
	}
	return record[at]
}

// batchEnrichProcessStats fills CPU, memory, state and uptime for every
// non-Docker port through the same one-call sampler the stats-only tick uses,
// so a scan-filled row and a tick-refreshed row come from identical parsing.
//
// The sample is keyed by pid and applied to every row that pid owns; keying it
// per row dropped every socket but the last when one process listened on
// several.
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
		for pid, sample := range samples {
			sample.ThreadCount = threads[pid]
			samples[pid] = sample
		}
	}

	for i := range pp {
		if pp[i].Type == PortTypeDocker || pp[i].PID <= 0 {
			continue
		}
		if sample, ok := samples[pp[i].PID]; ok {
			sample.Apply(&pp[i])
		}
	}
}

// collectPIDs returns unique non-zero PIDs from the port list.
func collectPIDs(pp []ListeningPort) []int {
	seen := make(map[int]bool)
	pids := make([]int, 0, len(pp))
	for _, row := range pp {
		if row.PID <= 0 || seen[row.PID] {
			continue
		}
		seen[row.PID] = true
		pids = append(pids, row.PID)
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
