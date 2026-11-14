package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOwnedStorageFailureReleasesConnectionAndPreservesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "literal ?#&%.db")
	original := chmodFile
	chmodFile = func(string, os.FileMode) error { return errors.New("injected owned permission failure") }
	_, err := OpenOwned(path)
	chmodFile = original
	if err == nil {
		t.Fatal("accepted permission failure")
	}
	db, err := OpenOwned(path)
	if err != nil {
		t.Fatalf("failed owned open leaked SQLite ownership: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("SQLite did not use the literal path: %v", err)
	}
	if err := db.AddRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	header, err := os.ReadFile(path)
	if err != nil || len(header) < 16 || string(header[:16]) != "SQLite format 3\x00" {
		t.Fatalf("configured literal database path was not actually used: %q %v", header, err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close not idempotent: %v", err)
	}
	db, err = OpenOwned(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	roots, err := db.Roots()
	if err != nil || len(roots) != 1 {
		t.Fatalf("committed writes lost: %v %v", roots, err)
	}
}
func TestOwnedStorageFilesystemClassificationFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		os   string
		kind uint64
		want bool
	}{
		{"darwin", 0, false}, {"darwin", 0x1000, true},
		{"linux", 0xef53, true}, {"linux", 0x794c7630, true}, {"linux", 0x6969, false}, {"linux", 0xff534d42, false}, {"linux", 0x65735546, false}, {"linux", 0x01021997, false}, {"linux", 0, false},
		{"windows", 3, true}, {"windows", 4, false}, {"windows", 0, false}, {"other", 3, false},
	} {
		if got := localStorageFilesystem(tc.os, tc.kind); got != tc.want {
			t.Fatalf("%s/%#x=%v want %v", tc.os, tc.kind, got, tc.want)
		}
	}
}

func TestOwnedConnectionCannotSilentlyReopenAfterLoss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	owner, err := OpenOwned(path)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := owner.AddRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	owner.DB().SetMaxIdleConns(0) // deliberate loss of the private owner connection
	if _, err := owner.Roots(); !errors.Is(err, ErrStorageOwnershipLost) {
		t.Fatalf("silently reacquired lost ownership: %v", err)
	}
	old, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	if err := old.AddRoot(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := owner.AddRoot(t.TempDir()); !errors.Is(err, ErrStorageOwnershipLost) {
		t.Fatalf("old writer and recovered candidate could coexist: %v", err)
	}
}

func TestDarwinRemoteAndUserSpaceFilesystemsRefused(t *testing.T) {
	for _, name := range []string{"nfs", "smbfs", "macfuse", "unknown"} {
		if localDarwinStorage(name, 0x1000) {
			t.Fatalf("accepted unsupported filesystem %q", name)
		}
	}
	if !localDarwinStorage("apfs", 0x1000) || localDarwinStorage("apfs", 0) {
		t.Fatal("local-volume requirement drifted")
	}
}
