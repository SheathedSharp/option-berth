// Package scanner owns the daemon's single scan goroutine. It wraps the
// existing ports/docker enrichment pipeline, keeps the last good Snapshot, and
// publishes (previous, next) pairs so the daemon can compute whichever delta
// flavours its subscribers asked for.
package scanner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/internal/docker"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// Timing constants from the spec's "Scanner loop" section.
const (
	// BaseInterval is the interval while anything is changing, and the
	// default `daemon.scan_interval` sets.
	BaseInterval = 2 * time.Second
	// MinScanInterval is the floor `daemon.scan_interval` may be set to. One
	// scan is an `lsof`/`ss`/`netstat` plus a docker round trip; below a
	// second a machine spends more of itself being measured than run.
	MinScanInterval = 1 * time.Second
	// MaxInterval caps the backoff on unchanged scans while the daemon is
	// serving RPC reads only. The constant is the ceiling at the default
	// base; a configured base scales it by IdleMaxFactor.
	MaxInterval = 10 * time.Second
	// SubscribedMaxInterval caps the backoff while at least one subscriber is
	// connected, so a live view never lags a change by more than this. Like
	// MaxInterval it is the ceiling at the default base, scaled by
	// SubscribedMaxFactor.
	SubscribedMaxInterval = 5 * time.Second
	// SubscribedMaxFactor and IdleMaxFactor scale the two ceilings with the
	// base interval, so `daemon.scan_interval` moves the whole adaptive curve
	// rather than only its floor. At the 2 s default they reproduce
	// SubscribedMaxInterval and MaxInterval exactly.
	SubscribedMaxFactor = 2.5
	IdleMaxFactor       = 5
	// BackoffFactor multiplies the interval after an unchanged scan.
	BackoffFactor = 1.5
	// CacheTTL is how long an RPC read may reuse the last scan.
	CacheTTL = 2 * time.Second
	// HealthCadence keeps HTTP probes off the port-scan cadence.
	HealthCadence = 10 * time.Second
	// HealthTimeout bounds a single probe.
	HealthTimeout = 2 * time.Second
	// HealthBudget bounds a whole round of opt-in health probes, however many
	// ports are listening. Ten probes run at once, so without a ceiling a
	// machine with forty listeners costs four waves of HealthTimeout — and a
	// write handler's republish is queued behind the scan that pays it
	// (contract §44).
	HealthBudget = 4 * time.Second
	// ConfiguredHealthBudget is the same ceiling for the `oberth.yaml` health
	// paths, which are probed on every tick rather than on HealthCadence.
	ConfiguredHealthBudget = 2 * time.Second
	// ScanLockBudget bounds RPC gate admission and the cooperative follow-up
	// owned by Republish. It is not a hard limit on synchronous filesystem or
	// store calls already in progress. Forced observations never turn a gate
	// failure into last-good success.
	ScanLockBudget = 15 * time.Second
	// StatsInterval is the fixed cadence of the stats-only tick: per-process
	// cpu and memory plus this machine's load row, sampled without a port
	// scan behind it. Unlike the scan interval it never backs off, and it
	// only runs while someone is subscribed.
	StatsInterval = 1 * time.Second
	// MinStatsInterval is the floor `daemon.stats_interval` may be set to.
	// Below it the sampler costs more than the numbers it publishes are
	// worth: every tick is a `ps` per machine on Unix and a PowerShell on
	// Windows.
	MinStatsInterval = 250 * time.Millisecond
)

// Include is the per-subscriber opt-in from `state.subscribe {include}`.
type Include struct {
	Stats  bool
	Health bool
}

// Demand reports what the daemon's subscribers currently want. Returning zero
// subscribers stops the loop until Wake is called.
type Demand func() (subscribers int, include Include)

// Publisher receives every published change. prev is the snapshot the delta is
// computed against; next is the new state. Events are already derived.
type Publisher func(prev, next state.Snapshot, events []state.Event)

