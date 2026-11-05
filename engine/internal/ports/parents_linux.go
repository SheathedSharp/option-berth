package ports

import (
	"os"
	"strconv"
)

// nativeParentTable reads parent pids from /proc, the authoritative source on
// Linux. Each numeric directory is a process and its stat file names the
// parent; a process that exits mid-walk is simply skipped.
func nativeParentTable() map[int]int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	parents := make(map[int]int, len(entries))
	for _, entry := range entries {
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		if parent, ok := procParent(entry.Name()); ok {
			parents[pid] = parent
		}
	}
	return parents
}

// procParent reads one process's parent from its /proc stat file. A process
// that exits mid-walk simply has no entry.
func procParent(name string) (int, bool) {
	stat, err := os.ReadFile("/proc/" + name + "/stat")
	if err != nil {
		return 0, false
	}
	return parseProcStatPPID(string(stat))
}
