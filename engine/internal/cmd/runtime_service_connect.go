package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// This file exists because of how the trim went (docs/history/scope.md,
// 2026-09-22): `runtimeServiceError` and `connectForWrite` used to live at the bottom of
// a file whose command (`oberth rename`) was one of the port-era tools that
// went away. Deleting the file took two helpers with it that the commands which
// stayed had been using all along — the compiler caught it, and they moved here,
// next to `dialRuntimeService`, which is the same subject: how a command reaches the
// runtime.service.
//
// The lesson is worth keeping: in this codebase a file is named after the command
// it fronts, but it may also be where that area's shared helpers landed. Read a
// file to its end before deciding it is only one thing.

// connectForWrite dials the runtime service, starting it if it is not running. A runtime service
// that will not start is fatal here: there is nowhere else to write to.
//
// It is a variable for the same reason dialRuntimeService is: the tests point the CLI
// at a runtime service of their own rather than at the user's.
var connectForWrite = func(ctx context.Context) (*client.Client, error) {
	info := client.ClientInfo{Name: "cli", Version: buildinfo.Version}

	// A runtime service that is already running is used as-is. If it is blind, warn
	// once and let the command's own observation error carry the next step;
	// blocking an unrelated write (workplace, events) would not help.
	if c, err := client.Dial(ctx, info); err == nil {
		warnIfBlindRuntimeService(c)
		return c, nil
	}

	// Where the user-level unit is installed, the supervisor is the only thing that
	// may start a runtime service. A detached child beside a unit would be a second
	// owner of one socket and one lock — exactly what the unit exists to rule out.
	unitClient, owned, unitErr := startRuntimeServiceUnit(ctx, info)
	if owned {
		if unitErr != nil {
			return nil, withRuntimeServiceLogHint(unitErr)
		}
		warnIfBlindRuntimeService(unitClient)
		return unitClient, nil
	}

	// Autostart is different: a runtime service created from a blind process outlives
	// the command and keeps misleading every client that connects to it. Reaching
	// this point means no unit is installed: an installed one owns the attempt above.
	if os.Getenv(client.NoAutostartEnv) == "" {
		if probe := scanProbe(ctx); probe.State != ports.ObservationObservable {
			code := probe.Code
			if code == "" {
				code = string(probe.State)
			}
			hint := "run `oberth runtime service run --detach` from a normal terminal"
			if probe.Hint != "" {
				hint += "; " + probe.Hint
			} else {
				hint += ", or allow option-berth in the sandbox"
			}
			return nil, failHint(code,
				"refusing to autostart a runtime service that cannot see listeners: "+probe.Detail, hint)
		}
	}

	c, err := client.Connect(ctx, info)
	if err != nil {
		return nil, withRuntimeServiceLogHint(err)
	}
	return c, nil
}

// startRuntimeServiceUnit asks the installed user-level unit to start the runtime
// service and dials what it starts.
//
// owned reports that the unit owned this attempt. It is true whenever the unit is
// installed, including when Start failed: falling back to a detached child would put
// a second owner beside the unit the machine already has.
func startRuntimeServiceUnit(ctx context.Context, info client.ClientInfo) (conn *client.Client, owned bool, err error) {
	supervisor := runtimeServiceSupervisor
	if !supervisor.Installed() || runtimesvc.EnvEnabled(os.Getenv(client.NoAutostartEnv)) {
		return nil, false, nil
	}
	if startErr := supervisor.Start(ctx); startErr != nil {
		return nil, true, startErr
	}
	socket := runtimesvc.SocketPath()
	if waitErr := client.WaitForSocket(ctx, socket, supervisorSocketTimeout); waitErr != nil {
		return nil, true, waitErr
	}
	// The unit started something; dial it, and never let the dial start its own.
	info.NoAutostart = true
	c, dialErr := client.Connect(ctx, info)
	if dialErr != nil {
		return nil, true, dialErr
	}
	return c, true, nil
}

var blindRuntimeServiceNoteOnce sync.Once

// warnIfBlindRuntimeService warns once about a runtime service that cannot see
// listeners. The warning is the same whichever way the process was started.
func warnIfBlindRuntimeService(c *client.Client) {
	if obs := c.Hello().Observation; obs != nil && obs.State != string(ports.ObservationObservable) {
		warnBlindRuntimeService(obs)
	}
}

func warnBlindRuntimeService(obs *rpc.ObservationStatus) {
	blindRuntimeServiceNoteOnce.Do(func() {
		code := obs.Code
		if code == "" {
			code = ports.CodeObservationBlind
		}
		fmt.Fprintf(os.Stderr, "%s\n", display.Dim(
			"note: runtime service cannot see listeners ("+code+"); restart it with "+
				"`oberth runtime service restart` from a normal terminal"))
	})
}

// withRuntimeServiceLogHint points at the log, because a runtime service that will not start is
// diagnosed there or nowhere.
func withRuntimeServiceLogHint(err error) error {
	return fmt.Errorf("%w\nhint: the runtime service log is at %s", err, runtimesvc.LogPath())
}
