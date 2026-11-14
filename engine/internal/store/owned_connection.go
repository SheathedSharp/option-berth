package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"sync"
)

// Once a lifetime owner connection is lost, silently opening a replacement
// could steal ownership back after an old writer has acquired SQLite. Fail
// closed instead. No new transaction/connection is permitted on this Store.
var ErrStorageOwnershipLost = fmt.Errorf("%w: owning SQLite connection was lost; stop and restart this Runtime Service explicitly", ErrStorageOwned)

type singleOwnerConnector struct {
	mu     sync.Mutex
	used   bool
	driver driver.Driver
	dsn    string
}

func (c *singleOwnerConnector) Connect(ctx context.Context) (driver.Conn, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.used {
		return nil, ErrStorageOwnershipLost
	}
	c.used = true
	return c.driver.Open(c.dsn)
}
func (c *singleOwnerConnector) Driver() driver.Driver { return c.driver }
func newOwnedDB(dsn string) (*sql.DB, error) {
	registered, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	driver := registered.Driver()
	if err := registered.Close(); err != nil {
		return nil, err
	}
	return sql.OpenDB(&singleOwnerConnector{driver: driver, dsn: dsn}), nil
}
