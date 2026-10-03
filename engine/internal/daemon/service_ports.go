package daemon

import (
	"github.com/sheathedsharp/option-berth/internal/claims"
	"github.com/sheathedsharp/option-berth/internal/groups"
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
	claimsMu.Lock()
	defer claimsMu.Unlock()
	m, err := claimsManager(rt)
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
