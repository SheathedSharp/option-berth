package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
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
	info := client.ClientInfo{Name: "cli", Version: buildinfo.Version}

	// A daemon that is already running is used as-is. If it is blind, warn
	// once and let the command's own observation error carry the next step;
	// blocking an unrelated write (workplace, events) would not help.
	if c, err := client.Dial(ctx, info); err == nil {
		if obs := c.Hello().Observation; obs != nil && obs.State != string(ports.ObservationObservable) {
			warnBlindDaemon(obs)
		}
		return c, nil
	}

	// Autostart is different: a daemon created from a blind process outlives
	// the command and keeps misleading every client that connects to it.
	if os.Getenv(client.NoAutostartEnv) == "" {
		if probe := scanProbe(ctx); probe.State != ports.ObservationObservable {
			code := probe.Code
			if code == "" {
				code = string(probe.State)
			}
			hint := probe.Hint
			if hint == "" {
				hint = "run `oberth serve --detach` from a normal terminal, or allow option-berth in the sandbox"
			}
			return nil, failHint(code,
				"refusing to autostart a daemon that cannot see listeners: "+probe.Detail, hint)
		}
	}

	c, err := client.Connect(ctx, info)
	if err != nil {
		return nil, withDaemonLogHint(err)
	}
	return c, nil
}

var blindDaemonNoteOnce sync.Once

func warnBlindDaemon(obs *rpc.ObservationStatus) {
	blindDaemonNoteOnce.Do(func() {
		code := obs.Code
		if code == "" {
			code = ports.CodeObservationBlind
		}
		fmt.Fprintf(os.Stderr, "%s\n", display.Dim(
			"note: daemon cannot see listeners ("+code+"); restart it with "+
				"`oberth daemon restart` from a normal terminal"))
	})
}

// withDaemonLogHint points at the log, because a daemon that will not start is
// diagnosed there or nowhere.
func withDaemonLogHint(err error) error {
	return fmt.Errorf("%w\nhint: the daemon log is at %s", err, daemon.LogPath())
}
