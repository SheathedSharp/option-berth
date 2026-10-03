package cmd

import (
	"context"
	"fmt"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/client"
)

// This file exists because of how the trim went (docs/history/scope.md,
// 2026-09-22): `daemonError` and `connectForWrite` used to live at the bottom of
// a file whose command (`oberth rename`) was one of the port-era tools that
// went away. Deleting the file took two helpers with it that the commands which
// stayed had been using all along — the compiler caught it, and they moved here,
// next to `dialDaemon`, which is the same subject: how a command reaches the
// daemon.
//
// The lesson is worth keeping: in this codebase a file is named after the command
// it fronts, but it may also be where that area's shared helpers landed. Read a
// file to its end before deciding it is only one thing.

// connectForWrite dials the daemon, starting it if it is not running. A daemon
// that will not start is fatal here: there is nowhere else to write to.
//
// It is a variable for the same reason dialDaemon is: the tests point the CLI
// at a daemon of their own rather than at the user's.
var connectForWrite = func(ctx context.Context) (*client.Client, error) {
	c, err := client.Connect(ctx, client.ClientInfo{Name: "cli", Version: buildinfo.Version})
	if err != nil {
		return nil, withDaemonLogHint(err)
	}
	return c, nil
}

// withDaemonLogHint points at the log, because a daemon that will not start is
// diagnosed there or nowhere.
func withDaemonLogHint(err error) error {
	return fmt.Errorf("%w\nhint: the daemon log is at %s", err, daemon.LogPath())
}
