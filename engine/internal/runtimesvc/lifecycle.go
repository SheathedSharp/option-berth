package runtimesvc

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// observationTTL bounds how often the self-probe is re-run. Its answer only
// changes when the process's sandbox does, so a short cache suffices: a status
// read refreshes it while a hello reuses it.
const observationTTL = 30 * time.Second

// observationProbeTimeout stops runtime.service.hello from waiting out a wedged
// collector. A probe that cannot answer within this budget is reported as
// blind, which is the safe direction: an unknown table must not look healthy.
const observationProbeTimeout = 2 * time.Second

// Runtime is the handle the runtime service hands to extension packages: the scanner,
// the logger and the process identity came first, the store followed; the
// process table, the port allocator and the spawn function are added by the
// steps that introduce them.
type Runtime struct {
	Scanner    *scanner.Loop
	Logger     *slog.Logger
	StartedAt  time.Time
	PID        int
	BinaryPath string
	Socket     string
	Version    string

	// Store is the runtime service's SQLite database: renames, group pins, the port
	// history ring and the known `oberth.yaml` roots. Nil when the database
	// could not be opened; every user of it must cope with that.
	Store *store.Store
	// DB is the raw handle an extension package needs. It is the
	// same connection pool Store uses.
	DB  *sql.DB
	srv *Server

	runs   RunRegistry
	runsMu sync.RWMutex

	// Active lifecycle holders and waiters only; idle entries are reclaimed.
	groupLocksMu sync.Mutex
	groupLocks   map[string]*groupLock

	// The self-probe verdict, cached so runtime.service.hello does not scan on every
	// connection. A blind runtime service must say so before a client trusts an empty
	// port list.
	observationMu sync.Mutex
	observation   *rpc.ObservationStatus
	observedAt    time.Time
}

// RefreshObservation returns the runtime service's self-probe verdict, running the probe
// when the cached answer is stale. It is a fact: it binds a real socket and
// looks for it with the same collector the scanner uses.
func (r *Runtime) RefreshObservation(ctx context.Context) *rpc.ObservationStatus {
	r.observationMu.Lock()
	defer r.observationMu.Unlock()
	if r.observation != nil && time.Since(r.observedAt) < observationTTL {
		return r.observation
	}
	probeCtx, cancel := context.WithTimeout(ctx, observationProbeTimeout)
	defer cancel()
	result := ports.ProbeListenerObservation(probeCtx)
	status := &rpc.ObservationStatus{
		State:     string(result.State),
		Code:      result.Code,
		Source:    result.Source,
		Detail:    result.Detail,
		Hint:      result.Hint,
		CheckedAt: time.Now().UTC().Format(time.RFC3339),
	}
	// A cancelled or timed-out probe says nothing about this host, so it is
	// never cached as a verdict. The conservative result goes out, and the next
	// caller is free to ask again.
	if errors.Is(result.Err, context.Canceled) || errors.Is(result.Err, context.DeadlineExceeded) {
		if r.observation != nil && errors.Is(result.Err, context.Canceled) {
			return r.observation
		}
		return status
	}
	r.observation = status
	r.observedAt = time.Now()
	return status
}

// RunRegistry is the set of processes `oberth start` spawned. Carrying it on
// the runtime lets the group resolver attribute a listener to the run that owns
// it (group source `start`); internal/runtimesvc/runsreg installs the real
// implementation from its own OnStart hook, so this package never imports it.
type RunRegistry interface {
	// Stopping marks runs option-berth is about to stop, so their exit is recorded
	// as stopped rather than as a crash.
	Stopping(pids []int)
	// Run reports the run that owns a port, matching groups.Registry so the
	// resolver can take it as-is.
	Run(p state.Port) (run state.Run, ok bool)
	// GroupRuns lists the live runs recorded under a group, with the service
	// each belongs to.
	GroupRuns(group string) []state.Run
	// GroupPIDs lists the live runs recorded under a group.
	GroupPIDs(group string) []int
	// RunStart reports when the process behind a run pid started, so a stop
	// can hand the killer the identity it verifies before signalling. False
	// means no recorded start (unknown, never "reused").
	RunStart(pid int) (time.Time, bool)
	// Prune drops runs whose process is gone.
	Prune()
}

// noRuns is the stand-in used before a registry is installed, so callers never
// have to nil-check.
type noRuns struct{}

func (noRuns) Stopping([]int)                   {}
func (noRuns) Run(state.Port) (state.Run, bool) { return state.Run{}, false }
func (noRuns) GroupRuns(string) []state.Run     { return nil }
func (noRuns) GroupPIDs(string) []int           { return nil }
func (noRuns) RunStart(int) (time.Time, bool)   { return time.Time{}, false }
func (noRuns) Prune()                           {}

