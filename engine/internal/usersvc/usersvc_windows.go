//go:build windows

package usersvc

import (
	"context"
	"errors"
)

// ErrNoUserService is what Start and Restart return on Windows. The runtime
// service there is still started with `run --detach`, and pretending a unit
// exists would send a caller looking for a file that is never written.
var ErrNoUserService = errors.New(
	"no user-level service manager on Windows: start the runtime service with `oberth runtime service run --detach`")

// current is the Windows supervisor: it installs nothing and reports itself
// uninstalled, so every caller keeps the detached path it had before.
var current Supervisor = none{}

type none struct{}

func (none) Name() string     { return "none" }
func (none) UnitPath() string { return "" }

func (none) Installed() bool                      { return false }
func (none) Active(context.Context) (bool, error) { return false, nil }
func (none) Start(context.Context) error          { return ErrNoUserService }
func (none) Restart(context.Context) error        { return ErrNoUserService }