// Options configures a Loop. Only Scan is defaulted; the rest may be zero.
type Options struct {
	DaemonVersion string
	Logger        *slog.Logger
	Demand        Demand
	Publish       Publisher

	// Store persists renames, group pins, known `oberth.yaml` roots and the
	// port history ring. Nil means the loop scans without a database.
	Store Store

	// Runs returns the run registry group attribution consults. It is a
	// function because `oberth start` installs the registry after the loop is
	// built; nil, or a nil return, means groups.PortRuns{}.
	Runs func() groups.Registry

	// Sessions builds the snapshot's `sessions` collection from the ports the
	// tick just resolved. It is a function for the same reason Runs is: the
	// daemon installs it from an OnStart hook, after the loop exists. Nil
	// publishes an empty collection.
	Sessions func(ports []state.Port) []state.SessionRecord

	// Scan overrides the OS scan. Tests inject a fake; production leaves it nil
	// and gets ports.Scan + ports.Enrich, with container data from a
	// docker.Watcher's cached list rather than an inline `docker ps`
	// (step 5A.8).
	Scan func(include Include) ([]ports.ListeningPort, error)

	// ScanContext takes precedence over Scan. Production uses this seam;
	// legacy Scan callbacks remain synchronous and must return on their own.
	ScanContext func(ctx context.Context, include Include) ([]ports.ListeningPort, error)

	// Probe overrides the health probe. Tests inject a fake; production leaves
	// it nil and gets ports.ProbeHealth. Configured health goes through this
	// seam, so tests need not open a real socket.
	Probe Probe

	// ProbeContext takes precedence over Probe and propagates scan ownership
	// to configured HTTP health checks. Nil uses the legacy override, if any,
	// otherwise the context-aware production HTTP adapter.
	ProbeContext ProbeContext

	// SampleStats reads cpu, memory, state and uptime for pids the last
	// snapshot already named — the stats-only tick's one OS call. Tests
	// inject a synchronous fake; production leaves it nil and gets the
	// context-aware ports.SampleProcStatsContext. Overrides must return; they
	// are not detached on cancellation.
	SampleStats func(pids []int) map[int]ports.ProcSample

	// ScanInterval overrides the base port-scan cadence
	// (`daemon.scan_interval`): the interval used while something is changing
	// and the floor the backoff returns to. Zero means BaseInterval; anything
	// below MinScanInterval is clamped to it. Both backoff ceilings scale
	// with it, so the adaptive shape is the same whatever the base.
	ScanInterval time.Duration

	// StatsInterval overrides the stats-only tick's cadence
	// (`daemon.stats_interval`): Zero means StatsInterval; anything below
	// MinStatsInterval is clamped to it.
	StatsInterval time.Duration

	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Loop is the scan goroutine plus the cached snapshot RPC reads serve from.
type Loop struct {
	opts Options
	now  func() time.Time
	// base is the resolved `daemon.scan_interval`: the cadence while
	// something is changing and the floor the backoff returns to. It is fixed
	// at construction, so nothing locks to read it.
	base time.Duration

	wake chan struct{}
	// statsWake unparks the stats-only tick when a subscriber connects. It is
	// separate from wake so the two ticks can park and resume independently.
	statsWake chan struct{}

	attr attribution

	// docker keeps the container list the production scan enriches from,
	// refreshed in the background so no scan — and so no handler queued
	// behind one — ever waits on the docker CLI (step 5A.8). Nil when a test
	// injects its own Scan.
	docker *docker.Watcher

	// runGate admits one OS scan at a time. It is held from a scan's first
	// system call to its last, so two scans can never overlap and a slow one
	// can never commit port rows on top of a newer one's. Only scanning takes
	// it: a republish needs no OS call at all.
	//
	// It is a buffered channel rather than a sync.Mutex because a handler that
	// waits for it has to wait *with a deadline* (contract §44).
	runGate chan struct{}

	// rpcGate does the same for the scans an RPC starts: one at a time, so ten
	// clients reading at once still cost the machine one scan. It is separate
	// from runGate on purpose. A `ports.kill` needs a bare port list and gets
	// it in a moment; the loop's own tick is collecting stats, which means
	// `docker stats` and a machine-wide `lsof`, and queueing the first behind
	// the second is what made a dry-run kill take double figures of seconds
	// (contract §44). The two can overlap because ordering no longer depends
	// on them not overlapping: attribution and the commit are serialized by
	// orderGate, and a scan whose OS half is older than the published snapshot
	// never commits (see commitScan).
	rpcGate chan struct{}

	// orderGate is what 1A.15's scanMu really guards: the attribution that
	// reads the store, the commit into the cache and the publish, taken by
	// everything that publishes a re-attributed snapshot — a scan, a store
	// write's republish.
	//
	// The rule it buys is unchanged (contract §38): a scan's attribution
	// happens either wholly before a store write or wholly after it, so the
	// tick that loaded the rename table before `ports.rename` wrote to it can
	// no longer commit on top of the write and put the old display_name back.
	// What changed in 1A.19 is where it starts. It used to be taken before the
	// OS scan, which meant a rename, a group pin or a "Save color" waited out
	// `lsof`, `ps`, `docker stats` and a round of health probes before its own
	// microsecond of work — seconds of latency for a guarantee that only ever
	// needed the store-reading half. Ordering is about attribution, not about
	// asking the kernel which sockets are open (contract §44).
	//
	// Lock order is runGate, then orderGate, then commitMu; never the reverse.
	orderGate chan struct{}

	// commitMu orders the in-memory commit and its publication. External stats
	// sampling does not hold it: prepared samples are validated against the
	// latest scan generation and applied to the current immutable snapshot.
	// Lock order: orderGate -> commitMu -> mu, or statsMu -> commitMu -> mu.
	// Neither scans nor store writes acquire statsMu.
	commitMu contextMutex
	// statsMu serializes only samplers, keeping their observation order without
	// queuing RPCs or full scans behind an external process.
	statsMu contextMutex

	mu   sync.Mutex
	subs int
	snap state.Snapshot
	// lastPorts is the OS half of the last scan, before attribution. Republish
	// re-attributes it instead of scanning the machine again, which is what
	// makes a rename or a `oberth.yaml` write cost microseconds rather than a
	// full scan (contract §44).
	lastPorts []ports.ListeningPort
	haveSnap  bool

	// scanSeq numbers scans in the order their OS half begins, and
	// committedSeq is the number of the scan the published snapshot came
	// from. Both are counters rather than timestamps on purpose: "did a scan
	// begin after I asked" and "is this scan older than the one already
	// published" are happens-before questions, and a clock cannot answer
	// them. `time.Now` on Windows moves in steps of up to about 15 ms, so two
	// events milliseconds apart read as simultaneous — which made a kill's
	// rescan coalesce onto the scan that had *just* finished and resolve its
	// targets against the very snapshot §17 says it must not use.
	scanSeq      uint64
	committedSeq uint64
	// statsScanSeq is the most recent accepted OS scan that collected stats.
	// A sample prepared before that scan must not overwrite its newer data.
	// Bare scans may safely rebase samples after per-row identity validation.
	statsScanSeq uint64

	// Mutation notifications coalesce by revision, independently of the
	// completion clock. An observation begun before a change cannot discharge
	// it; a failed attempt paces retries without making cached facts fresh.
	refreshRequested uint64
	refreshAttempted uint64
	refreshObserved  uint64

	// Attempts pace retries, successful observations determine cache freshness.
	// A failed collector may retain the last snapshot, never renew its TTL.
	lastAttemptAt time.Time
	outcomeSeq    uint64
	lastScanAt    time.Time
	lastHealthAt  time.Time
	interval      time.Duration
	seq           uint64
	scans         int64
	lastErr       error
}

// New builds a Loop. It does not start scanning; call Run.
func New(opts Options) *Loop {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Demand == nil {
		opts.Demand = func() (int, Include) { return 0, Include{} }
	}
	if opts.Publish == nil {
		opts.Publish = func(state.Snapshot, state.Snapshot, []state.Event) {}
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	base := resolveScanInterval(opts.ScanInterval)
	l := &Loop{
		opts:      opts,
		now:       now,
		base:      base,
		wake:      make(chan struct{}, 1),
		statsWake: make(chan struct{}, 1),
		runGate:   make(chan struct{}, 1),
		rpcGate:   make(chan struct{}, 1),
		orderGate: make(chan struct{}, 1),
		interval:  base,
	}
	if l.opts.Scan == nil && l.opts.ScanContext == nil {
		// The refresh cadence is the scan cadence: scans poke the watcher,
		// and it asks Docker at most once per base interval. A refresh that
		// changes the list wakes the loop, so a new container's port is
		// enriched within one tick rather than one backed-off interval.
		l.docker = docker.NewWatcher(docker.WatcherOptions{
			Interval: base,
			Logger:   opts.Logger,
			OnChange: l.Wake,
		})
		l.opts.ScanContext = l.osScanContext
	}
	return l
}

// resolveScanInterval clamps a configured base scan interval. Zero (the unset
// case) means BaseInterval; there is no "off", because the loop already parks
// itself whenever nothing is subscribed.
func resolveScanInterval(d time.Duration) time.Duration {
	if d <= 0 {
		return BaseInterval
	}
	if d < MinScanInterval {
		return MinScanInterval
	}
	return d
}

// osScan is the production scan: the same pipeline `oberth list` runs, so the
// daemon and the no-daemon path emit the same rows — except that container
// data and container stats come from the watcher's cache, at most one refresh
// old, instead of from a `docker ps` this scan would wait on. A wedged Docker
// used to cost every scan the whole docker.CLITimeout, and every `ports.kill`
// and `state.snapshot` queued behind it (step 5A.8).
func (l *Loop) osScan(include Include) ([]ports.ListeningPort, error) {
	ctx, cancel := observationContext(context.Background())
	defer cancel()
	return l.osScanContext(ctx, include)
}

func (l *Loop) osScanContext(ctx context.Context, include Include) ([]ports.ListeningPort, error) {
	pp, err := ports.ScanContext(ctx)
	if err != nil {
		return nil, err
	}
	l.docker.Enrich(pp)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// The owner deadline reaches metadata, cwd and full-stat commands.
	// Filesystem/PEB syscalls remain synchronous, checked between operations.
	if err := ports.EnrichContext(ctx, pp); err != nil {
		return nil, err
	}
	if include.Stats {
		if err := ports.EnrichStatsContext(ctx, pp, l.docker.Stats()); err != nil {
			return nil, err
		}
	}
	return pp, ctx.Err()
}

// Probe runs one health probe through the loop's seam.
func (l *Loop) Probe(host string, port int, path string, timeout time.Duration) ports.HealthResult {
	return l.ProbeContext(context.Background(), host, port, path, timeout)
}

// ProbeContext uses one synchronous probe; cancellation is never a verdict.
func (l *Loop) ProbeContext(ctx context.Context, host string, port int, path string, timeout time.Duration) ports.HealthResult {
	if ctx.Err() != nil {
		return ports.HealthResult{}
	}
	var result ports.HealthResult
	switch {
	case l.opts.ProbeContext != nil:
		result = l.opts.ProbeContext(ctx, host, port, path, timeout)
	case l.opts.Probe != nil:
		result = l.opts.Probe(host, port, path, timeout)
	default:
		result = ports.ProbeHealthContext(ctx, host, port, path, timeout)
	}
	if ctx.Err() != nil {
		return ports.HealthResult{}
	}
	return result
}

// SetDemand installs the demand callback after construction. The daemon uses
// it because the server and the loop refer to each other.
func (l *Loop) SetDemand(d Demand) {
	if d != nil {
		l.opts.Demand = d
	}
}

// SetPublisher installs the publish callback after construction.
func (l *Loop) SetPublisher(p Publisher) {
	if p != nil {
		l.opts.Publish = p
	}
}

// Cached returns the last published snapshot without scanning.
func (l *Loop) Cached() state.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap {
		return state.Rows{}.Normalize().Into(state.Snapshot{At: l.now().Format(time.RFC3339), DaemonVersion: l.opts.DaemonVersion})
	}
	return l.snap
}

// Wake nudges a stopped or backed-off loop to scan now. Called when a
// subscriber connects or when an RPC reads state.
func (l *Loop) Wake() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
	select {
	case l.statsWake <- struct{}{}:
	default:
	}
}

