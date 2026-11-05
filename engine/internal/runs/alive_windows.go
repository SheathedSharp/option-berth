//go:build windows

package runs

import (
	"os/exec"
	"strconv"
	"strings"
)

// pidAlive reports whether a process currently exists. This is best-effort on
// Windows: ask tasklist for the PID and look for a row. If the query itself
// fails the answer is "alive", because pruning on a failed observation would
// delete evidence the engine cannot disprove.
func pidAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	out, err := exec.Command("tasklist", "/FI", "PID eq "+strconv.Itoa(pid), "/NH").Output()
	if err != nil {
		return true
	}
	return len(out) > 0 && !strings.Contains(string(out), "No t")
}
