package killer

import (
	"context"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Process is one row of the system process table: enough to walk ancestry and
// to name a process in a result row.
type Process struct {
	PID     int
	PPID    int
	Command string
}

// ProcessTable is a pid -> Process snapshot of the machine, taken once per
// KillPorts call so that a tree walk never races a moving process table.
type ProcessTable map[int]Process

// maxTreeDepth bounds every ancestry/descendant walk so a pathological or
// cyclic table (pid 1 reparenting, a pid reported as its own parent) can never
// spin forever.
const maxTreeDepth = 64

// Children returns the direct children of pid, sorted by pid for determinism.
func (t ProcessTable) Children(pid int) []int {
	var out []int
	for child, p := range t {
		if p.PPID == pid && child != pid {
			out = append(out, child)
		}
	}
	sort.Ints(out)
	return out
}

// Descendants returns pid and every process below it, children before parents
// (post-order). Signalling in this order stops a supervisor's workers before
// the supervisor itself, so a restart-on-exit parent has nothing left to
// restart. pid itself is always last.
//
// Processes not present in the table (a pid the scan saw but ps did not, or a
// table this platform cannot build) still yield the single pid, so callers get
// the same shape either way.
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
	var out []int
	seen := map[int]bool{pid: true}
	for cur, depth := pid, 0; depth < maxTreeDepth; depth++ {
		p, ok := t[cur]
		if !ok || p.PPID <= 1 || seen[p.PPID] {
			break
		}
		seen[p.PPID] = true
		out = append(out, p.PPID)
		cur = p.PPID
	}
	return out
}

// Name returns a short, human-usable name for a pid: the basename of the
// command's first word. Empty when the pid is unknown.
func (t ProcessTable) Name(pid int) string {
	p, ok := t[pid]
	if !ok || p.Command == "" {
		return ""
	}
	fields := strings.Fields(p.Command)
	if len(fields) == 0 {
		return ""
	}
	first := fields[0]
	if i := strings.LastIndexAny(first, "/\\"); i >= 0 && i+1 < len(first) {
		first = first[i+1:]
	}
	return first
}

// scanProcessTable snapshots the process table. On unix this is the same
// `ps -A` call the scanner's enrichment uses; on Windows there is no cheap
// equivalent and none is needed, because the Windows killer delegates tree
// termination to `taskkill /T`.
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

// parseProcessTable parses `ps -A -o pid=,ppid=,command=` output. Split out so
// tests can feed a fixture without running ps.
func parseProcessTable(out string) ProcessTable {
	table := ProcessTable{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		pid, err := strconv.Atoi(fields[0])
		if err != nil {
			continue
		}
		ppid, err := strconv.Atoi(fields[1])
		if err != nil {
			continue
		}
		// Consume the two numeric columns by position. Searching for PPID's
		// digits can match inside PID (100 -> 1) and corrupt the command tail.
		command := strings.TrimSpace(line[len(fields[0]):])
		command = strings.TrimSpace(command[len(fields[1]):])
		table[pid] = Process{
			PID:     pid,
			PPID:    ppid,
			Command: command,
		}
	}
	return table
}
