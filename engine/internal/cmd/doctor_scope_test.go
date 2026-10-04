package cmd

import (
	"context"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"os"
	"path/filepath"
	"testing"
)

func TestDoctorRepairHonorsExplicitProject(t *testing.T) {
	cwd, target := t.TempDir(), t.TempDir()
	for _, dir := range []string{cwd, target} {
		if err := os.WriteFile(filepath.Join(dir, groups.LegacyConfigName), []byte("name: demo\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	withDoctorFlags(t, func() { doctorProjectFlag = target })
	if _, err := fixLegacyConfigName(context.Background(), testCommand()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(target, groups.ConfigName)); err != nil {
		t.Fatal("selected project not repaired", err)
	}
	if _, err := os.Stat(filepath.Join(cwd, groups.LegacyConfigName)); err != nil {
		t.Fatal("unselected project was modified", err)
	}
}
