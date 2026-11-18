//go:build windows

package cmd

import (
	"errors"
	"os/exec"
)

func detachClaudeObserver(*exec.Cmd) error {
	return errors.New("native Claude lifecycle observation is supported on macOS/Linux only")
}
