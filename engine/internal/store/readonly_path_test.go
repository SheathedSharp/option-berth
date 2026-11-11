package store

import (
	"database/sql"
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

// The read-only reader is one connection: it is a small evidence path, and
// keeping one connection leaves no pool to grow behind the caller.
func TestOpenReadOnlyKeepsOneConnection(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "seed.db")
	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}
	ro, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if !ro.readOnly {
		t.Fatal("OpenReadOnly returned a writable handle")
	}
	if _, err := ro.Version(); err != nil {
		t.Fatal(err)
	}
	stats := ro.DB().Stats()
	if stats.MaxOpenConnections != 1 {
		t.Errorf("MaxOpenConnections = %d, want 1", stats.MaxOpenConnections)
	}
	if stats.Idle != 1 {
		t.Errorf("Idle = %d, want the one connection kept", stats.Idle)
	}
}

// Closing a read-only store does not checkpoint: the handle cannot change
// journal mode, so it must not run the checkpoint pragmas at all.
func TestCloseDoesNotCheckpointAReadOnlyStore(t *testing.T) {
	var queries []string
	s := &Store{
		db:       sql.OpenDB(fixedConnector{conn: fixedConn{record: &queries}}),
		readOnly: true,
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if len(queries) != 0 {
		t.Fatalf("read-only Close ran %q, want no checkpoint", queries)
	}
}

// A Windows absolute path reaches the file: URI as a drive letter after
// filepath.ToSlash. It must become a path segment, not a scheme-relative
// form; Unix absolute paths already start with a slash and pass unchanged.
func TestReadOnlyURIPathKeepsTheDriveLetterInThePath(t *testing.T) {
	for _, tc := range []struct {
		name string
		abs  string
		want string
	}{
		{"unix absolute", "/tmp/option-berth.db", "/tmp/option-berth.db"},
		{"windows drive", "C:/Users/dev/option-berth.db", "/C:/Users/dev/option-berth.db"},
		{"already rooted", "/C:/Users/dev/option-berth.db", "/C:/Users/dev/option-berth.db"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readOnlyURIPath(tc.abs); got != tc.want {
				t.Fatalf("readOnlyURIPath(%q) = %q, want %q", tc.abs, got, tc.want)
			}
		})
	}
}
