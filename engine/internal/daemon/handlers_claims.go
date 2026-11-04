package daemon

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Every claims call runs under one lock: the acquire probe reads the table,
// picks ports and writes them back, and two of those interleaving would hand
// the same port to two keys. The calls are rare and short, so a daemon-wide
// mutex is the whole concurrency story. (The unique constraint on
// claims.port is the second line of defence, for a second daemon on the same
// database.)
var claimsMu sync.Mutex

// claimsManager builds the manager for this request, or reports the daemon
// that came up without a database.
func claimsManager(rt *Runtime) (*claims.Manager, error) {
	st := rt.Store
	if st == nil {
		return nil, errNoStore()
	}
	listening, narrow, err := listeningState(rt)
	if err != nil {
		return nil, scanFailure(err)
	}
	return claimsManagerWithListening(rt, listening, narrow)
}

// claimsManagerWithListening is claimsManager for a caller that has already
// observed the listener table. The probe is a full scan, so a caller holding
// claimsMu must hand the observation in rather than let the manager run it
// under the lock: no lock on this path may span IO.
//
// narrow says the table cannot see every socket owner. A claim then verifies
// each candidate by binding it; absence from the table is unknown, not free.
func claimsManagerWithListening(rt *Runtime, listening map[int]bool, narrow bool) (*claims.Manager, error) {
	st := rt.Store
	if st == nil {
		return nil, errNoStore()
	}
	return claims.New(st.Claims(), claims.Options{
		Listening:    func() (map[int]bool, error) { return listening, nil },
		Narrow:       narrow,
		Bindable:     canBindPort,
		DefaultCount: func(project string) int { return worktreePorts(rt, project) },
	}), nil
}

// worktreePorts answers claims.Options.DefaultCount: the worktree_ports of the
// `oberth.yaml` belonging to a claim's project, or zero when no config says
// (step 5A.7).
//
// A claim names its project the way claims.Identity does — the main
// checkout's directory name, which every linked worktree of it shares — so a
// config belongs to the project when claims.Identity of the config's own
// directory gives the same name. A config whose group name is the project
// matches too, for a caller that passed the group name as project.
//
// Several configs can match: the main checkout's file and each worktree's
// checked-out copy of it, or a monorepo's nested files. The main checkout's
// repository-root file wins, because that is the file the desktop's group page
// edits; see configRank.
func worktreePorts(rt *Runtime, project string) int {
	if rt.Scanner == nil || project == "" {
		return 0
	}
	best, bestRank := 0, -1
	for _, cfg := range rt.Scanner.Configs() {
		if cfg.WorktreePorts == nil {
			continue
		}
		if r := configRank(cfg, project); r > bestRank {
			best, bestRank = *cfg.WorktreePorts, r
		}
	}
	return best
}

// configRank orders the configs that could answer for a project: -1 is no
// match, 0 a match by group name only, and 1-4 a match by checkout, higher for
// the main checkout over a linked worktree and for the repository root over a
// nested directory.
func configRank(cfg *groups.Config, project string) int {
	if name, _ := claims.Identity(cfg.Dir, "", ""); name != project {
		if cfg.Name == project {
			return 0
		}
		return -1
	}
	root, linked, inRepo := groups.Find(cfg.Dir)
	rank := 1
	if !inRepo || linked == "" {
		rank += 2
	}
	if !inRepo || cfg.Dir == root {
		rank++
	}
	return rank
}

// listeningState is the live scan as a set plus whether that table can see
// every socket owner. The set is how the claim probe skips ports something is
// already serving on; the coverage says whether absence from the set is a fact.
func listeningState(rt *Runtime) (map[int]bool, bool, error) {
	snap, err := readSnapshot(rt, scanner.Include{})
	if err != nil {
		return nil, false, err
	}
	out := make(map[int]bool, len(snap.Ports))
	for _, row := range snap.Ports {
		out[row.Port] = true
	}
	return out, snap.Observation.Narrow(), nil
}

// canBindPort is the positive test for "free": bind each address a local
// service may use and close immediately. A claim that hands out a busy port
// ends as EADDRINUSE in the service, and when the table cannot see other
// users' sockets a bind failure is the only evidence that the port is taken.
//
// All four addresses are probed because a specific-address holder and a
// wildcard holder do not conflict with each other on macOS: only the address
// the service itself will bind can prove the port usable for it.
func canBindPort(port int) bool {
	probes := []struct{ network, host string }{
		{"tcp4", "0.0.0.0"},
		{"tcp4", "127.0.0.1"},
	}
	if ipv6Usable() {
		probes = append(probes,
			struct{ network, host string }{"tcp6", "::"},
			struct{ network, host string }{"tcp6", "::1"})
	}
	for _, probe := range probes {
		ln, err := net.Listen(probe.network, net.JoinHostPort(probe.host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// ipv6Usable keeps a host without IPv6 from losing every allocation: only a
// family that exists can answer for a port.
func ipv6Usable() bool {
	ln, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// claimsError maps the manager's failures onto the contract's error codes:
// claim_conflict (1201) for a port another key holds, not_found when the range
// is full, invalid_params for a request that names nothing.
func claimsError(what string, err error) error {
	switch {
	case errors.Is(err, store.ErrClaimed):
		return rpc.NewError(rpc.CodeClaimConflict, err.Error(),
			"another key holds that port; release it or ask for a different count")
	case errors.Is(err, claims.ErrExhausted):
		return rpc.NewError(rpc.CodeNotFound, err.Error(),
			"release stale claims with `option-berth claim --release` or widen the range")
	case errors.Is(err, claims.ErrNoProject):
		return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			`send {"project": "myapp", "worktree": "main"} or {"key": "myapp/main"}`)
	default:
		return storeError(what, err)
	}
}
