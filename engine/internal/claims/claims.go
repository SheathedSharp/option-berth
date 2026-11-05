package claims

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

const (
	// DefaultTTL is how long a claim lives when the caller says nothing: one
	// day, the `claim_port` tool's default.
	DefaultTTL = 24 * time.Hour
	// MaxCount caps one key's holdings, so a typo in `count` cannot reserve a
	// whole range. It matches the manifest's worktree_ports cap, so a block a
	// configuration asks for is always granted by one Acquire.
	MaxCount = groups.MaxWorktreePorts
	// acquireRounds is how often Acquire retries when the table changed under
	// it. The manager mutex makes a retry unnecessary inside one daemon; a
	// second writer on the same database is what the retry covers.
	acquireRounds = 3
)

var (
	// ErrExhausted means the window holds no free port left.
	ErrExhausted = errors.New("no free port left in the claim range")
	// ErrNoProject means neither a key nor a project was supplied.
	ErrNoProject = errors.New("a claim needs a project or a key")
	// ErrNarrowUnverified means the listener table cannot see every socket
	// owner and no bind probe was supplied. Allocating from that table would
	// be a guess, and the service pays for a guess with EADDRINUSE.
	ErrNarrowUnverified = errors.New("listener table cannot see every owner and no bind probe is configured")
)

// Table is the persistence a Manager needs. The store's Claims view is the
// production implementation.
type Table interface {
	Put(rows ...store.ClaimRow) error
	Get(key string) ([]store.ClaimRow, error)
	Delete(key string) (int, error)
	Expire(now time.Time) (int, error)
	List() ([]store.ClaimRow, error)
}

// Options tunes a Manager. The zero value uses the default range, the wall
// clock and no listener probe.
type Options struct {
	// Range is the window ports are derived from. Zero means DefaultRange.
	Range Range
	// Now is the clock; tests replace it.
	Now func() time.Time
	// Listening reports the ports bound right now — the daemon's snapshot. A
	// claim never hands out a serving port, except one this same key already
	// holds: that listener is very likely the caller's own process, and taking
	// it away would break the idempotency claims exist for.
	Listening func() (map[int]bool, error)
	// Bindable is the positive free-port test used when Narrow is set.
	Bindable func(port int) bool
	// Narrow says Listening came from a table that cannot see every socket
	// owner. A port absent from such a set is unknown, not free, so every
	// candidate must pass Bindable.
	Narrow bool
	// DefaultTTL overrides DefaultTTL for callers that pass no ttl.
	DefaultTTL time.Duration
	// DefaultCount supplies the count for a request that names none, from the
	// project's manifest. Nil or zero means one port.
	DefaultCount func(project string) int
}

// Manager is the claims book. One lives in the daemon, per database.
type Manager struct {
	table          Table
	window         Range
	clock          func() time.Time
	ttl            time.Duration
	listeners      func() (map[int]bool, error)
	narrow         bool
	probeBind      func(port int) bool
	requestedCount func(project string) int

	mu sync.Mutex
}

// New builds a manager over a claims table.
func New(table Table, opts Options) *Manager {
	m := &Manager{
		table:          table,
		window:         opts.Range,
		clock:          opts.Now,
		ttl:            opts.DefaultTTL,
		listeners:      opts.Listening,
		narrow:         opts.Narrow,
		probeBind:      opts.Bindable,
		requestedCount: opts.DefaultCount,
	}
	if m.window.Valid() != nil {
		m.window = DefaultRange
	}
	if m.clock == nil {
		m.clock = time.Now
	}
	if m.ttl <= 0 {
		m.ttl = DefaultTTL
	}
	return m
}

// Range is the window this manager derives from.
func (m *Manager) Range() Range { return m.window }

// Request is one acquire. Key wins when set; otherwise the key is derived from
// Project and Worktree.
type Request struct {
	Key      string
	Count    int
	TTL      time.Duration
	Project  string
	Worktree string
}

// Result is what an acquire hands back.
type Result struct {
	Ports []int

	Key      string
	Project  string
	Worktree string

	CreatedAt time.Time
	ExpiresAt time.Time
}

// target is a Request with every default resolved, ready for one attempt.
type target struct {
	key      string
	project  string
	worktree string
	count    int
	ttl      time.Duration
}

// Acquire reserves Count ports for a key and returns them.
//
// The same key receives the same ports every time: ports it already holds come
// first, in port order, and only the shortfall is probed. Probing starts at
// the key's derived port and walks upward, skipping listening ports and ports
// another key holds. The TTL is refreshed on every call, so a live agent keeps
// its block and an abandoned one lets go.
func (m *Manager) Acquire(req Request) (Result, error) {
	planned, err := m.plan(req)
	if err != nil {
		return Result{}, err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	var conflict error
	for round := 0; round < acquireRounds; round++ {
		result, err := m.reserve(planned)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, store.ErrClaimed) {
			return Result{}, err
		}
		conflict = err
	}
	return Result{}, conflict
}

// plan resolves a request's key, count and TTL before the lock is taken.
func (m *Manager) plan(req Request) (target, error) {
	key, project, worktree, err := resolve(req)
	if err != nil {
		return target{}, err
	}
	count := req.Count
	if count == 0 && m.requestedCount != nil {
		count = m.requestedCount(project)
	}
	if count == 0 {
		count = 1
	}
	if count < 1 || count > MaxCount {
		return target{}, fmt.Errorf("claims: count must be between 1 and %d", MaxCount)
	}
	ttl := req.TTL
	if ttl <= 0 {
		ttl = m.ttl
	}
	return target{key: key, project: project, worktree: worktree, count: count, ttl: ttl}, nil
}

