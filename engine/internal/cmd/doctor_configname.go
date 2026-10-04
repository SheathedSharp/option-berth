package cmd

import (
	"context"
	"fmt"
	"github.com/sheathedsharp/option-berth/internal/doctor"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

// Repair uses the same explicit project and nearest-manifest selection as the
// diagnosis. A filesystem rename must never stage or otherwise write to Git.
func fixLegacyConfigName(context.Context, *cobra.Command) (string, error) {
	project, err := doctorProject()
	if err != nil {
		return "", err
	}
	present := doctor.ProjectConfigFiles(project)
	if len(present) == 0 {
		return "", fmt.Errorf("no %s found for %s", groups.LegacyConfigName, project)
	}
	return renameLegacyConfig(filepath.Dir(present[0]), present)
}

func renameLegacyConfig(dir string, present []string) (string, error) {
	if len(present) != 1 {
		return "", fmt.Errorf("%s must have exactly one config file; reconcile it manually", dir)
	}
	from := present[0]
	if !groups.IsLegacyName(filepath.Base(from)) {
		return "", fmt.Errorf("%s already uses a current name", from)
	}
	info, err := os.Lstat(from)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s is not a regular file; rename it manually", from)
	}
	to := filepath.Join(dir, groups.ConfigName)
	// Link is an atomic no-replace publication. Never fall back to an overwriting
	// rename if the filesystem cannot support it; both names remain recoverable.
	if err := os.Link(from, to); err != nil {
		return "", fmt.Errorf("cannot create %s without replacing it: %w", to, err)
	}
	if err := os.Remove(from); err != nil {
		return "", fmt.Errorf("created %s but could not remove %s; both names remain: %w", to, from, err)
	}
	return fmt.Sprintf("renamed %s to %s; Git index unchanged", from, to), nil
}
