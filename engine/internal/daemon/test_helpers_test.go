package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/groups"
)

func writeConfig(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, groups.ConfigName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
