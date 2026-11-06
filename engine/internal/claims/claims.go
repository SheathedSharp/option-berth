// Package claims books the port a `port: auto` service runs on.
//
// A reservation is book-keeping, not an OS bind: option-berth records that an
// address belongs to one service so that parallel checkouts stop picking the
// same port, and so a service that is started again gets the port it had
// before. A process outside option-berth can still take a reserved port, which
// is why every reservation is checked against the live listener table before it
// is handed out.
package claims

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// DefaultLease is how long a reservation lives without being renewed: one day.
const DefaultLease = 24 * time.Hour

// ErrExhausted is returned when the range holds no free port left.
var ErrExhausted = errors.New("no free port left in the claim range")

// ErrNoAddress is returned for a reservation that names no checkout or no
// service. Such a row could not be found again by `down --worktree`, so it is
// refused rather than stored.
var ErrNoAddress = errors.New("a reservation needs a project and a service")

// ErrNarrowUnverified is returned when the listener table cannot see every
// socket owner and no bind probe was supplied. Allocating from that table would
// be a guess, and the service pays for a guess with EADDRINUSE.
var ErrNarrowUnverified = errors.New("listener table cannot see every owner and no bind probe is configured")

// Address names one service in one checkout: the subject a reservation is
// about. Two checkouts of one repository are two addresses, and two services in
// one checkout are two addresses.
//
// Worktree is the checkout's name within its repository — DefaultWorktree for
// the main checkout, the directory name for a linked worktree. It is not the
// `${worktree}` token; this one names the reservation's owner.
type Address struct {
	Project  string
	Worktree string
	Service  string
}

// Key is the row key this address is stored under: `<project>/<worktree>/<service>`.
func (a Address) Key() string {
	trimmed := a.trimmed()
	return trimmed.Project + "/" + trimmed.Worktree + "/" + trimmed.Service
}

// trimmed is the address with its parts cleaned and its worktree defaulted, so
// an empty worktree means the main checkout rather than an empty path segment.
func (a Address) trimmed() Address {
	return Address{
		Project:  strings.Trim(strings.TrimSpace(a.Project), "/"),
		Worktree: worktreeName(a.Worktree),
		Service:  strings.TrimSpace(a.Service),
	}
}

// validate refuses an address a reservation could not be found back from.
func (a Address) validate() error {
	trimmed := a.trimmed()
	if trimmed.Project == "" || trimmed.Service == "" {
		return ErrNoAddress
	}
	return nil
}

// Reservation is one booked address and the window it is booked for.
type Reservation struct {
	Address
	// Port is the port this service was given.
	Port int
	// Since is when the service first took the port; it does not move on a
	// renewal, so a reader can tell a long-held address from a fresh one.
	Since time.Time
	// Until is when the reservation lapses unless the service is started again.
	Until time.Time
}

// Table is the persistence a Book needs; *store.Store's Claims view is the
// production implementation.
type Table interface {
	Put(rows ...store.ClaimRow) error
	List() ([]store.ClaimRow, error)
	Expire(now time.Time) (int, error)
}

// Options tunes a Book. The zero value uses the default range, the wall clock,
// no listener probe and the default lease.
type Options struct {
	// Range is the window ports are derived from. The zero value is DefaultRange.
	Range Range
	// Clock is the clock, replaced in tests.
	Clock func() time.Time
	// Listening reports the ports something is bound to right now — the
	// daemon's scan snapshot. A reservation never hands out a port that is
	// already serving, except one this same service already holds: that
	// listener is very likely its own process, and taking the port away from it
	// would break the idempotency the whole feature exists for.
	Listening func() (map[int]bool, error)
	// Narrow says Listening came from a table that cannot see every socket
	// owner (the Darwin lsof fallback cannot see other users' sockets). A port
	// absent from a narrow set is unknown, so each candidate is verified with
	// Bindable instead of being taken as free.
	Narrow bool
	// Bindable reports whether this process can bind a port right now. It is
	// the positive test used when Narrow is set; without it a narrow book
	// refuses to allocate rather than risk handing out a busy port.
	Bindable func(port int) bool
	// Lease overrides DefaultLease.
	Lease time.Duration
}

