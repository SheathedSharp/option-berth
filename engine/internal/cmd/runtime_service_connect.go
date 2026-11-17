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
	unitClient, unitStarted, unitErr := startRuntimeServiceUnit(ctx, info)
	if unitStarted {
		if unitErr != nil {
			return nil, withRuntimeServiceLogHint(unitErr)
		}
		warnIfBlindRuntimeService(unitClient)
		return unitClient, nil
	}

	// Autostart is different: a runtime service created from a blind process outlives
	// the command and keeps misleading every client that connects to it.
	if os.Getenv(client.NoAutostartEnv) == "" {
		if probe := scanProbe(ctx); probe.State != ports.ObservationObservable {
			code := probe.Code
			if code == "" {
				code = string(probe.State)
			}
			return nil, failHint(code,
				"refusing to autostart a runtime service that cannot see listeners: "+probe.Detail,
				autostartRefusalHint(probe, unitErr))
		}
	}

	c, err := client.Connect(ctx, info)
	if err != nil {
		if unitErr != nil {
			// Falling back was only safe because the unit never started. Say why it did
			// not, or the second failure would hide the first.
			err = fmt.Errorf("%w (the user service did not start either: %v)", err, unitErr)
		}
		return nil, withRuntimeServiceLogHint(err)
	}
	return c, nil
}

// autostartRefusalHint names the next step after a refused autostart.
//
// The probe names its own next step (leave the sandbox, or run doctor), but the
// contract for a refused autostart is specific: the runtime service has to be started
// from a process that can observe listeners. Without a unit that means a normal
// terminal; with one it means the supervisor, because `run --detach` would put a
// second owner beside the unit the machine already has.
func autostartRefusalHint(probe ports.ProbeResult, unitErr error) string {
	supervisor := runtimeServiceSupervisor
	if !supervisor.Installed() {
		hint := "run `oberth runtime service run --detach` from a normal terminal"
		if probe.Hint != "" {
			return hint + "; " + probe.Hint
		}
		return hint + ", or allow option-berth in the sandbox"
	}
	hint := fmt.Sprintf("the user-level service unit (%s) did not start the runtime service", supervisor.Name())
	if unitErr != nil {
		hint += ": " + unitErr.Error()
	}
	if probe.Hint != "" {
		hint += "; " + probe.Hint
	}
	return hint
}

// startRuntimeServiceUnit asks the installed user-level unit to start the runtime
// service and dials what it starts.
//
// started reports that the unit owned this attempt. When it did, a failure is final:
// a detached child next to a unit would leave one machine with two runtime services
// claiming one socket and one lock. When it did not, err carries the supervisor's own
// refusal, so the caller can report both failures rather than only its own.
func startRuntimeServiceUnit(ctx context.Context, info client.ClientInfo) (conn *client.Client, started bool, err error) {
	supervisor := runtimeServiceSupervisor
	if !supervisor.Installed() || runtimesvc.EnvEnabled(os.Getenv(client.NoAutostartEnv)) {
		return nil, false, nil
	}
	if startErr := supervisor.Start(ctx); startErr != nil {
		return nil, false, startErr
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
