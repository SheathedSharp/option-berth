package killer

import (
	"context"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Process is one row of the system process table: enough to walk ancestry and
// to name a process in a result.
type Process struct {
	PID  int
	PPID int

	Command string
}

// ProcessTable is a pid -> Process snapshot taken once per KillPorts call, so a
// tree walk never races a moving process table.
type ProcessTable map[int]Process

// maxTreeDepth bounds every walk. A pathological table — pid 1 reparenting, or
// a pid claiming itself as parent — can never spin forever.
const maxTreeDepth = 64

// Children returns the direct children of pid, sorted by pid for determinism.
func (t ProcessTable) Children(pid int) []int {
	children := make([]int, 0, 4)
	for candidate, process := range t {
		if process.PPID == pid && candidate != pid {
			children = append(children, candidate)
		}
	}
	sort.Ints(children)
	return children
}

// Descendants returns pid and everything below it, children before parents
// (post-order). Signalling in that order stops a supervisor's workers before
// the supervisor, so a restart-on-exit parent has nothing left to restart; pid
// itself is always last.
//
// A pid the table never saw still yields itself, so callers get one shape
// whether or not this platform can build a table.
func (t ProcessTable) Descendants(pid int) []int {
	if pid <= 0 {
		return nil
	}
	// Build the parent index once. Re-scanning the entire process table for
	// every visited node made a wide tree quadratic in the table size.
	children := make(map[int][]int)
	for child, process := range t {
		if child != process.PPID {
			children[process.PPID] = append(children[process.PPID], child)
		}
	}
	seen := map[int]bool{}
	var out []int
	var walk func(int, int)
	walk = func(p, depth int) {
		if seen[p] || depth > maxTreeDepth {
			return
		}
		seen[p] = true
		// Sort only the reachable sibling sets, preserving the public order.
		cc := children[p]
		sort.Ints(cc)
		for _, child := range cc {
			walk(child, depth+1)
		}
		out = append(out, p)
	}
	walk(pid, 0)
	return out
}

// Ancestors returns the chain above pid, closest parent first, stopping at
// pid 1 (or at the first pid missing from the table).
func (t ProcessTable) Ancestors(pid int) []int {
	var chain []int
	visited := map[int]bool{pid: true}
	current := pid
	for depth := 0; depth < maxTreeDepth; depth++ {
		process, known := t[current]
		if !known || process.PPID <= 1 || visited[process.PPID] {
			break
		}
		visited[process.PPID] = true
		chain = append(chain, process.PPID)
		current = process.PPID
	}
	return chain
}

// Name returns a short, human-usable name for a pid: the basename of the
// command's first word. Empty when the pid is unknown.
func (t ProcessTable) Name(pid int) string {
	process, known := t[pid]
	if !known || process.Command == "" {
		return ""
	}
	fields := strings.Fields(process.Command)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	if slash := strings.LastIndexAny(first, `/\`); slash >= 0 && slash+1 < len(first) {
		first = first[slash+1:]
	}
	return first
}

// scanProcessTable snapshots the machine. Unix uses the same `ps -A` call as
// the scanner's enrichment; Windows has no cheap equivalent and needs none,
// because its tree termination is delegated to `taskkill /T`.
func scanProcessTable() ProcessTable {
	table, _ := scanProcessTableContext(context.Background())
	return table
}

func scanProcessTableContext(ctx context.Context) (ProcessTable, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if runtime.GOOS == "windows" {
		return ProcessTable{}, nil
	}
	cmd, cancel := controlCommand(ctx, "ps", "-A", "-o", "pid=,ppid=,command=")
	defer cancel()
	out, err := cmd.Output()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	return parseProcessTable(string(out)), nil
}

// parseProcessTable reads `ps -A -o pid=,ppid=,command=` output. It is split
// out so tests can feed a fixture without running ps.
func parseProcessTable(out string) ProcessTable {
	table := ProcessTable{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if process, ok := parseProcessRow(strings.TrimSpace(line)); ok {
			table[process.PID] = process
		}
	}
	return table
}

func parseProcessRow(line string) (Process, bool) {
	if line == "" {
		return Process{}, false
	}
	columns := strings.Fields(line)
	if len(columns) < 3 {
		return Process{}, false
	}
	pid, err := strconv.Atoi(columns[0])
	if err != nil {
		return Process{}, false
	}
	ppid, err := strconv.Atoi(columns[1])
	if err != nil {
		return Process{}, false
	}
	// The two numeric columns are consumed by position: searching for PPID's
	// digits can match inside PID (100 → 1) and corrupt the command tail.
	command := strings.TrimSpace(line[len(columns[0]):])
	command = strings.TrimSpace(command[len(columns[1]):])
	return Process{PID: pid, PPID: ppid, Command: command}, true
}
