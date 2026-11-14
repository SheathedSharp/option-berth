package runtimesvc

import (
	"os"
	"path/filepath"
	"testing"
)

// Pure advisory-lock regressions: no Server, DB, process scan, socket or child.
func TestStorageLeaseExcludesDifferentEndpoints(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(root, "data.db")
	lease, err := acquireStorageLease(root, db, filepath.Join(t.TempDir(), "a.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if second, err := acquireStorageLease(root, db, filepath.Join(t.TempDir(), "b.sock"), nil); err == nil {
		second.Release()
		t.Fatal("different endpoints admitted two writers")
	} else if !IsAlreadyRunning(err) {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestStorageLeaseExcludesSharedDatabaseAcrossHomes(t *testing.T) {
	db := filepath.Join(t.TempDir(), "shared.db")
	lease, err := acquireStorageLease(t.TempDir(), db, filepath.Join(t.TempDir(), "a.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if second, err := acquireStorageLease(t.TempDir(), db, filepath.Join(t.TempDir(), "b.sock"), nil); err == nil {
		second.Release()
		t.Fatal("different state directories admitted two writers to one database")
	} else if !IsAlreadyRunning(err) {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestStorageLeaseRejectsAndBlocksPreviousHost(t *testing.T) {
	for _, location := range []string{"state", "endpoint", "additional"} {
		t.Run(location, func(t *testing.T) {
			root, endpoint, additional := t.TempDir(), t.TempDir(), t.TempDir()
			dirs := map[string]string{"state": root, "endpoint": endpoint, "additional": additional}
			// This retired filename is a safety exclusion guard, never a transport.
			oldPath := filepath.Join(dirs[location], "daemon.lock")
			old, err := AcquireLock(oldPath)
			if err != nil {
				t.Fatal(err)
			}
			db, socket := filepath.Join(root, "data.db"), filepath.Join(endpoint, "new.sock")
			if lease, err := acquireStorageLease(root, db, socket, []string{additional}); err == nil {
				lease.Release()
				t.Fatal("admitted writer while previous host holds guard")
			} else if !IsAlreadyRunning(err) {
				t.Fatalf("wrong error: %v", err)
			}
			if err := old.Release(); err != nil {
				t.Fatal(err)
			}
			lease, err := acquireStorageLease(root, db, socket, []string{additional})
			if err != nil {
				t.Fatalf("failed acquisition leaked an earlier lock: %v", err)
			}
			defer lease.Release()
			if old, err := AcquireLock(oldPath); err == nil {
				old.Release()
				t.Fatal("previous host could start while new host owns storage")
			}
		})
	}
}

func TestStorageLeasePreservesGuardInodeAndReleases(t *testing.T) {
	root := t.TempDir()
	db, socket := filepath.Join(root, "data.db"), filepath.Join(root, "new.sock")
	lease, err := acquireStorageLease(root, db, socket, []string{root, root})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(filepath.Join(root, "storage.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(filepath.Join(root, "storage.lock"))
	if err != nil || !os.SameFile(before, after) {
		t.Fatalf("lock inode changed: %v", err)
	}
	successor, err := acquireStorageLease(root, db, socket, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer successor.Release()
}

func TestStorageLeaseResolvesDatabaseSymlink(t *testing.T) {
	target := filepath.Join(t.TempDir(), "data.db")
	if err := os.WriteFile(target, []byte("fixture-not-a-real-database"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias.db")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	first, err := acquireStorageLease(t.TempDir(), target, filepath.Join(t.TempDir(), "a.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Release()
	if second, err := acquireStorageLease(t.TempDir(), alias, filepath.Join(t.TempDir(), "b.sock"), nil); err == nil {
		second.Release()
		t.Fatal("a DB symlink admitted a second writer")
	} else if !IsAlreadyRunning(err) {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestStorageLeaseHoldsBothNeutralKeys(t *testing.T) {
	root := t.TempDir()
	db := filepath.Join(t.TempDir(), "data.db")
	lease, err := acquireStorageLease(root, db, filepath.Join(t.TempDir(), "service.sock"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, path := range []string{filepath.Join(root, "storage.lock"), db + ".storage.lock"} {
		if other, err := AcquireLock(path); err == nil {
			other.Release()
			t.Fatalf("neutral lease not held: %s", path)
		}
	}
}

func TestStorageLeaseRejectsRelativePreviousGuards(t *testing.T) {
	t.Chdir(t.TempDir()) // even a regression cannot write a guard in the checkout.
	for _, previous := range []string{"", "relative"} {
		root := t.TempDir()
		if lease, err := acquireStorageLease(root, filepath.Join(root, "data.db"), filepath.Join(root, "service.sock"), []string{previous}); err == nil {
			lease.Release()
			t.Fatalf("invalid previous guard directory was accepted: %q", previous)
		}
		if _, err := os.Stat(filepath.Join(root, "storage.lock")); !os.IsNotExist(err) {
			t.Fatalf("validation happened after locks were opened: %v", err)
		}
	}
}