// Book issues the reservations. One lives in the daemon, per database.
//
// A Book holds one row per service rather than a block per checkout: the
// product's unit is one `port: auto` service, and a service needs exactly one
// port. There is no count to ask for and no block to fill.
type Book struct {
	table    Table
	window   Range
	clock    func() time.Time
	lease    time.Duration
	live     func() (map[int]bool, error)
	narrow   bool
	bindable func(port int) bool

	// mu serialises reserve-and-write. Two of those interleaving would hand
	// the same port to two services; the unique constraint on claims.port is
	// the second line of defence, for a writer outside this Book.
	mu sync.Mutex
}

// New builds a book over a reservations table.
func New(t Table, o Options) *Book {
	b := &Book{
		table: t, window: o.Range, clock: o.Clock, lease: o.Lease,
		live: o.Listening, narrow: o.Narrow, bindable: o.Bindable,
	}
	b.window = b.window.bounds()
	if b.clock == nil {
		b.clock = time.Now
	}
	if b.lease <= 0 {
		b.lease = DefaultLease
	}
	return b
}

// Range reports the window this book derives from.
func (b *Book) Range() Range { return b.window }

// Reserve returns the address this service runs on, booking it if it holds
// none.
//
// The same service gets the same port every time: a port it already holds is
// returned as it stands, and only a service that holds none probes for one.
// Probing starts at the port derived from the address and walks upward,
// skipping ports that are listening and ports another service holds. The lease
// is renewed on every call, so a service that is started regularly keeps its
// address and one nobody starts any more lets go.
func (b *Book) Reserve(a Address) (Reservation, error) {
	if err := a.validate(); err != nil {
		return Reservation{}, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.reserve(a.trimmed())
}

// reserve is Reserve's body, called with the address cleaned and the lock held.
func (b *Book) reserve(a Address) (Reservation, error) {
	now := b.clock()
	// A lease that ran out is not a reservation; sweep before reading so the
	// table says what this acquire is about to be told.
	if _, err := b.table.Expire(now); err != nil {
		return Reservation{}, err
	}
	rows, err := b.table.List()
	if err != nil {
		return Reservation{}, err
	}
	serving, err := b.holders()
	if err != nil {
		return Reservation{}, err
	}

	key := a.Key()
	held := make(map[int]string, len(rows))
	var mine []store.ClaimRow
	for _, row := range rows {
		held[row.Port] = row.Key
		if row.Key == key {
			mine = append(mine, row)
		}
	}

	port, since, ok := b.choose(key, mine, held, serving)
	if !ok {
		return Reservation{}, fmt.Errorf("%w (%d-%d): every port is serving or held by another service",
			ErrExhausted, b.window.First(), b.window.Last())
	}
	if since.IsZero() {
		// A port this service did not hold before starts its clock now, and
		// the stored row keeps that moment on every later renewal.
		since = now
	}

	until := now.Add(b.lease)
	if err := b.table.Put(store.ClaimRow{
		Port: port, Key: key, Project: a.Project, Worktree: a.Worktree,
		Since: since, Until: until,
	}); err != nil {
		return Reservation{}, err
	}
	return Reservation{Address: a, Port: port, Since: since, Until: until}, nil
}

// choose picks the port this service runs on. A port the service already holds
// wins over everything else — including a listener on it, which is very likely
// the service itself — and only a service that holds none probes the window.
func (b *Book) choose(key string, mine []store.ClaimRow, held map[int]string, serving map[int]bool) (port int, since time.Time, ok bool) {
	if len(mine) > 0 {
		sort.Slice(mine, func(i, j int) bool { return mine[i].Port < mine[j].Port })
		return mine[0].Port, mine[0].Since, true
	}
	base := b.window.Base(key)
	for i := 0; i < b.window.Span(); i++ {
		candidate := b.window.Walk(base, i)
		if serving[candidate] {
			continue
		}
		if holder, taken := held[candidate]; taken && holder != key {
			continue
		}
		// A narrow table cannot say that this port is free; a real bind can.
		if b.narrow && !b.bindable(candidate) {
			continue
		}
		return candidate, time.Time{}, true
	}
	return 0, time.Time{}, false
}

// holders is the live listener table, and the answer to whether a claim may be
// derived from it at all.
func (b *Book) holders() (map[int]bool, error) {
	if b.narrow && b.bindable == nil {
		return nil, ErrNarrowUnverified
	}
	if b.live == nil {
		return nil, nil
	}
	return b.live()
}

func worktreeName(worktree string) string {
	trimmed := strings.Trim(strings.TrimSpace(worktree), "/")
	if trimmed == "" {
		return DefaultWorktree
	}
	return trimmed
}