// Run drives the loop until ctx is cancelled. With zero subscribers it parks on
// Wake and does no work at all; the next RPC or subscription starts it again.
//
// It also runs the stats-only tick (see runStats) in a second goroutine, and
// returns only once both have stopped. Commits and publication are serialized
// by commitMu, while their OS work may overlap: the whole point is that
// a 1 s load sample does not have to wait for — or reset — an adaptive port
// scan that may be 5 s apart.
func (l *Loop) Run(ctx context.Context) {
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		l.runStats(ctx)
	}()
	if l.docker != nil {
		// The docker watcher refreshes the container list the scans read,
		// in its own goroutine for the same reason: nothing on the scan
		// path waits on the docker CLI.
		background.Add(1)
		go func() {
			defer background.Done()
			l.docker.Run(ctx)
		}()
	}
	defer background.Wait()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		subs, include := l.opts.Demand()
		if subs == 0 {
			// Parked: no scanning, no timer.
			select {
			case <-ctx.Done():
				return
			case <-l.wake:
				continue
			}
		}

		// A scan an RPC ran a moment ago is this tick's scan. Wake means "the
		// state you are holding may be stale", and a scan younger than the
		// current interval is not: without this floor every `ports.kill`,
		// every `oberth list` that missed the cache and every write woke the
		// loop into a second full scan, which is one more scan for the next
		// caller to be queued behind (contract §44).
		//
		// The floor is the *loop's* cadence and nothing else. A handler that
		// asks for a scan gets one: Rescan is a forced scan (see scanMode)
		// and never consults this, because a kill resolving its selectors
		// against the previous tick is the bug §17 exists to prevent.
		wait := l.dueIn()
		if wait <= 0 {
			l.scanAndPublishContext(ctx, include)
			wait = l.dueIn()
		}
		if wait <= 0 {
			wait = l.base
		}

		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-l.wake:
			// A read or a new subscriber: back to the base interval.
			l.mu.Lock()
			l.interval = l.base
			l.mu.Unlock()
		}
	}
}

