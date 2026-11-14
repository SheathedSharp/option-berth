package runtimesvc

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// storageLease owns all storage and cross-generation exclusion handles. Its
// lifetime encloses store opening, restoration, producers, and store closing.
// Endpoint ownership alone cannot protect a DB shared by different sockets.
type storageLease struct {
	locks    []*Lock
	database *store.Store
}

// acquireStorageLease never dials an older host or removes any lock inode.
// previousDirs must include the previous default endpoint directory and any
// explicitly configured previous endpoint locations. SQLite ownership, not this
// directory inventory, excludes older writers at arbitrary endpoint locations.
func acquireStorageLease(stateDir, dbPath, socket string, previousDirs []string) (*storageLease, error) {
	// Validate before creating even a neutral lock: a relative old guard would
	// resolve against the host working directory and could protect the wrong DB.
	for _, dir := range previousDirs {
		if dir == "" || !filepath.IsAbs(dir) {
			return nil, fmt.Errorf("runtime_service.previous_service_lock_dirs requires non-empty absolute directories; storage was not opened")
		}
	}
	// Refuse unsupported network/user-space storage before creating sidecars.
	locations := append([]string{stateDir, dbPath}, previousDirs...)
	if !isNamedPipe(socket) {
		locations = append(locations, filepath.Dir(socket))
	}
	for _, location := range locations {
		if err := store.ValidateStorageFilesystem(location); err != nil {
			return nil, err
		}
	}
	// Existing DB symlinks must name the same lease as the underlying file.
	if resolved, err := filepath.EvalSymlinks(dbPath); err == nil {
		dbPath = resolved
	}
	paths := []string{filepath.Join(stateDir, "storage.lock"), dbPath + ".storage.lock"}
	dirs := append([]string{stateDir, filepath.Dir(dbPath)}, previousDirs...)
	if !isNamedPipe(socket) {
		dirs = append(dirs, filepath.Dir(socket))
	}
	for _, dir := range dirs {
		// v2's lock name remains only as a held safety interlock. It is not the
		// new endpoint lock and never enables old CLI/RPC/socket fallback.
		paths = append(paths, filepath.Join(dir, "daemon.lock"))
	}
	sort.Strings(paths)
	lease := &storageLease{}
	seen := map[string]bool{}
	for _, path := range paths {
		abs, err := filepath.Abs(path)
		if err != nil {
			_ = lease.Release()
			return nil, err
		}
		// Resolve existing directory aliases before deduplication. AcquireLock
		// creates missing private directories; no file is unlinked on release.
		if dir, err := filepath.EvalSymlinks(filepath.Dir(abs)); err == nil {
			abs = filepath.Join(dir, filepath.Base(abs))
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		lock, err := AcquireLock(abs)
		if err != nil {
			_ = lease.Release()
			return nil, fmt.Errorf("runtime storage is already owned or unavailable; stop the previous Runtime Service explicitly before switching versions: %w", err)
		}
		lease.locks = append(lease.locks, lock)
	}
	database, err := store.OpenOwned(dbPath)
	if err != nil {
		_ = lease.Release()
		return nil, fmt.Errorf("claiming Runtime Service database ownership: %w", err)
	}
	lease.database = database
	return lease, nil
}

func isNamedPipe(socket string) bool {
	return len(socket) >= 2 && socket[:2] == `\\`
}

func (l *storageLease) Release() error {
	if l == nil {
		return nil
	}
	var errs []error
	if l.database != nil {
		errs = append(errs, l.database.Close())
		l.database = nil
	}
	for i := len(l.locks) - 1; i >= 0; i-- {
		errs = append(errs, l.locks[i].Release())
	}
	l.locks = nil
	return errors.Join(errs...)
}
