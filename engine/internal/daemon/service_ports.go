package daemon

import (
	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// AcquireServicePort books the port a `port: auto` service runs on, for the
// checkout the service's `oberth.yaml` sits in (dir). The reservation is keyed
// by that checkout and the service name, so the service gets the same port
// every time it starts, a sibling checkout never gets it, and every other
// reservation and `ports.next` step around it. The lease is renewed on every
// start.
//
// It is exported for internal/daemon/groupstart, which starts services but
// cannot reach the reservations book itself.
func AcquireServicePort(rt *Runtime, dir, service string) (int, error) {
	project, worktree := claims.Identity(dir)
	address := claims.Address{Project: project, Worktree: worktree, Service: service}

	// Observe first, then lock. The listener probe is a full scan (lsof, ps and
	// docker stats) and the slowest thing on this path; holding the daemon-wide
	// claims lock across it would make a concurrent `down` fail its TryLock
	// capture with "claims are changing". The snapshot is one generation old by
	// the time the table is read, which is the same race every observation on
	// this chain accepts: a port that starts listening in between is caught by
	// the listen-time bind failure, never by a longer-held lock.
	listening, narrow, err := listeningState(rt)
	if err != nil {
		return 0, scanFailure(err)
	}

	claimsMu.Lock()
	defer claimsMu.Unlock()
	book, err := reservationsBook(rt, listening, narrow)
	if err != nil {
		return 0, err
	}
	reserved, err := book.Reserve(address)
	if err != nil {
		return 0, claimsError("reserving a port for "+service, err)
	}
	return reserved.Port, nil
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
