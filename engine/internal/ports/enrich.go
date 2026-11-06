package ports

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
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
		if err := enrichWindowsIdentity(ctx, pp); err != nil {
			return err
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

// enrichWindowsIdentity joins the CIM process query to the rows it describes.
// The query carries the command line, the parent and the start time, which is
// what Windows has instead of `ps -A`; tasklist is the fallback for names the
// query withheld. A pid neither source answered for keeps empty fields, and a
// cancelled round leaves the caller to discard the rows.
func enrichWindowsIdentity(ctx context.Context, pp []ListeningPort) error {
	args := pidArguments(collectPIDs(pp))
	// The ticket is taken before the query so a table observed later cannot be
	// overwritten by one observed earlier.
	observed := beginProcessObservation()
	commands := batchGetCommandsWindowsContext(ctx, args)
	if err := ctx.Err(); err != nil {
		return err
	}
	fillProcessNamesWindowsContext(ctx, pp, commands)
	if err := ctx.Err(); err != nil {
		return err
	}
	rememberObservedProcessFacts(commands, observed)
	for i := range pp {
		if facts, answered := commands[pp[i].PID]; answered {
			applyProcessFacts(&pp[i], facts, commands)
		}
	}
	return nil
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

// EnrichStats populates CPU, memory, threads, uptime, state and connections
// for the rows a scan produced. Container numbers are joined by container id;
// native numbers come from one batched process sample. Called only when stats
// were requested.
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
	applyContainerStats(pp, dockerStats)
	batchEnrichProcessStatsContext(ctx, pp)
	if err := ctx.Err(); err != nil {
		return err
	}
	// Connection counts, from one listing of the machine's TCP connections
	// rather than one call per port.
	applyConnectionCountsContext(ctx, pp)
	return ctx.Err()
}

// applyContainerStats writes pre-fetched container numbers onto the rows that
// belong to those containers. The container id is the join key; a row whose
// container is not in the map keeps what the process table said.
func applyContainerStats(pp []ListeningPort, stats map[string]*DockerStatsEntry) {
	if len(stats) == 0 {
		return
	}
	for i := range pp {
		if pp[i].Type != PortTypeDocker {
			continue
		}
		if entry, known := stats[pp[i].DockerContainer]; known && entry != nil {
			entry.applyTo(&pp[i])
		}
	}
}

// DockerStatsEntry holds pre-fetched per-container stats.
type DockerStatsEntry struct {
	CPUPercent float64
	MemoryRSS  int64
	PIDs       int
	State      string
	Uptime     string
}

// applyTo copies the container's numbers onto the row that reported it. These
// are runtime facts, not process facts: the fields land in the same row as a
// native sample, but nothing here proves the pid's identity.
func (entry DockerStatsEntry) applyTo(row *ListeningPort) {
	row.CPUPercent, row.MemoryRSS, row.ThreadCount = entry.CPUPercent, entry.MemoryRSS, entry.PIDs
	row.State, row.Uptime = entry.State, entry.Uptime
}

// lstartFields is how many whitespace-separated tokens `ps -o lstart=` emits:
// "Mon Jan  2 15:04:05 2006". (The Unix process table parser lives in
// displayname_signals.go beside the cache it feeds.)

// fillProcessNamesWindows names the rows the CIM query left blank. tasklist
// needs no WMI service and no elevation, so it answers where Get-CimInstance
// did not; a pid it cannot name simply stays unnamed, because one process
// failing to resolve must not cost the others their identity.
func fillProcessNamesWindows(pp []ListeningPort, commands map[int]processFacts) {
	fillProcessNamesWindowsContext(context.Background(), pp, commands)
}

func fillProcessNamesWindowsContext(ctx context.Context, pp []ListeningPort, commands map[int]processFacts) {
	if !anyUnnamed(pp, commands) || ctx.Err() != nil {
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
		if name, known := names[pp[i].PID]; known {
			pp[i].Process = name
		}
	}
}

// anyUnnamed reports whether tasklist could still add a name the CIM query did
// not already supply.
func anyUnnamed(pp []ListeningPort, commands map[int]processFacts) bool {
	for i := range pp {
		if pp[i].Process == "" && commands[pp[i].PID].command == "" {
			return true
		}
	}
	return false
}

// parseTasklist reads `tasklist /NH /FO CSV` into pid -> image name. There is
// no header: the image name is the first column and the pid the second.
func parseTasklist(out string) map[int]string {
	names := make(map[int]string)
	eachCSVRow(out, func(rec []string) {
		if len(rec) < 2 {
			return
		}
		pid, err := strconv.Atoi(strings.TrimSpace(rec[1]))
		if err != nil {
			return
		}
		if name := strings.TrimSpace(rec[0]); name != "" {
			names[pid] = name
		}
	})
	return names
}

// eachCSVRow feeds every readable row of a CSV document to visit, and skips the
// ones it cannot read.
//
// One unreadable line is one process without a name, not a reason to forget the
// processes already read: on Windows identity is what decides whether a port is
// shown at all and which group it joins, so a single odd command line must not
// cost every other process its identity.
func eachCSVRow(out string, visit func([]string)) {
	text := strings.TrimSpace(strings.TrimPrefix(out, "\ufeff"))
	if text == "" {
		return
	}
	reader := csv.NewReader(strings.NewReader(text))
	reader.FieldsPerRecord = -1
	reader.LazyQuotes = true
	for {
		record, err := reader.Read()
		if err != nil {
			var parseErr *csv.ParseError
			if errors.As(err, &parseErr) {
				continue
			}
			return // io.EOF, or a failure that ends the document
		}
		if len(record) > 0 {
			visit(record)
		}
	}
}

// batchGetCommandsWindows fetches command lines and start times via PowerShell
// Get-CimInstance on Windows.
func batchGetCommandsWindows(pidStrs []string) map[int]processFacts {
	observed := beginProcessObservation()
	result := batchGetCommandsWindowsContext(context.Background(), pidStrs)
	rememberObservedProcessFacts(result, observed)
	return result
}

func batchGetCommandsWindowsContext(ctx context.Context, pidStrs []string) map[int]processFacts {
	if len(pidStrs) == 0 || ctx.Err() != nil {
		return map[int]processFacts{}
	}
	cimCmd, stopCIM := boundedContext(ctx, "powershell", "-NoProfile", "-Command", cimProcessQuery(pidStrs))
	defer stopCIM()
	out, err := cimCmd.Output()
	if err != nil || ctx.Err() != nil {
		return map[int]processFacts{}
	}
	return parseCIMProcesses(string(out))
}

// cimProcessQuery asks for everything one scan needs in one call. The parent
// rides along because Windows has no `ps -A` to build a process table from; a
// second PowerShell per scan to learn one parent pid would cost more than the
// rest of the scan together.
func cimProcessQuery(pidStrs []string) string {
	conditions := make([]string, len(pidStrs))
	for i, pid := range pidStrs {
		conditions[i] = "ProcessId=" + pid
	}
	const fields = "ProcessId,ParentProcessId,@{N='StartedAt';E={$_.CreationDate.ToString('o')}},CommandLine"
	return fmt.Sprintf(
		"Get-CimInstance Win32_Process -Filter '%s' | Select-Object %s | ConvertTo-Csv -NoTypeInformation",
		strings.Join(conditions, " or "), fields,
	)
}

// parseCIMProcesses reads the CSV Get-CimInstance produced. Columns are found
// by their header names, so Select-Object's order and PowerShell's
// capitalisation cannot turn a parent pid into a start time. One unreadable
// row costs only itself: identity decides whether a port is shown and which
// group it joins, so the rows that could be read must survive the one that
// could not. A row that exposes its pid while withholding its command line
// still carries the parent and start time that did come back.
func parseCIMProcesses(out string) map[int]processFacts {
	table := parseCSVTable(out)
	result := make(map[int]processFacts, len(table.rows))
	if !table.knows("processid") {
		return result // a header we cannot read names nothing
	}
	for _, row := range table.rows {
		pid, info, ok := cimRow(table, row)
		if ok {
			result[pid] = info
		}
	}
	return result
}

// cimRow reads one CIM result row through the header. It reports false for a
// row that names no process, and for one that carries no evidence at all:
// a protected process may expose its pid while withholding its parent, its
// command and its start time, and a row of nothing but a pid says nothing.
func cimRow(table csvTable, row []string) (int, processFacts, bool) {
	pid, err := strconv.Atoi(strings.TrimSpace(table.value(row, "processid")))
	if err != nil || pid <= 0 {
		return 0, processFacts{}, false
	}
	ppid, _ := strconv.Atoi(strings.TrimSpace(table.value(row, "parentprocessid")))
	command := strings.TrimSpace(table.value(row, "commandline"))
	startedAt := parseStartTime(table.value(row, "startedat"))
	if command == "" && ppid <= 0 && startedAt == "" {
		return 0, processFacts{}, false
	}
	return pid, processFacts{command: command, startedAt: startedAt, ppid: ppid}, true
}

// csvTable is a decoded CSV document: the header's column positions and the
// rows that could be read. PowerShell may prefix the document with a #TYPE
// line, which is dropped before the header is taken.
type csvTable struct {
	columns map[string]int
	rows    [][]string
}

// parseCSVTable reads a CSV document without letting one bad row end it.
// Tolerating bad rows is deliberate: on Windows identity decides whether a
// port is shown and which group it joins, so one odd command line must not
// cost every other process its identity.
func parseCSVTable(out string) csvTable {
	var table csvTable
	eachCSVRow(out, func(record []string) {
		if strings.HasPrefix(strings.TrimSpace(record[0]), "#TYPE") {
			return
		}
		if table.columns == nil {
			table.columns = columnIndexes(record)
			return
		}
		table.rows = append(table.rows, record)
	})
	return table
}

// knows reports whether the header named a column.
func (t csvTable) knows(column string) bool {
	_, known := t.columns[column]
	return known
}

// value reads one named column, "" when the header lacked it or the row is
// short.
func (t csvTable) value(row []string, column string) string {
	index, known := t.columns[column]
	if !known || index >= len(row) {
		return ""
	}
	return row[index]
}

// columnIndexes maps a CSV header to column positions. Names are lowercased
// and trimmed, and a repeated name keeps its first position, so a lookup does
// not depend on PowerShell's capitalisation or on Select-Object's order.
func columnIndexes(header []string) map[string]int {
	columns := make(map[string]int, len(header))
	for index, name := range header {
		key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\ufeff")))
		if key == "" {
			continue
		}
		if _, taken := columns[key]; !taken {
			columns[key] = index
		}
	}
	return columns
}

// batchEnrichProcessStats fetches CPU, memory, state and uptime for every
// non-Docker port through the same one-call sampler the daemon's stats-only
// tick uses, so a scanned row and a refreshed row are filled from identical
// parsing. The sample is keyed by pid and applied to every row that pid owns:
// a per-row sample would leave every socket but the last of a process empty.
func batchEnrichProcessStats(pp []ListeningPort) {
	batchEnrichProcessStatsContext(context.Background(), pp)
}

func batchEnrichProcessStatsContext(ctx context.Context, pp []ListeningPort) {
	pids := nativePIDs(pp)
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
		samples = withThreadCounts(ctx, samples, pids)
		if ctx.Err() != nil {
			return
		}
	}
	applyProcessSamples(pp, samples)
}