// scanAndPublish runs one scan, updates the cache, adapts the interval and
// publishes when something changed. One scan at a time (see scanMu); the
// publish itself happens inside scanLocked, under commitMu.
func (l *Loop) scanAndPublish(include Include) {
	l.scanAndPublishContext(context.Background(), include)
}

func (l *Loop) scanAndPublishContext(ctx context.Context, include Include) {
	ctx, cancel := observationContext(ctx)
	defer cancel()
	if lockContext(ctx, l.runGate) != nil {
		return
	}
	defer unlock(l.runGate)
	if ctx.Err() != nil {
		return
	}

	_, _, _, _ = l.scanLockedContext(ctx, include, Include{}, true)
}

// publish hands the transition to the daemon and then writes its port
// transitions to the history ring, in that order: a client sees the change
// before the disk does (daemon spec, "SQLite").
func (l *Loop) publish(prev, next state.Snapshot, events []state.Event) {
	l.opts.Publish(prev, next, events)
	l.record(events)
}

// scanLocked performs a scan and swaps it into the cache. It returns the new
// snapshot, the one it replaced, and whether anything a client cares about
// changed.
//
// collect is what this scan asks the OS for. carry is what the *subscribers*
// want the published snapshot to keep but this caller has no reason to pay
// for: those fields are copied forward from the previous snapshot instead of
// being collected again (contract §44). The scan loop's own tick passes its
// demand as collect and nothing as carry; an RPC passes the caller's own
// include as collect and the subscribers' as carry.
func (l *Loop) scanLocked(collect, carry Include, reportFailure bool) (next, prev state.Snapshot, changed bool, err error) {
	return l.scanLockedContext(context.Background(), collect, carry, reportFailure)
}

func (l *Loop) scanLockedContext(ctx context.Context, collect, carry Include, reportFailure bool) (next, prev state.Snapshot, changed bool, err error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}
	// Read outside the mutex: Demand takes the server's own lock, which the
	// server holds while calling back into the loop.
	subs, _ := l.opts.Demand()
	wantHealth := collect.Health && l.healthDue()

	gen, refresh := l.beginScan()
	var pp []ports.ListeningPort
	if l.opts.ScanContext != nil {
		pp, err = l.opts.ScanContext(ctx, collect)
	} else {
		pp, err = l.opts.Scan(collect)
	}
	if err := ctx.Err(); err != nil {
		// Ownership ended, not a collector failure. Preserve the last outcome
		// and freshness, and leave pending invalidation for a later observer.
		return state.Snapshot{}, l.Cached(), false, err
	}
	if err != nil {
		if reportFailure {
			l.opts.Logger.Warn("scan failed, keeping last good state", "error", err)
		}
		// Failure notification is part of publication ordering too. A recovery
		// must not commit between checking the outcome and reporting its error.
		if lockErr := l.commitMu.LockContext(ctx); lockErr != nil {
			return state.Snapshot{}, l.Cached(), false, lockErr
		}
		defer l.commitMu.Unlock()
		if err := ctx.Err(); err != nil {
			return state.Snapshot{}, l.Cached(), false, err
		}
		l.mu.Lock()
		if gen >= l.outcomeSeq {
			l.outcomeSeq = gen
			l.lastErr = err
			l.lastAttemptAt = l.now()
			l.refreshAttempted = max(l.refreshAttempted, refresh)
		}
		l.scans++
		prev = l.snap
		current := gen == l.outcomeSeq
		superseded := !current && l.haveSnap && l.lastErr == nil
		l.mu.Unlock()
		if superseded {
			return prev, prev, false, nil
		}
		if reportFailure && current {
			l.opts.Publish(prev, prev, []state.Event{{
				Kind: "scan_error", At: l.nowRFC3339(),
				Data: map[string]any{"error": err.Error()},
			}})
		}
		return state.Snapshot{}, prev, false, err
	}

	if wantHealth {
		ports.EnrichHealthContext(ctx, pp, HealthTimeout, HealthBudget)
	}

	// Everything from here on is the ordered half of the scan: the attribution
	// that reads the store, the commit, and the publish. It is serialized
	// against every store write's republish and against every other publisher,
	// and the publish rides inside it so a later seq can never reach a client
	// first (contract §38, §44).
	if err := lockContext(ctx, l.orderGate); err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}
	defer unlock(l.orderGate)

	// Resolve every port's group with the pins the store holds, apply the
	// stored renames and build the group collection, all before the snapshot
	// is assembled: what gets published is already attributed and named.
	rows, groupRows, err := l.attributeContext(ctx, pp)
	if err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}
	sessionRows, err := l.sessionsContext(ctx, rows)
	if err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}

	// Configured health comes after attribution because it is the groups that
	// say which port has a `health:` path. It runs on every tick regardless of
	// `include`: a health path in a `oberth.yaml` is part of what the service
	// is, not an opt-in statistic (step 1A.7). Its budget is what keeps this
	// short enough to sit inside the ordering gate.
	if err := ProbeConfiguredContext(ctx, rows, groupRows, l.ProbeContext, ConfiguredHealthBudget); err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}

	if err := l.commitMu.LockContext(ctx); err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}
	defer l.commitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, l.Cached(), false, err
	}

	next, prev, changed = l.commitScan(commit{
		subs:     subs,
		collect:  collect,
		carry:    carry,
		health:   wantHealth,
		seq:      gen,
		refresh:  refresh,
		pp:       pp,
		rows:     rows,
		groups:   groupRows,
		sessions: sessionRows,
	})
	if changed {
		l.publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
	}
	return next, prev, changed, nil
}

// commit is one finished scan on its way into the cache.
type commit struct {
	subs    int
	collect Include
	carry   Include
	health  bool
	// seq is the number this scan took when its OS half began.
	seq      uint64
	refresh  uint64
	pp       []ports.ListeningPort
	rows     []state.Port
	groups   []state.Group
	sessions []state.SessionRecord
}

