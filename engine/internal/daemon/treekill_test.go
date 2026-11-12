//go:build integration

package daemon_test

import (
	"os/exec"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// stopCommand takes a `option-berth` invocation down for good and reaps it.
//
// Killing only the process the test started is not enough. `oberth start` puts
// the command it supervises in a process group of its own (daemon spec), so
// that child survives its parent while still holding the inherited stdout pipe.
// Wait then blocks copying from a pipe nobody will close — which is how a
// failed assertion became a ten-minute CI timeout. So the whole tree goes, and
// the WaitDelay that env.command sets is the backstop for anything that gets
// away anyway.
func stopCommand(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	killTree(cmd)
	// Reap it now: an unreaped child keeps its pid, and a later kill by pid
	// could land on whatever the system handed that number to next.
	_ = cmd.Wait()
}

// descendants lists every process below root in the process table, deepest
// first, so a child is signalled before the parent that could respawn it.
func descendants(root int) []int {
	byParent := map[int][]int{}
	for child, parent := range ports.ParentTable() {
		if child == parent || child <= 1 {
			continue
		}
		byParent[parent] = append(byParent[parent], child)
	}

	var found []int
	var visit func(pid, depth int)
	visit = func(pid, depth int) {
		if depth > 32 {
			return
		}
		for _, child := range byParent[pid] {
			visit(child, depth+1)
			found = append(found, child)
		}
	}
	visit(root, 0)
	return found
}
