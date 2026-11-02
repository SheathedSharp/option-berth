package daemon

import (
	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// AcquireServicePort claims the port a `port: auto` service runs on, for the
// checkout the service's `oberth.yaml` sits in (dir). The claim sits under
// claims.ServiceKey, so a claimed port is skipped by every other claim and by
// `ports.next`, and the claim is refreshed
// every time the service starts.
//
// It is exported for internal/daemon/groupstart, which starts services but
// cannot reach the claims book itself.
func AcquireServicePort(rt *Runtime, dir, service string) (int, error) {
	project, worktree := claims.Identity(dir, "", "")

	// Observe first, then lock. The listener probe is a full scan (lsof, ps and
	// docker stats) and the slowest thing on this path; holding the daemon-wide
	// claims lock across it would make a concurrent `down` fail its TryLock
	// capture with "claims are changing". The snapshot is one generation old by
	// the time the table is read, which is the same race every observation on
	// this chain accepts: a port that starts listening in between is caught by
	// the listen-time bind failure, never by a longer-held lock.
	listening, err := listeningPorts(rt)
	if err != nil {
		return 0, scanFailure(err)
	}

	claimsMu.Lock()
	defer claimsMu.Unlock()
	m, err := claimsManagerWithListening(rt, listening)
	if err != nil {
		return 0, err
	}
	res, err := m.Acquire(claims.Request{
		Key:      claims.ServiceKey(project, worktree, service),
		Project:  project,
		Worktree: worktree,
		Count:    1,
	})
	if err != nil {
		return 0, claimsError("claiming a port for "+service, err)
	}
	return res.Ports[0], nil
}

// configForGroup is the oberth.yaml whose services are published under a group
// name, or nil for a group no config describes.
func configForGroup(rt *Runtime, name string) *groups.Config {
	if rt.Scanner == nil {
		return nil
	}
	for _, cfg := range rt.Scanner.Configs() {
		if rt.Scanner.GroupOf(cfg) == name {
			return cfg
		}
	}
	return nil
}