// commitScan swaps a finished scan into the cache and adapts the interval.
// Caller holds the ordering gate and commitMu; this takes l.mu for the swap
// itself.
func (l *Loop) commitScan(c commit) (next, prev state.Snapshot, changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.subs = c.subs
	prev = l.snap
	rows, groupRows := c.rows, c.groups

	l.scans++
	if l.haveSnap && c.seq < l.committedSeq {
		// Superseded. This scan asked the OS what was listening before the
		// snapshot on the wire was captured, so committing it would put back
		// ports a newer scan has already seen go — the "no going backwards"
		// half of contract §38, now enforced at the commit rather than by
		// forbidding two scans to overlap at all (contract §44).
		return prev, prev, false
	}

	if c.seq >= l.outcomeSeq {
		l.outcomeSeq = c.seq
		l.lastErr = nil
		l.lastAttemptAt = l.now()
	}

	// Carry forward every probe result this scan did not take: between health
	// cadences, for a configured probe the round's budget cut short, and for a
	// scan an RPC started that had no reason to pay for a subscriber's health
	// (contract §44). Without it a port flickers between "checked" and
	// "unknown" on the wire.
	carryHealth(prev.Ports, rows, (c.collect.Health || c.carry.Health) && !c.health)

	// Same for stats. A scan an RPC started collects none unless the caller
	// asked, so it copies the subscribers' last readings across rather than
	// publishing a snapshot with the numbers stripped out — and the 1 s stats
	// tick refreshes them within the second anyway (contract §42).
	if c.carry.Stats && !c.collect.Stats {
		carryStats(prev.Ports, rows)
	}
	// Service rows are built before health is probed. Rejoin after carrying
	// health so service-level readiness is the same fact dependencies, status
	// and subscribers observe.
	syncConfiguredHealth(rows, groupRows)
	withStats := c.collect.Stats || c.carry.Stats

	l.lastScanAt = l.now()
	l.committedSeq = c.seq
	l.refreshObserved = c.refresh
	l.refreshAttempted = max(l.refreshAttempted, c.refresh)
	if c.collect.Stats {
		l.statsScanSeq = c.seq
	}
	l.lastPorts = c.pp
	if c.health {
		l.lastHealthAt = l.lastScanAt
	}

	// Every collection is always an array, never null.
	local := state.Rows{
		Ports:    rows,
		Groups:   groupRows,
		Sessions: c.sessions,
	}.Tag(state.LocalhostName).Normalize()

	next = local.Into(state.Snapshot{
		At:            l.lastScanAt.Format(time.RFC3339),
		DaemonVersion: l.opts.DaemonVersion,
	})

	if l.haveSnap && !snapshotChanged(prev, next, withStats) {
		// Nothing to publish: keep the previous seq and back the interval off.
		next.Seq = prev.Seq
		l.snap = next
		l.interval = backoff(l.interval, l.base, l.maxIntervalLocked())
		return next, prev, false
	}

	l.seq++
	next.Seq = l.seq
	l.snap = next
	l.haveSnap = true
	l.interval = l.base
	return next, prev, true
}

// dueIn is how long until the next scan is due: the current interval minus the
// age of the last attempt, unless a new invalidation requires an observation.
// A failed attempt acknowledges scheduling, not freshness. Zero means now.
func (l *Loop) dueIn() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.refreshAttempted < l.refreshRequested {
		return 0
	}
	at := l.lastAttemptAt
	if at.IsZero() {
		at = l.lastScanAt
	}
	if at.IsZero() {
		return 0
	}
	return l.interval - l.now().Sub(at)
}

// healthDue reports whether the health cadence has elapsed.
func (l *Loop) healthDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.lastHealthAt.IsZero() || l.now().Sub(l.lastHealthAt) >= HealthCadence
}

// maxIntervalLocked is the ceiling the backoff may reach right now: 2.5x the
// base while anyone is subscribed, so a live view never lags a change by more
// than that, and 5x when the daemon only answers RPC reads. At the default 2 s
// base those are the 5 s and 10 s of the spec. Caller holds the mutex.
func (l *Loop) maxIntervalLocked() time.Duration {
	factor := float64(IdleMaxFactor)
	if l.subs > 0 {
		factor = SubscribedMaxFactor
	}
	return time.Duration(float64(l.base) * factor)
}

// backoff multiplies the interval by 1.5, floored at base and capped at max.
func backoff(d, base, max time.Duration) time.Duration {
	next := time.Duration(float64(d) * BackoffFactor)
	if next > max {
		return max
	}
	if next < base {
		return base
	}
	return next
}

// snapshotChanged reports whether the diff between two snapshots is non-empty.
func snapshotChanged(prev, next state.Snapshot, withStats bool) bool {
	var d state.Delta
	if withStats {
		d = state.DiffWithStats(prev, next)
	} else {
		d = state.Diff(prev, next)
	}
	return len(d.Ports.Added) > 0 || len(d.Ports.Updated) > 0 || len(d.Ports.Removed) > 0 ||
		len(d.Groups.Added) > 0 || len(d.Groups.Updated) > 0 || len(d.Groups.Removed) > 0 ||
		len(d.Sessions.Added) > 0 || len(d.Sessions.Updated) > 0 || len(d.Sessions.Removed) > 0
}

// carryHealth copies health results from the previous snapshot onto rows that
// were not probed this tick.
//
// all says whether to carry every result or only the configured ones. A
// `oberth.yaml` health path is state rather than an opt-in statistic
// (contract §22), so its last verdict is always better than the "unknown" a
// skipped probe would publish; an opt-in probe is only carried while somebody
// is still asking for health.
func carryHealth(prev []state.Port, next []state.Port, all bool) {
	if len(prev) == 0 {
		return
	}
	byKey := make(map[string]*state.Port, len(prev))
	for i := range prev {
		byKey[prev[i].Key()] = &prev[i]
	}
	for i := range next {
		if next[i].Health != nil {
			continue
		}
		previous, ok := byKey[next[i].Key()]
		if !ok || previous.Health == nil || !sameRuntime(*previous, next[i]) {
			continue
		}
		if all || previous.Health.Configured {
			next[i].Health = previous.Health
		}
	}
}

