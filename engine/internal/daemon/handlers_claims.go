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

// Claim operations serialize here. An acquire reads the table, picks ports and
// writes them back; two of those interleaving would hand one port to two keys.
// Claims are rare and short, so one daemon-wide mutex is the whole strategy.
// The unique constraint on claims.port is the second line of defence, for a
// second daemon sharing the database.
var claimsMu sync.Mutex

// claimsManager builds the manager for one request, or reports a daemon that
// came up without a database.
func claimsManager(rt *Runtime) (*claims.Manager, error) {
	if rt.Store == nil {
		return nil, errNoStore()
	}
	listening, narrow, err := listeningState(rt)
	if err != nil {
		return nil, scanFailure(err)
	}
	return claimsManagerWithListening(rt, listening, narrow)
}

// claimsManagerWithListening serves callers that already observed the
// listener table. The observation is a full scan, and a caller holding
// claimsMu must hand it in rather than let the manager run it under the lock:
// no lock here may span IO.
//
// narrow marks a table that cannot see every socket owner. Each candidate is
// then verified by binding it, because absence from that table is unknown, not
// free.
func claimsManagerWithListening(rt *Runtime, listening map[int]bool, narrow bool) (*claims.Manager, error) {
	if rt.Store == nil {
		return nil, errNoStore()
	}
	return claims.New(rt.Store.Claims(), claims.Options{
		Listening:    func() (map[int]bool, error) { return listening, nil },
		Narrow:       narrow,
		Bindable:     canBindPort,
		DefaultCount: func(project string) int { return worktreePorts(rt, project) },
	}), nil
}

// worktreePorts answers claims.Options.DefaultCount: the worktree_ports of
// the `oberth.yaml` that belongs to a claim's project, or zero when none does.
//
// A claim names its project the way claims.Identity does — the main
// checkout's directory name, shared by every linked worktree — so a config
// belongs to the project when claims.Identity of the config's own directory
// agrees. A config whose group name equals the project also matches, for a
// caller that passed a group name.
//
// Several configs can match: the main checkout's file and each worktree's
// checked-out copy, or nested files in a monorepo. configRank decides which
// wins; the main checkout's repository-root file outranks the rest because
// that is the file the desktop's group page edits.
func worktreePorts(rt *Runtime, project string) int {
	if rt.Scanner == nil || project == "" {
		return 0
	}
	chosen, chosenRank := 0, -1
	for _, config := range rt.Scanner.Configs() {
		if config.WorktreePorts == nil {
			continue
		}
		if rank := configRank(config, project); rank > chosenRank {
			chosen, chosenRank = *config.WorktreePorts, rank
		}
	}
	return chosen
}

// configRank orders the configs that could answer for a project. A group-name
// match is weakest; a checkout match ranks higher for the main checkout over a
// linked worktree and for the repository root over a nested directory.
func configRank(cfg *groups.Config, project string) int {
	name, _ := claims.Identity(cfg.Dir, "", "")
	if name != project {
		if cfg.Name == project {
			return 0
		}
		return -1
	}

	root, linked, inRepo := groups.Find(cfg.Dir)
	switch {
	case !inRepo:
		return 4
	case linked == "" && cfg.Dir == root:
		return 4
	case linked == "":
		return 3
	case cfg.Dir == root:
		return 2
	default:
		return 1
	}
}

// listeningState returns the live scan as a port set plus whether that table
// can see every socket owner. The set lets the probe skip ports something
// already serves on; the coverage decides whether absence from the set counts
// as a fact.
func listeningState(rt *Runtime) (map[int]bool, bool, error) {
	snapshot, err := readSnapshot(rt, scanner.Include{})
	if err != nil {
		return nil, false, err
	}
	ports := make(map[int]bool, len(snapshot.Ports))
	for _, row := range snapshot.Ports {
		ports[row.Port] = true
	}
	return ports, snapshot.Observation.Narrow(), nil
}

// bindProbe is one address a local service may bind.
type bindProbe struct {
	network string
	host    string
}

// canBindPort is the positive test for "free": bind every address a local
// service may use, then close immediately. A claim that hands out a busy port
// ends as EADDRINUSE in the service; when the table cannot see other users'
// sockets, a failed bind is the only evidence the port is taken.
//
// All four addresses are probed because a specific-address holder and a
// wildcard holder do not conflict on macOS: only the address the service will
// itself bind proves the port usable for it.
func canBindPort(port int) bool {
	probes := []bindProbe{
		{network: "tcp4", host: "0.0.0.0"},
		{network: "tcp4", host: "127.0.0.1"},
	}
	if ipv6Usable() {
		probes = append(probes,
			bindProbe{network: "tcp6", host: "::"},
			bindProbe{network: "tcp6", host: "::1"},
		)
	}
	for _, probe := range probes {
		listener, err := net.Listen(probe.network, net.JoinHostPort(probe.host, strconv.Itoa(port)))
		if err != nil {
			return false
		}
		_ = listener.Close()
	}
	return true
}

// ipv6Usable keeps a host without IPv6 from losing every allocation: only a
// family that exists may answer for a port.
func ipv6Usable() bool {
	listener, err := net.Listen("tcp6", "[::1]:0")
	if err != nil {
		return false
	}
	_ = listener.Close()
	return true
}

// claimsError maps manager failures to contract codes: claim_conflict (1201)
// when another key holds the port, not_found when the range is empty,
// invalid_params when the request names nothing.
func claimsError(what string, err error) error {
	switch {
	case errors.Is(err, store.ErrClaimed):
		return conflictError(err)
	case errors.Is(err, claims.ErrExhausted):
		return exhaustedError(err)
	case errors.Is(err, claims.ErrNoProject):
		return missingProjectError(err)
	}
	return storeError(what, err)
}

func conflictError(err error) error {
	return rpc.NewError(rpc.CodeClaimConflict, err.Error(),
		"another key holds that port; release it or ask for a different count")
}

func exhaustedError(err error) error {
	return rpc.NewError(rpc.CodeNotFound, err.Error(),
		"release stale claims with `option-berth claim --release` or widen the range")
}

func missingProjectError(err error) error {
	return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
		`send {"project": "myapp", "worktree": "main"} or {"key": "myapp/main"}`)
}
