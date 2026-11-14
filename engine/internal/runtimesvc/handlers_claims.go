package runtimesvc

import (
	"errors"
	"net"
	"strconv"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// Every reservations call runs under one lock: reserving reads the table, picks
// a port and writes it back, and two of those interleaving would hand the same
// port to two services. The calls are rare and short, so a runtime service-wide mutex is
// the whole concurrency story. (The unique constraint on claims.port is the
// second line of defence, for a second writer on the same database.)
var claimsMu sync.Mutex

// reservationsBook builds the book for this request, or reports the runtime service that
// came up without a database.
//
// The caller hands in the listener table it already observed, because the probe
// is a full scan and no lock on this path may span IO: a caller holding
// claimsMu must not let the book run one.
//
// narrow says the table cannot see every socket owner. A reservation then
// verifies each candidate by binding it; absence from the table is unknown, not
// free.
func reservationsBook(rt *Runtime, listening map[int]bool, narrow bool) (*claims.Book, error) {
	st := rt.Store
	if st == nil {
		return nil, errNoStore()
	}
	return claims.New(st.Claims(), claims.Options{
		Listening: func() (map[int]bool, error) { return listening, nil },
		Narrow:    narrow,
		Bindable:  canBindPort,
	}), nil
}

// listeningState is the live scan as a set plus whether that table can see
// every socket owner. The set is how the reservation probe skips ports
// something is already serving on; the coverage says whether absence from the
// set is a fact.
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
// service may use and close immediately. A reservation that hands out a busy
// port ends as EADDRINUSE in the service, and when the table cannot see other
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

// claimsError maps the book's failures onto the contract's error codes:
// claim_conflict (1201) for a port another service took between the read and
// the write, not_found when the range is full, invalid_params for an address
// that names nothing.
//
// The full-range hint names a real next step — a person can widen the range
// only through the runtime service's configuration, so it sends them to the two things
// they can actually do.
func claimsError(what string, err error) error {
	switch {
	case errors.Is(err, store.ErrClaimed):
		return rpc.NewError(rpc.CodeClaimConflict, err.Error(),
			"another service reserved that port first; start again to take the next free one")
	case errors.Is(err, claims.ErrExhausted):
		return rpc.NewError(rpc.CodeNotFound, err.Error(),
			"stop services this worktree no longer needs with `oberth down`, or check the runtime service's claims range")
	case errors.Is(err, claims.ErrNoAddress):
		return rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			"a `port: auto` service needs its manifest's project and its own name to reserve a port")
	default:
		return storeError(what, err)
	}
}