// carryStats copies the previous snapshot's stats onto rows that were not
// sampled this scan, keyed the same way health is. A port that appeared in
// this scan and has no previous reading keeps a null `stats`; the 1 s stats
// tick fills it in on its next pass.
func carryStats(prev []state.Port, next []state.Port) {
	mergeStats(prev, next, false)
}

// replace is used only for re-attribution of the old OS batch: its embedded
// stats predate the sampler, so the published snapshot wins for the same run.
// A new OS scan instead keeps the fresh values it actually sampled.
func mergeStats(prev []state.Port, next []state.Port, replace bool) {
	if len(prev) == 0 {
		return
	}
	byKey := make(map[string]*state.Port, len(prev))
	for i := range prev {
		byKey[prev[i].Key()] = &prev[i]
	}
	for i := range next {
		if next[i].Stats != nil && !replace {
			continue
		}
		if previous, ok := byKey[next[i].Key()]; ok && sameRuntime(*previous, next[i]) {
			next[i].Stats = previous.Stats
		}
	}
}

// deriveEvents turns a snapshot transition into the discrete events the tray
// and app show as notifications.
func deriveEvents(prev, next state.Snapshot, at string) []state.Event {
	d := state.DiffWithStats(prev, next)
	groups := make([]state.Group, 0, len(prev.Groups)+len(next.Groups))
	groups = append(groups, prev.Groups...)
	groups = append(groups, next.Groups...)

	// A key that is both removed and added in one delta is a restart.
	removed := make(map[string]bool, len(d.Ports.Removed))
	for _, k := range d.Ports.Removed {
		removed[k] = true
	}

	events := make([]state.Event, 0, len(d.Ports.Added)+len(d.Ports.Removed))
	restarted := make(map[string]bool, len(d.Ports.Added))
	for i := range d.Ports.Added {
		p := d.Ports.Added[i]
		kind := "port_up"
		if removed[p.Key()] {
			kind, restarted[p.Key()] = "port_restarted", true
		}
		events = append(events, eventForPort(next.Seq, kind, at, p, groups))
	}
	before := make(map[string]state.Port, len(prev.Ports))
	for _, p := range prev.Ports {
		before[p.Key()] = p
	}
	for _, key := range d.Ports.Removed {
		if restarted[key] {
			continue
		}
		p := before[key]
		events = append(events, eventForPort(next.Seq, "port_down", at, p, groups))
	}
	for i := range d.Ports.Updated {
		p := d.Ports.Updated[i]
		old := before[p.Key()]
		if healthStatus(old.Health) != healthStatus(p.Health) {
			ev := eventForPort(next.Seq, "health_changed", at, p, groups)
			ev.Data = map[string]any{"from": healthStatus(old.Health), "to": healthStatus(p.Health)}
			events = append(events, ev)
		}
	}
	// An unchanged group cannot introduce a new readiness timeout. Reuse the
	// delta already computed instead of rejoining every service on idle ticks.
	if len(d.Groups.Added)+len(d.Groups.Updated) == 0 {
		return events
	}
	changedGroups := make(map[string]bool, len(d.Groups.Added)+len(d.Groups.Updated))
	for _, group := range d.Groups.Added {
		changedGroups[group.Key()] = true
	}
	for _, group := range d.Groups.Updated {
		changedGroups[group.Key()] = true
	}
	// These indexes are per transition and read-only. Build them only when a
	// ready_timeout needs comparison; changed groups must not cost G² + S².
	var previousGroups map[string]*state.Group
	for _, group := range next.Groups {
		if !changedGroups[group.Key()] {
			continue
		}
		var previousServices map[string]*state.Service
		for _, service := range group.Services {
			if service.LastExit == nil || service.LastExit.Reason != "ready_timeout" {
				continue
			}
			if previousGroups == nil {
				previousGroups = make(map[string]*state.Group, len(prev.Groups))
				for i := range prev.Groups {
					key := prev.Groups[i].Key()
					if _, seen := previousGroups[key]; !seen {
						previousGroups[key] = &prev.Groups[i]
					}
				}
			}
			if previousServices == nil {
				previousServices = make(map[string]*state.Service)
				if before := previousGroups[group.Key()]; before != nil {
					previousServices = make(map[string]*state.Service, len(before.Services))
					for i := range before.Services {
						name := before.Services[i].Name
						if _, seen := previousServices[name]; !seen {
							previousServices[name] = &before.Services[i]
						}
					}
				}
			}
			if previous, exists := previousServices[service.Name]; exists && sameServiceExit(previous.LastExit, service.LastExit) {
				continue
			}
			groupName := group.Name
			events = append(events, state.Event{
				EventID: state.NewEventID(next.Seq, "ready_timeout", group.Key()+":"+service.Name),
				Seq:     next.Seq,
				Kind:    "ready_timeout",
				At:      at,
				Source:  state.EventSourceForGroup(group),
				Group:   &groupName,
				Data:    map[string]any{"service": service.Name, "reason": "ready_timeout"},
			})
		}
	}
	return events
}

func sameServiceExit(a, b *state.ServiceExit) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func eventForPort(seq uint64, kind, at string, p state.Port, groups []state.Group) state.Event {
	key := p.Key()
	return state.Event{
		EventID: state.NewEventID(seq, kind, key),
		Seq:     seq,
		Kind:    kind,
		At:      at,
		Source:  state.EventSourceForPort(p, groups),
		Port:    &p,
		Group:   p.Group,
	}
}

func healthStatus(h *state.Health) string {
	if h == nil {
		return ""
	}
	return h.Status
}

func (l *Loop) nowRFC3339() string { return l.now().Format(time.RFC3339) }

// Snapshot returns the current state for an RPC read. It reuses the cached
// scan when it is younger than CacheTTL and already carries everything the
// caller asked for; otherwise it scans now. Either way it wakes the loop, so
// reading state snaps the interval back to the base (spec, "Scanner loop").
func (l *Loop) Snapshot(include Include) (state.Snapshot, error) {
	return l.SnapshotAll(include)
}

