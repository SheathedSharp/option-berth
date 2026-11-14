package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	sqlite "modernc.org/sqlite"
)

// ErrStorageOwned means another SQLite connection owns the database. Callers
// must not report empty history or silently start a storeless host instead.
var ErrStorageOwned = errors.New("runtime storage is already owned")

// OpenOwned takes SQLite's own lifetime exclusive ownership, not just an
// advisory sidecar understood by new binaries. Unmodified older Store writers
// therefore cannot join it. Commits remain durable; no transaction is held open.
// External readers must use the owner's RPC while it runs.
func OpenOwned(path string) (*Store, error) {
	path = databasePath(path)
	var err error
	path, err = filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if err := ValidateStorageFilesystem(path); err != nil {
		return nil, err
	}
	s, err := OpenWith(path, Options{BusyTimeout: 50 * time.Millisecond, owned: true})
	return s, ownershipError(path, err)
}

func ownershipError(path string, err error) error {
	if err == nil {
		return nil
	}
	var code *sqlite.Error
	if errors.As(err, &code) && (code.Code()&0xff == 5 || code.Code()&0xff == 6) {
		return fmt.Errorf("%w (%s): stop the previous Runtime Service explicitly with its matching CLI before switching; use the active owner's RPC for reads: %v", ErrStorageOwned, path, err)
	}
	return err
}

// VersionContext bounds a read waiting for the owning connection as well as SQL.
func (s *Store) VersionContext(ctx context.Context) (int, error) {
	var version int
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_version`).Scan(&version)
	return version, err
}