// reserve is one allocation attempt against a freshly expired table.
func (m *Manager) reserve(want target) (Result, error) {
	now := m.clock()
	rows, err := m.currentRows(now)
	if err != nil {
		return Result{}, err
	}
	listening, err := m.listeningNow()
	if err != nil {
		return Result{}, err
	}
	if m.narrow && m.probeBind == nil {
		return Result{}, ErrNarrowUnverified
	}

	owners := make(map[int]string, len(rows))
	own := make([]store.ClaimRow, 0, want.count)
	for _, row := range rows {
		owners[row.Port] = row.Key
		if row.Key == want.key {
			own = append(own, row)
		}
	}
	sort.Slice(own, func(i, j int) bool { return own[i].Port < own[j].Port })

	created := make(map[int]time.Time, len(own))
	chosen := make([]int, 0, want.count)
	taken := make(map[int]bool, want.count)
	for _, row := range own {
		if len(chosen) == want.count {
			break
		}
		chosen = append(chosen, row.Port)
		taken[row.Port] = true
		created[row.Port] = row.CreatedAt
	}

	for offset := 0; len(chosen) < want.count && offset < m.window.Span(); offset++ {
		port := m.window.At(want.key, offset)
		if taken[port] || listening[port] {
			continue
		}
		if holder, held := owners[port]; held && holder != want.key {
			continue
		}
		// A narrow table cannot say the port is free; a real bind can.
		if m.narrow && !m.probeBind(port) {
			continue
		}
		chosen = append(chosen, port)
		taken[port] = true
	}
	if len(chosen) < want.count {
		return Result{}, fmt.Errorf("%w (%d-%d): wanted %d, found %d",
			ErrExhausted, m.window.Start, m.window.End, want.count, len(chosen))
	}
	sort.Ints(chosen)

	// A shrinking count leaves ports behind; dropping the key first keeps the
	// table exactly what the caller was just told it holds.
	if len(own) > len(chosen) {
		if _, err := m.table.Delete(want.key); err != nil {
			return Result{}, err
		}
	}

	expires := now.Add(want.ttl)
	write := claimRows(want, chosen, created, now, expires)
	if err := m.table.Put(write...); err != nil {
		return Result{}, err
	}

	return Result{
		Key: want.key, Project: want.project, Worktree: want.worktree,
		Ports: chosen, CreatedAt: earliest(created, chosen, now), ExpiresAt: expires,
	}, nil
}

// currentRows sweeps expired claims and returns the live ones.
func (m *Manager) currentRows(now time.Time) ([]store.ClaimRow, error) {
	if _, err := m.table.Expire(now); err != nil {
		return nil, err
	}
	return m.table.List()
}

// claimRows builds the rows to persist for one allocation. A port the key
// already held keeps its original creation time; a newly chosen port starts
// now.
func claimRows(want target, chosen []int, created map[int]time.Time, now, expires time.Time) []store.ClaimRow {
	rows := make([]store.ClaimRow, 0, len(chosen))
	for _, port := range chosen {
		createdAt, reused := created[port]
		if !reused {
			createdAt = now
		}
		rows = append(rows, store.ClaimRow{
			Port: port, Key: want.key, Project: want.project, Worktree: want.worktree,
			CreatedAt: createdAt, ExpiresAt: expires,
		})
	}
	return rows
}

// earliest is when this allocation's oldest port was first reserved.
func earliest(created map[int]time.Time, chosen []int, fallback time.Time) time.Time {
	first := fallback
	for _, port := range chosen {
		if at, reused := created[port]; reused && at.Before(first) {
			first = at
		}
	}
	return first
}

// Release gives up every port held under key. A key nobody holds releases
// zero ports and still succeeds.
func (m *Manager) Release(key string) (released int, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.clock()
	if _, err := m.table.Expire(now); err != nil {
		return 0, err
	}
	return m.table.Delete(key)
}

// Held maps every claimed port to its key after sweeping expired rows.
// `ports.next` uses it to skip foreign claims.
func (m *Manager) Held() (map[int]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows, err := m.currentRows(m.clock())
	if err != nil {
		return nil, err
	}
	out := make(map[int]string, len(rows))
	for _, row := range rows {
		out[row.Port] = row.Key
	}
	return out, nil
}

// listeningNow normalises the optional listener probe to a non-nil set.
func (m *Manager) listeningNow() (map[int]bool, error) {
	empty := map[int]bool{}
	if m.listeners == nil {
		return empty, nil
	}
	live, err := m.listeners()
	switch {
	case err != nil:
		return nil, err
	case live == nil:
		return empty, nil
	}
	return live, nil
}

// resolve turns a request into a key and the project/worktree stored with it.
// An explicit key wins; project and worktree are then recovered from it unless
// the caller supplied them too.
func resolve(req Request) (key, project, worktree string, err error) {
	key, project, worktree = req.Key, req.Project, req.Worktree
	if key == "" {
		if project == "" {
			return "", "", "", ErrNoProject
		}
		key = Key(project, worktree)
	}
	switch {
	case project == "":
		project, worktree = SplitKey(key)
	case worktree == "":
		worktree = DefaultWorktree
	}
	return key, project, worktree, nil
}