// SnapshotAll returns the current local state.
func (l *Loop) SnapshotAll(include Include) (state.Snapshot, error) {
	return l.SnapshotAllContext(context.Background(), include)
}

// SnapshotAllContext permits last-good fallback only for collection failures,
// never for an owner that has cancelled its request.
func (l *Loop) SnapshotAllContext(ctx context.Context, include Include) (state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	defer l.Wake()

	if snap, ok := l.cached(include); ok {
		return snap, nil
	}
	return l.scanNowContext(ctx, include, mayCoalesce)
}

// SnapshotForReadiness reads the SAME attributed/health-probed state as an
// ordinary snapshot, but an active dependency wait uses the scan floor rather
// than the idle read TTL. Concurrent waiters reuse a completed fresh round;
// they do not each force an OS scan. Failures are returned, never hidden by a
// last-good snapshot. This neither changes the periodic cadence nor claims
// that a registered run has already bound a listener.
func (l *Loop) SnapshotForReadiness() (state.Snapshot, error) {
	return l.SnapshotForReadinessContext(context.Background())
}

// SnapshotForReadinessContext carries the dependency wait's ownership through
// queueing, collection and configured health. Cancellation is not cached.
func (l *Loop) SnapshotForReadinessContext(ctx context.Context) (state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	defer l.Wake()
	if snap, ok := l.cachedWithin(Include{}, MinScanInterval); ok {
		return snap, nil
	}
	return l.scanNowContext(ctx, Include{}, readinessScan)
}

// A failed active observation is reusable as an error, not as stale success.
// This keeps 250 ms dependency polls from spawning a failing collector four
// times per second. A new invalidation is allowed to trigger one new attempt.
func (l *Loop) recentReadinessError() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	age := l.now().Sub(l.lastAttemptAt)
	if l.lastErr != nil && l.refreshAttempted == l.refreshRequested && age >= 0 && age < MinScanInterval {
		return l.lastErr
	}
	return nil
}

// Rescan scans now whatever the cache says, publishes the change it finds and
// only then returns. It is what a write path calls: `ports.rename`,
// `groups.assign` and the group config writes need the delta carrying the
// change to be on the wire before their own reply is (contract §18), and a
// kill needs its selectors resolved against what is listening this instant.
//
// It is not Invalidate + Snapshot. That pair has a window between the two
// calls: a tick landing in it refreshes lastScanAt from a scan that began
// before the write, Snapshot then finds the cache "fresh" and serves it, and
// the write reaches no one until the next tick — up to SubscribedMaxInterval
// later. Rescan closes the window by holding scanMu across both halves.
func (l *Loop) Rescan(include Include) (state.Snapshot, error) {
	return l.RescanContext(context.Background(), include)
}

// RescanContext remains an independent forced observation, not a shared read.
func (l *Loop) RescanContext(ctx context.Context, include Include) (state.Snapshot, error) {
	defer l.Wake()
	snap, err := l.scanNowContext(ctx, include, forceScan)
	return snap, err
}

// Republish makes a store or `oberth.yaml` write visible on the wire without
// scanning the machine (contract §44).
//
// The write changed how the ports the daemon already knows are *named and
// grouped*, not which ports exist, so re-running attribution over the last
// scan's own OS rows answers it exactly. It takes the scan lock, so it still
// cannot interleave with a scan and §38's rule holds: a scan that started
// before the write commits first and this publish supersedes it, a scan that
// starts after it sees the write. What it no longer does is make a "Save"
// button wait for `lsof`, `ps` and `docker stats`.
//
// It wakes the loop on the way out, so a real scan follows and picks up
// anything that started or stopped meanwhile. Before the first scan, or if the
// work cannot finish inside the cooperative ScanLockBudget, it falls back to
// a real scan or leaves an invalidation for the next observer respectively.
// Filesystem/store callbacks already in progress still have to return; the
// budget does not claim to interrupt arbitrary synchronous I/O.
func (l *Loop) Republish() error {
	// A persisted write owns its follow-up independently of the request that
	// saved it, but admission and cooperative work share one finite budget.
	ctx, cancel := context.WithTimeout(context.Background(), ScanLockBudget)
	defer cancel()
	return l.RepublishContext(ctx)
}

// commitRepublish swaps a re-attributed snapshot into the cache. It is
// commitScan minus everything that belongs to an OS scan: it does not touch
// the scan counters, `lastScanAt`, the interval or the machine's load row, so
// the RPC cache TTL and `daemon status` read exactly what they would have if
// the write had never happened. Caller holds the ordering gate and commitMu.
func (l *Loop) commitRepublish(
	subs int, carry Include,
	rows []state.Port, groupRows []state.Group, sessionRows []state.SessionRecord,
) (next, prev state.Snapshot, changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.subs = subs
	prev = l.snap

	carryHealth(prev.Ports, rows, true)
	syncConfiguredHealth(rows, groupRows)
	mergeStats(prev.Ports, rows, true)

	local := state.Rows{
		Ports:    rows,
		Groups:   groupRows,
		Sessions: sessionRows,
	}.Tag(state.LocalhostName).Normalize()

	next = local.Into(state.Snapshot{
		At:            l.now().Format(time.RFC3339),
		DaemonVersion: l.opts.DaemonVersion,
	})
	if !snapshotChanged(prev, next, true) {
		// A write that changed nothing a client can see publishes nothing and
		// burns no seq, the same rule the stats tick follows.
		return prev, prev, false
	}

	l.seq++
	next.Seq = l.seq
	l.snap = next
	return next, prev, true
}

// scanMode says whether a caller may be given somebody else's scan.
type scanMode uint8

