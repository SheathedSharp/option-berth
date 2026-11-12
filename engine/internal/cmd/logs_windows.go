//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// execDockerLogs runs docker logs for a container.
func execDockerLogs(container string) error {
	binary, err := exec.LookPath("docker")
	if err != nil {
		return fmt.Errorf("docker not found in PATH")
	}

	argv := []string{"logs"}
	if logsFollow {
		argv = append(argv, "-f")
	}
	argv = append(argv, container)

	c := exec.Command(binary, argv...)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// tailLogSources is not supported on Windows.
func tailLogSources(_ []ports.LogSource) error {
	return fmt.Errorf("log viewing is not supported on Windows for non-Docker processes")
}

// tailProcFD is not supported on Windows.
func tailProcFD(_ int) error {
	return fmt.Errorf("log viewing is not supported on Windows for non-Docker processes")
}
