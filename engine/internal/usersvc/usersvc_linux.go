//go:build linux

package usersvc

import (
	"context"
	"os"
	"path/filepath"
)

// UnitName is the systemd user unit install.sh writes.
const UnitName = "option-berth-runtime.service"

// current is the Linux supervisor.
var current Supervisor = systemd{}

type systemd struct{}

func (systemd) Name() string { return "systemd" }

// UnitPath is the per-user unit file. systemd --user reads
// $XDG_CONFIG_HOME/systemd/user and falls back to ~/.config when the variable is
// unset, which is exactly what this function does.
func (systemd) UnitPath() string {
	dir := os.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil || home == "" {
			return ""
		}
		dir = filepath.Join(home, ".config")
	}
	return filepath.Join(dir, "systemd", "user", UnitName)
}

func (s systemd) Installed() bool {
	path := s.UnitPath()
	if path == "" {
		return false
	}
	_, err := os.Stat(path)
	return err == nil
}

// Active asks systemd whether the unit is running. `is-active --quiet` answers
// with its exit status alone, and "inactive" is that answer rather than an
// error to report.
func (systemd) Active(ctx context.Context) (bool, error) {
	return run(ctx, "systemctl", "--user", "is-active", "--quiet", UnitName) == nil, nil
}

func (systemd) Start(ctx context.Context) error {
	return run(ctx, "systemctl", "--user", "start", UnitName)
}

func (systemd) Restart(ctx context.Context) error {
	return run(ctx, "systemctl", "--user", "restart", UnitName)
}