const (
	// mayCoalesce is the read path. A scan that *began* after this call did
	// saw everything this caller could have seen, so waiting for the one in
	// flight and then running a second is pure duplication.
	mayCoalesce scanMode = iota
	// forceScan is the write and kill path. `ports.kill` and `groups.kill`
	// resolve their selectors against the scan they ask for (contract §17,
	// §25) and a write's fallback republish has to see its own change, so
	// these always scan — never the one that happened to be in flight, and
	// never the one that finished a moment ago.
	forceScan
	// readinessScan is an active dependency wait, not a control mutation.
	// Reuse an observation at most MinScanInterval old, including one that
	// finished while waiting for rpcGate. It never falls back after failure.
	readinessScan
)

// scanNow is the uncached half of both: one scan under the run gate, published
// before it returns.
func (l *Loop) scanNow(include Include, mode scanMode) (state.Snapshot, error) {
	return l.scanNowContext(context.Background(), include, mode)
}

func (l *Loop) scanNowContext(ctx context.Context, include Include, mode scanMode) (state.Snapshot, error) {
	ctx, cancel := observationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	// Sampled before the gate: any scan that begins while this call waits
	// takes a higher number, which is what "began after I asked" means
	// without consulting a clock.
	asked := l.scanGeneration()
	if err := lockForContext(ctx, l.rpcGate, ScanLockBudget); err != nil {
		if err := ctx.Err(); err != nil {
			return state.Snapshot{}, err
		}
		// Only reads may fall back. A control request must not resolve targets
		// against an observation from before the forced scan it required.
		if mode != mayCoalesce {
			return state.Snapshot{}, fmt.Errorf("scanner busy for more than %s; fresh observation unavailable", ScanLockBudget)
		}
		l.opts.Logger.Warn("scanner busy, serving the last snapshot", "waited", ScanLockBudget)
		return l.Cached(), nil
	}
	defer unlock(l.rpcGate)

	if mode == readinessScan {
		if err := l.recentReadinessError(); err != nil {
			return state.Snapshot{}, err
		}
		if snap, ok := l.cachedWithin(include, MinScanInterval); ok {
			return snap, nil
		}
	}
	if mode == mayCoalesce {
		if snap, ok := l.scannedSince(asked, include); ok {
			return snap, nil
		}
	}

	next, prev, _, err := l.scanLockedContext(ctx, include, l.demandInclude(), false)
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if err != nil {
		if mode == mayCoalesce && prev.Seq > 0 {
			// Read-only requests retain the documented last-good fallback.
			return prev, nil
		}
		return state.Snapshot{}, err
	}
	return next, nil
}

// beginScan captures both scan order and the changes known before OS I/O.
func (l *Loop) beginScan() (generation, refresh uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scanSeq++
	return l.scanSeq, l.refreshRequested
}

// scanGeneration is the number of the last scan to have begun.
func (l *Loop) scanGeneration() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scanSeq
}

// scannedSince reports the cached snapshot when a scan that began strictly
// after generation gen has already committed one carrying what the caller
// asked for.
//
// Strictly after, and counted rather than timed: a scan numbered gen is the
// one that was already running (or had just finished) when the caller asked,
// and serving its result would be serving state from before the call. That
// distinction is invisible to `time.Now` on a platform whose clock moves in
// milliseconds, which is how a kill's rescan came to be skipped on Windows and
// nowhere else.
//
// Health never coalesces either way: the probes run on their own cadence, so
// "a scan happened" says nothing about whether it probed.
func (l *Loop) scannedSince(gen uint64, include Include) (state.Snapshot, bool) {
	if include.Health {
		return state.Snapshot{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap || l.lastErr != nil || l.committedSeq <= gen || l.refreshObserved != l.refreshRequested {
		return state.Snapshot{}, false
	}
	if include.Stats && !l.snapHasStats() {
		return state.Snapshot{}, false
	}
	return l.snap, true
}

// cached returns the cached snapshot when it is younger than CacheTTL and
// already carries everything the caller asked for.
func (l *Loop) cached(include Include) (state.Snapshot, bool) {
	return l.cachedWithin(include, CacheTTL)
}

func (l *Loop) cachedWithin(include Include, maxAge time.Duration) (state.Snapshot, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	age := l.now().Sub(l.lastScanAt)
	fresh := l.haveSnap && l.lastErr == nil && l.refreshObserved == l.refreshRequested && age >= 0 && age < maxAge
	covers := !include.Stats || l.snapHasStats()
	return l.snap, fresh && covers
}

// demandInclude is what the subscribers want a published snapshot to keep.
//
// A scan started by a read is published like any other, so it must not drop
// what a tick would have carried: without this a `oberth list` in the middle of
// a subscribed session would publish a snapshot with the stats stripped out,
// and every subscriber that opted into them would see them blink away and
// back. It used to be *collected* again, which put `docker stats` and a round
// of health probes on the critical path of every kill and every write; it is
// copied forward now instead (contract §44).
func (l *Loop) demandInclude() Include {
	_, want := l.opts.Demand()
	return want
}

// snapHasStats reports whether the cached snapshot carries stats. Caller holds
// the mutex.
func (l *Loop) snapHasStats() bool {
	for i := range l.snap.Ports {
		if l.snap.Ports[i].Stats != nil {
			return true
		}
	}
	return len(l.snap.Ports) == 0
}

// Status is what `daemon.status` reports about the scanner.
type Status struct {
	LastScanAt time.Time
	// IntervalMs is the adaptive cadence right now: somewhere between the
	// base and whichever ceiling currently applies.
	IntervalMs int
	// BaseIntervalMs and StatsIntervalMs are the effective settings behind
	// it: `daemon.scan_interval` and `daemon.stats_interval` after clamping.
	// Both are fixed for the life of the daemon, so `daemon.status` is where
	// you check that an edit to the config file took.
	BaseIntervalMs  int
	StatsIntervalMs int
	Scans           int64
	Seq             uint64
	LastError       error
}

// Status returns the scanner's current counters.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Status{
		LastScanAt:      l.lastScanAt,
		IntervalMs:      int(l.interval / time.Millisecond),
		BaseIntervalMs:  int(l.base / time.Millisecond),
		StatsIntervalMs: int(l.statsInterval() / time.Millisecond),
		Scans:           l.scans,
		Seq:             l.seq,
		LastError:       l.lastErr,
	}
}
