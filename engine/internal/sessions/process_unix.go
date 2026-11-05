//go:build !windows

package sessions

import (
	"os/exec"
	"strconv"
	"strings"
)

// processTable returns the host's processes for the ancestry walk. Detection
// is paid once per `oberth start`, so a single `ps` is the whole budget; if it
// fails the caller falls back to the parent pid.
func processTable() []Process {
	raw, err := exec.Command("ps", "-Ao", "pid=,ppid=,comm=").Output()
	if err != nil {
		return nil
	}
	return parsePS(string(raw))
}

// parsePS reads the `pid ppid comm` rows. A command may itself contain spaces
// (it is often a full path followed by arguments), so only the first two
// columns are parsed and the remainder is kept verbatim.
func parsePS(raw string) []Process {
	var rows []Process
	for _, line := range strings.Split(raw, "\n") {
		columns := strings.Fields(line)
		if len(columns) < 3 {
			continue
		}
		pid, pidErr := strconv.Atoi(columns[0])
		ppid, ppidErr := strconv.Atoi(columns[1])
		if pidErr != nil || ppidErr != nil {
			continue
		}
		rows = append(rows, Process{
			PID:     pid,
			PPID:    ppid,
			Command: strings.Join(columns[2:], " "),
		})
	}
	return rows
}
