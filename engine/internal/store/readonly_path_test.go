package store

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReadOnlyLiteralFilename(t *testing.T) {
	root := t.TempDir()
	seed := filepath.Join(root, "seed.db")
	db, err := Open(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(seed)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"ledger #1.db", "ledger %31.db", "ledger ?mode=rw.db"} {
		t.Run(name, func(t *testing.T) {
			if runtime.GOOS == "windows" && strings.Contains(name, "?") {
				t.Skip("question mark is not a Windows filename")
			}
			path := filepath.Join(root, name)
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			ro, err := OpenReadOnly(path)
			if err != nil {
				t.Fatal(err)
			}
			defer ro.Close()
			if _, err := ro.Version(); err != nil {
				t.Fatal(err)
			}
			if _, err := ro.DB().Exec(`CREATE TABLE forbidden(value TEXT)`); err == nil {
				t.Fatal("read-only connection allowed a write")
			}
		})
	}
}
