package daemon

import (
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/state"
)

type stopSignalRegistry interface {
	StopSignalObserver([]state.Run, string) func(int)
}

// Capture the selected run identities, without marking anything stopped.
// An older extension lacking this seam gets no speculative PID-only marker.
func stopSignalObserver(rt *Runtime, snap state.Snapshot, targets []killer.Target, dryRun bool, reason string) func(int) {
	if dryRun {
		return nil
	}
	reg, ok := rt.Runs().(stopSignalRegistry)
	if !ok {
		return nil
	}
	return reg.StopSignalObserver(stopSignalOwners(snap, targets), reason)
}

func stopSignalOwners(snap state.Snapshot, targets []killer.Target) []state.Run {
	var owners []state.Run
	for _, target := range targets {
		matchedPID := false
		for _, p := range snap.Ports {
			match := false
			switch {
			case target.RunID != "":
				match = p.Run != nil && (p.Run.ID == target.RunID || p.Run.Group == target.RunID)
			case target.PID > 0:
				match = p.PID == target.PID
			case target.Port > 0:
				match = p.Port == target.Port && (target.BindAddress == "" || p.BindAddress == target.BindAddress)
			}
			if !match {
				continue
			}
			if state.IsLocalhost(p.Host) && p.Type != state.TypeDocker && p.Run != nil {
				owners = append(owners, *p.Run)
			}
			if target.PID > 0 {
				// A PID selector uses the killer's first matching listener.
				matchedPID = true
				if state.IsLocalhost(p.Host) && p.Type != state.TypeDocker && p.Run == nil {
					owners = append(owners, state.Run{RootPID: target.PID})
				}
				break
			}
		}
		if target.PID > 0 && !matchedPID {
			owners = append(owners, state.Run{RootPID: target.PID})
		}
	}
	return owners
}