// SetRuns installs the run registry. It is called once, from an OnStart hook.
func (r *Runtime) SetRuns(reg RunRegistry) {
	r.runsMu.Lock()
	r.runs = reg
	r.runsMu.Unlock()
}

// Runs is the installed run registry, or a registry that knows nothing yet.
func (r *Runtime) Runs() RunRegistry {
	r.runsMu.RLock()
	reg := r.runs
	r.runsMu.RUnlock()
	if reg == nil {
		return noRuns{}
	}
	return reg
}

// RunRegistry is the installed registry in the shape the resolver expects. The
// scanner reads it once per tick, so a registry installed while the runtime service is
// already up takes effect on the next scan.
func (r *Runtime) RunRegistry() groups.Registry { return r.Runs() }

// DBPath names the database file behind this runtime service, or "" when it has none.
// runtime.service.status reports it.
func (r *Runtime) DBPath() string {
	db := r.Store
	if db == nil {
		return ""
	}
	return db.DBPath()
}

// Server is the running server; handlers use it for subscriber counts and to
// trigger a shutdown.
func (r *Runtime) Server() *Server { return r.srv }

// Subscribers counts the connections subscribed to state right now.
func (r *Runtime) Subscribers() int { return r.srv.Subscribers() }

// Uptime is the time since this runtime service started.
func (r *Runtime) Uptime() time.Duration { return time.Since(r.StartedAt) }

var (
	hooksMu         sync.Mutex
	extraCaps       = map[string]bool{}
	startHooks      []func(*Runtime) error
	shutdownHooks   []func(graceful bool)
	disconnectHooks []func(conn uint64)
)

// RegisterCapability announces a family name in runtime.service.hello's capabilities —
// the list clients use for feature detection. A package holding handlers for a
// namespace announces that namespace here, from the same init().
func RegisterCapability(name string) {
	if name == "" {
		return
	}
	hooksMu.Lock()
	extraCaps[name] = true
	hooksMu.Unlock()
}

// registeredCapabilities lists the announced extras in sorted order.
func registeredCapabilities() []string {
	hooksMu.Lock()
	names := make([]string, 0, len(extraCaps))
	for capability := range extraCaps {
		names = append(names, capability)
	}
	hooksMu.Unlock()
	sort.Strings(names)
	return names
}

// OnStart registers a callback run once the socket is listening and the
// runtime is complete, before the first connection is accepted.
func OnStart(f func(*Runtime)) {
	OnStartChecked(func(rt *Runtime) error {
		f(rt)
		return nil
	})
}

// OnStartChecked registers required startup work. An error prevents accepting
// any RPC and triggers non-graceful cleanup; callers can retry a fresh runtime service
// after the underlying failure is resolved, never a half-recovered registry.
func OnStartChecked(f func(*Runtime) error) {
	hooksMu.Lock()
	startHooks = append(startHooks, f)
	hooksMu.Unlock()
}

// OnDisconnect registers a callback that runs once for each client connection
// going away, receiving that connection's id. An extension package uses it to
// drop per-connection state this package knows nothing about — internal/session
// forgets a device flow whose terminal was closed, so an abandoned code is not
// what a later poll falls back onto.
//
// The callback runs on the closing connection's goroutine: keep it short, and
// never block in it.
func OnDisconnect(f func(conn uint64)) {
	hooksMu.Lock()
	disconnectHooks = append(disconnectHooks, f)
	hooksMu.Unlock()
}

func runDisconnectHooks(conn uint64) {
	for _, f := range disconnectHooksSnapshot() {
		f(conn)
	}
}

func disconnectHooksSnapshot() []func(uint64) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	return append([]func(uint64){}, disconnectHooks...)
}

// OnShutdown registers a callback run as the runtime service stops; graceful is false
// while the runtime service is unwinding after a failure.
func OnShutdown(f func(graceful bool)) {
	hooksMu.Lock()
	shutdownHooks = append(shutdownHooks, f)
	hooksMu.Unlock()
}

func runStartHooks(rt *Runtime) error {
	for _, h := range startHooksSnapshot() {
		if err := h(rt); err != nil {
			return err
		}
	}
	return nil
}

func startHooksSnapshot() []func(*Runtime) error {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	return append([]func(*Runtime) error{}, startHooks...)
}

// runShutdownHooks runs the shutdown callbacks in reverse registration order,
// so a package that set something up in OnStart tears it down after the
// packages that depend on it.
func runShutdownHooks(graceful bool) {
	hooks := shutdownHooksSnapshot()
	for i := len(hooks) - 1; i >= 0; i-- {
		hooks[i](graceful)
	}
}

func shutdownHooksSnapshot() []func(bool) {
	hooksMu.Lock()
	defer hooksMu.Unlock()
	return append([]func(bool){}, shutdownHooks...)
}