// nativePIDs are the pids whose resource facts this scan can read itself.
func nativePIDs(pp []ListeningPort) []int {
	pids := make([]int, 0, len(pp))
	for i := range pp {
		if pp[i].Type != PortTypeDocker && pp[i].PID > 0 {
			pids = append(pids, pp[i].PID)
		}
	}
	return uniquePIDs(pids)
}

// withThreadCounts folds the macOS thread listing into the samples it
// describes. A pid the listing did not answer for keeps the zero that means
// "not reported".
func withThreadCounts(ctx context.Context, samples map[int]ProcSample, pids []int) map[int]ProcSample {
	threads := countThreadsDarwinContext(ctx, pids)
	for pid, sample := range samples {
		sample.ThreadCount = threads[pid]
		samples[pid] = sample
	}
	return samples
}

// applyProcessSamples joins one sample batch to the rows its pids own. Keying
// by pid, not by row, is what keeps every socket of one process filled; a pid
// the sample did not answer for keeps what it had.
func applyProcessSamples(pp []ListeningPort, samples map[int]ProcSample) {
	for i := range pp {
		if pp[i].Type == PortTypeDocker || pp[i].PID <= 0 {
			continue
		}
		if sample, known := samples[pp[i].PID]; known {
			sample.Apply(&pp[i])
		}
	}
}

// collectPIDs is the distinct pid set a row set names, in first-seen order.
func collectPIDs(pp []ListeningPort) []int {
	pids := make([]int, len(pp))
	for i, row := range pp {
		pids[i] = row.PID
	}
	return uniquePIDs(pids)
}

// uniquePIDs drops duplicates and anything that cannot name a process.
func uniquePIDs(pids []int) []int {
	seen := make(map[int]struct{}, len(pids))
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if pid <= 0 {
			continue
		}
		if _, duplicate := seen[pid]; duplicate {
			continue
		}
		seen[pid] = struct{}{}
		out = append(out, pid)
	}
	return out
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
