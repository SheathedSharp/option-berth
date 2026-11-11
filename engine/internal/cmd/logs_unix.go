//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"syscall"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// execDockerLogs execs into docker logs for a container.
func execDockerLogs(container string) error {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH")
	}

	args := append([]string{"docker", "logs"}, followArgs()...)
	args = append(args, container)

	return syscall.Exec(dockerPath, args, os.Environ())
}

// followArgs is the one place `--follow` becomes a collector flag.
func followArgs() []string {
	if logsFollow {
		return []string{"-f"}
	}
	return nil
}

// execTail replaces this process with tail over the given paths: the terminal,
// the signals and the exit status then belong to tail itself.
func execTail(paths []string) error {
	tailPath, err := exec.LookPath("tail")
	if err != nil {
		return fmt.Errorf("tail not found in PATH")
	}
	args := append([]string{"tail"}, followArgs()...)
	args = append(args, paths...)
	return syscall.Exec(tailPath, args, os.Environ())
}

// printLogTargets names what is being tailed, one dim line each, then the
// blank line that separates the header from the output.
func printLogTargets(labels []string) {
	for _, label := range labels {
		fmt.Println(display.Dim("  " + label))
	}
	fmt.Println()
}

// tailLogSources tails the log files discovered for a listener.
func tailLogSources(sources []ports.LogSource) error {
	labels := make([]string, 0, len(sources))
	paths := make([]string, 0, len(sources))
	for _, source := range sources {
		label := source.Path
		if source.FD != "" {
			label = fmt.Sprintf("%s (%s)", source.Path, source.FD)
		}
		labels = append(labels, label)
		paths = append(paths, source.Path)
	}
	printLogTargets(labels)
	return execTail(paths)
}

// tailProcFD tails a process's own stdout/stderr when no log file exists.
// Linux exposes both through /proc; a process writing to a terminal or a pipe
// has nothing to follow, and that is reported rather than guessed around.
func tailProcFD(pid int) error {
	paths := []string{}
	labels := []string{}
	for _, fd := range []struct {
		path string
		name string
	}{
		{fmt.Sprintf("/proc/%d/fd/1", pid), "stdout"},
		{fmt.Sprintf("/proc/%d/fd/2", pid), "stderr"},
	} {
		if _, err := os.Stat(fd.path); err == nil {
			paths = append(paths, fd.path)
			labels = append(labels, fmt.Sprintf("%s (%s)", fd.path, fd.name))
		}
	}

	if len(paths) == 0 {
		return fmt.Errorf("no log sources found for PID %d\n\n%s",
			pid,
			display.Dim("The process may be writing to a terminal or pipe that cannot be captured.\n"+
				"Try restarting the process with output redirected to a file:\n"+
				"  command > output.log 2>&1"))
	}

	printLogTargets(labels)
	return execTail(paths)
}
