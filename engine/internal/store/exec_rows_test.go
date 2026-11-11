package store

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
)

// fixedConnector hands every statement to one fixed connection. It lets a test
// choose what the driver reports without a real database.
type fixedConnector struct {
	conn driver.Conn
}

func (c fixedConnector) Connect(context.Context) (driver.Conn, error) { return c.conn, nil }
func (c fixedConnector) Driver() driver.Driver                        { return fixedDriver{} }

type fixedDriver struct{}

func (fixedDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("fixedConnector is opened through sql.OpenDB")
}

type fixedConn struct {
	result  driver.Result
	execErr error
}

func (fixedConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("execRows does not prepare")
}
func (fixedConn) Close() error              { return nil }
func (fixedConn) Begin() (driver.Tx, error) { return nil, errors.New("execRows does not begin") }
func (c fixedConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.execErr != nil {
		return nil, c.execErr
	}
	return c.result, nil
}

type fixedResult struct {
	affected int64
	err      error
}

func (fixedResult) LastInsertId() (int64, error) { return 0, nil }
func (r fixedResult) RowsAffected() (int64, error) {
	return r.affected, r.err
}

// storeWithConnector is a Store whose writes go to the given connection.
func storeWithConnector(t *testing.T, conn driver.Conn) *Store {
	t.Helper()
	db := sql.OpenDB(fixedConnector{conn: conn})
	t.Cleanup(func() { _ = db.Close() })
	return &Store{db: db}
}

// A write whose count the driver can report gives that count back.
func TestExecRowsReturnsTheAffectedCount(t *testing.T) {
	s := storeWithConnector(t, fixedConn{result: fixedResult{affected: 5}})

	n, err := s.execRows(`UPDATE ignored SET value = ?`, 1)
	if err != nil {
		t.Fatalf("execRows: %v", err)
	}
	if n != 5 {
		t.Fatalf("affected = %d, want 5", n)
	}
}

// A driver that cannot report the count did still run the write, so the write
// is not a failure and the count is zero.
func TestExecRowsKeepsTheWriteWhenTheDriverCannotReportTheCount(t *testing.T) {
	s := storeWithConnector(t, fixedConn{
		result: fixedResult{err: errors.New("count unavailable")},
	})

	n, err := s.execRows(`UPDATE ignored SET value = ?`, 1)
	if err != nil {
		t.Fatalf("a count the driver cannot report must not fail the write: %v", err)
	}
	if n != 0 {
		t.Fatalf("affected = %d, want 0", n)
	}
}

// A write that failed is reported with the driver's error and no count.
func TestExecRowsReportsAWriteFailure(t *testing.T) {
	writeErr := errors.New("disk is full")
	s := storeWithConnector(t, fixedConn{execErr: writeErr})

	n, err := s.execRows(`UPDATE ignored SET value = ?`, 1)
	if !errors.Is(err, writeErr) {
		t.Fatalf("err = %v, want %v", err, writeErr)
	}
	if n != 0 {
		t.Fatalf("affected = %d, want 0", n)
	}
}
