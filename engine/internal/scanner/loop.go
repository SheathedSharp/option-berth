// Package scanner is the runtime service's one scan goroutine.
//
// A scan is an OS sweep plus a Docker round trip, and two of them on one
// machine measure each other; there is exactly one. The loop owns four things:
// asking for an observation, numbering it in the order it began, deciding
// whether it may still be published, and publishing it as one
// (previous, next) pair. Turning that pair into deltas, events and
// per-subscriber filters is the runtime service's business, not this package's.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The loop runs two cadences and has two ceilings for the adaptive one.
//
// The port scan samples at BaseInterval while something is changing, backs off
// by BackoffFactor per unchanged tick, and stops at SubscribedMaxInterval when
// somebody is watching or MaxInterval when only RPC reads are left. Both
// ceilings scale with the configured base, so changing `runtime_service.scan_interval`
// moves the whole curve rather than just its floor.
const (
	// BaseInterval is the cadence while something is changing, and the default
	// for `runtime_service.scan_interval`.
	BaseInterval = 2 * time.Second

	// MinScanInterval is as fast as `runtime_service.scan_interval` may be set. A scan
	// is an `lsof`/`ss`/`netstat` sweep plus a Docker round trip; below a
	// second the machine spends more time being measured than running.
	MinScanInterval = 1 * time.Second

	// MaxInterval and SubscribedMaxInterval are the two ceilings the backoff
	// may reach. A live view must not lag a change by more than the second, so
	// having a subscriber tightens the ceiling.
	MaxInterval           = 10 * time.Second
	SubscribedMaxInterval = 5 * time.Second

	// SubscribedMaxFactor and IdleMaxFactor scale those ceilings with the base
	// interval. At the 2 s default they reproduce the two values above.
	SubscribedMaxFactor = 2.5
	IdleMaxFactor       = 5

	// BackoffFactor multiplies the interval after a scan that changed nothing.
	BackoffFactor = 1.5

	// CacheTTL is how long an RPC read may reuse the last scan.
	CacheTTL = 2 * time.Second

	// HealthCadence keeps the opt-in HTTP probes off the port-scan cadence.
	// HealthTimeout bounds one probe; HealthBudget bounds a whole round however
	// many ports are listening. Ten probes run at once, so without the ceiling
	// forty listeners would cost four waves of HealthTimeout, and a write
	// handler's republish queues behind the scan paying for it.
	HealthCadence = 10 * time.Second
	HealthTimeout = 2 * time.Second
	HealthBudget  = 4 * time.Second

	// ConfiguredHealthBudget is the same round ceiling for `oberth.yaml`
	// health paths, which are probed every tick rather than on HealthCadence.
	ConfiguredHealthBudget = 2 * time.Second

	// ScanLockBudget bounds RPC gate admission and the cooperative follow-up
	// Republish owns. It is not a hard limit on synchronous filesystem or store
	// calls already under way, and a forced observation never turns a gate
	// failure into last-good success.
	ScanLockBudget = 15 * time.Second

	// StatsInterval is the fixed cadence of the stats-only tick: per-process
	// cpu and memory plus this machine's load row, with no port scan behind it.
	// It never backs off, and only runs while somebody is subscribed.
	StatsInterval = 1 * time.Second

	// MinStatsInterval is as fast as `runtime_service.stats_interval` may be set. Below
	// it the sampler costs more than the numbers it publishes: every tick is a
	// `ps` per machine on Unix and a PowerShell on Windows.
	MinStatsInterval = 250 * time.Millisecond
)

// Include is the per-subscriber opt-in that a `state.subscribe {include}`
// request carries.
type Include struct {
	Stats  bool
	Health bool
}

// Demand reports what the runtime service's subscribers currently want. Zero
// subscribers park the loop until Wake is called.
type Demand func() (subscribers int, include Include)

// Publisher receives every published change. prev is the snapshot the delta is
// computed against; next is the new state. Events are already derived.
type Publisher func(prev, next state.Snapshot, events []state.Event)

// Options configures a Loop. Only a collector is defaulted; the rest may be
// zero.
type Options struct {
	RuntimeServiceVersion string
	Logger                *slog.Logger
	Demand                Demand
	Publish               Publisher

	// Store persists renames, group pins, known `oberth.yaml` roots and the
	// port history ring. Nil means the loop scans without a database.
	Store Store

	// Runs returns the run registry group attribution consults. It is a
	// function because `oberth start` installs the registry only after the loop
	// exists; nil, or a nil return, means groups.PortRuns{}.
	Runs func() groups.Registry

	// Sessions builds the snapshot's `sessions` collection out of the ports the
	// tick just resolved. It is a function for the same reason Runs is: the
	// runtime service installs it from an OnStart hook, once the loop exists. Nil
	// publishes an empty collection.
	Sessions func(ports []state.Port) []state.SessionRecord

	// Scan overrides the OS scan with a synchronous callback. Tests use it;
	// production leaves it nil and gets ports.Scan with container data from a
	// docker.Watcher's cached list rather than an inline `docker ps`. The
	// runtime service never waits on the Docker CLI for a scan.
	Scan func(include Include) ([]ports.ListeningPort, error)

	// ScanContext takes precedence over Scan and carries the observation's
	// ownership. Legacy Scan callbacks remain synchronous and must return on
	// their own.
	ScanContext func(ctx context.Context, include Include) ([]ports.ListeningPort, error)

	// ScanSource takes precedence over ScanContext and also reports which
	// collector answered. Production uses this seam: a narrow source like the
	// Darwin lsof fallback cannot see other users' sockets, so the snapshot has
	// to publish that coverage with the rows instead of letting absence read as
	// "not listening".
	ScanSource func(ctx context.Context, include Include) ([]ports.ListeningPort, string, error)

	// Probe overrides the health probe. Tests inject a fake; production leaves
	// it nil and gets ports.ProbeHealth. Configured health goes through this
	// seam, so tests need not open a real socket.
	Probe Probe

	// ProbeContext takes precedence over Probe and propagates scan ownership to
	// configured HTTP health checks. Nil uses the legacy override, if any,
	// otherwise the context-aware production HTTP adapter.
	ProbeContext ProbeContext

	// SampleStats reads cpu, memory, state and uptime for pids the last
	// snapshot already named — the stats-only tick's one OS call. Tests inject
	// a synchronous fake; production leaves it nil and gets the context-aware
	// ports.SampleProcStatsContext. Overrides must return; they are not
	// detached on cancellation.
	SampleStats func(pids []int) map[int]ports.ProcSample

	// ScanInterval overrides the base port-scan cadence
	// (`runtime_service.scan_interval`): the interval used while something is changing
	// and the floor the backoff returns to. Zero means BaseInterval; anything
	// below MinScanInterval is clamped to it.
	ScanInterval time.Duration

	// StatsInterval overrides the stats-only tick's cadence
	// (`runtime_service.stats_interval`): zero means StatsInterval, and anything under
	// MinStatsInterval is clamped up to it.
	StatsInterval time.Duration

	// Now overrides the clock for tests.
	Now func() time.Time
}

// Loop is the scan goroutine plus the cached snapshot that RPC reads are
// served from.
type Loop struct {
	opts Options
	now  func() time.Time
	// base is the resolved `runtime_service.scan_interval`: the cadence while something
	// is changing and the floor the backoff returns to. Fixed at construction,
	// so nothing locks to read it.
	base time.Duration

	wake chan struct{}
	// statsWake unparks the stats-only tick when a subscriber connects. It is
	// separate from wake so the two ticks park and resume independently.
	statsWake   chan struct{}
	runtimeWake chan struct{}

	attr attribution

	// docker keeps the container list the production scan enriches from,
	// refreshed in the background so no scan — and so no handler queued behind
	// one — ever waits on the Docker CLI. Nil when a test injects its own
	// collector.
	docker *docker.Watcher

	// runGate admits one OS scan at a time. It is held from a scan's first
	// system call to its last, so two scans never overlap and a slow one can
	// never commit port rows on top of a newer one's.
	//
	// A buffered channel rather than a sync.Mutex, because a handler that waits
	// for it has to wait with a deadline, which a mutex cannot express.
	runGate chan struct{}

	// rpcGate does the same for the scans an RPC starts: one at a time, so ten
	// clients reading at once still cost the machine one scan. It is separate
	// from runGate on purpose: a `ports.kill` needs a bare port list and gets
	// it in a moment, while the loop's own tick is collecting stats, which
	// means `docker stats` and a machine-wide process table.
	rpcGate chan struct{}

	// orderGate guards the attribution that reads the store, the commit into
	// the cache and the publish. A scan's attribution happens either wholly
	// before a store write's republish or wholly after it, so a tick that
	// loaded the rename table before that write can no longer commit on top of
	// it and put the old display_name back.
	//
	// Where it starts matters: taking it before the OS scan would make a rename
	// wait out `lsof`, `ps` and `docker stats` for a guarantee that only ever
	// needed the store-reading half. Ordering is about attribution, not about
	// asking the kernel which sockets are open.
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
	// makes a rename or an `oberth.yaml` write cost microseconds.
	lastPorts []ports.ListeningPort
	// lastNarrow carries the last scan's table coverage into a republish, which
	// re-attributes those same rows without scanning again.
	lastNarrow bool
	haveSnap   bool

	// scanSeq numbers scans in the order their OS half begins, and committedSeq
	// is the number of the scan the published snapshot came from. Both are
	// counters, not timestamps, because "did a scan begin after I asked" and
	// "is this scan older than the one already published" are happens-before
	// questions a clock cannot answer — `time.Now` on Windows moves in steps of
	// about 15 ms, which once made a kill's rescan coalesce onto the scan that
	// had just finished.
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
	opts = withDefaults(opts)
	base := resolveScanInterval(opts.ScanInterval)
	l := &Loop{
		opts:        opts,
		now:         opts.Now,
		base:        base,
		interval:    base,
		wake:        make(chan struct{}, 1),
		statsWake:   make(chan struct{}, 1),
		runtimeWake: make(chan struct{}, 1),
		runGate:     make(chan struct{}, 1),
		orderGate:   make(chan struct{}, 1),
		rpcGate:     make(chan struct{}, 1),
	}
	l.attachDockerWatcher(base)
	return l
}

// withDefaults fills in every option a caller may leave nil, so the rest of the
// loop never has to ask whether a seam was provided.
func withDefaults(opts Options) Options {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Demand == nil {
		// Nobody is watching: the loop parks itself until someone asks.
		opts.Demand = func() (int, Include) { return 0, Include{} }
	}
	if opts.Publish == nil {
		opts.Publish = func(state.Snapshot, state.Snapshot, []state.Event) {}
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

// collect asks this loop's collector for the listener table. The seam is read
// per call, never cached, because both the runtime service and tests install a collector
// after New.
//
// Precedence is the documented one: source, then context, then the legacy
// synchronous callback. Only a source can name itself; the other two report an
// empty name, which means the platform's primary table.
func (l *Loop) collect(ctx context.Context, include Include) (rows []ports.ListeningPort, source string, err error) {
	switch {
	case l.opts.ScanSource != nil:
		return l.opts.ScanSource(ctx, include)
	case l.opts.ScanContext != nil:
		rows, err = l.opts.ScanContext(ctx, include)
		return rows, "", err
	default:
		rows, err = l.opts.Scan(include)
		return rows, "", err
	}
}

// attachDockerWatcher gives a loop that has no injected collector the real one,
// with a Docker watcher behind it.
//
// The refresh cadence is the scan cadence: scans poke the watcher and it asks
// Docker at most once per base interval. A refresh that changes the container
// list wakes the loop, so a new container's port is enriched within one tick
// rather than one backed-off interval.
func (l *Loop) attachDockerWatcher(base time.Duration) {
	if l.opts.Scan != nil || l.opts.ScanContext != nil || l.opts.ScanSource != nil {
		return
	}
	l.docker = docker.NewWatcher(docker.WatcherOptions{
		Interval: base,
		Logger:   l.opts.Logger,
		OnChange: l.Wake,
	})
	l.opts.ScanSource = l.osScanSourceContext
}

// resolveScanInterval clamps a configured base scan interval. Zero (the unset
// case) means BaseInterval; there is no "off", because the loop already parks
// itself whenever nothing is subscribed.
func resolveScanInterval(d time.Duration) time.Duration {
	switch {
	case d <= 0:
		return BaseInterval
	case d < MinScanInterval:
		return MinScanInterval
	default:
		return d
	}
}

// osScanSourceContext is osScanContext plus the collector that answered, so a
// narrow fallback table can be published as narrow.
func (l *Loop) osScanSourceContext(ctx context.Context, include Include) ([]ports.ListeningPort, string, error) {
	pp, source, err := ports.ScanContextSource(ctx)
	if err != nil {
		return nil, source, err
	}
	// The runtime service's own self-probe binds a short-lived loopback socket. A
	// regular scan that overlaps it would otherwise publish that socket as an
	// undeclared listener of the checkout the runtime service was started in. The probe
	// keeps using the raw collector, because seeing this process's own
	// listener is exactly what it tests.
	pp = withoutOwnListeners(pp)
	l.docker.Enrich(pp)
	if err := ctx.Err(); err != nil {
		return nil, source, err
	}
	// The owner deadline reaches metadata, cwd and full-stat commands.
	// Filesystem and PEB syscalls remain synchronous, checked between
	// operations.
	if err := ports.EnrichContext(ctx, pp); err != nil {
		return nil, source, err
	}
	if include.Stats {
		if err := ports.EnrichStatsContext(ctx, pp, l.docker.Stats()); err != nil {
			return nil, source, err
		}
	}
	return pp, source, ctx.Err()
}

// withoutOwnListeners drops rows owned by the runtime service process. In production
// option-berth has no TCP listener of its own, so a row with this PID is the
// self-probe's transient socket; publishing it would make the probe look like a
// service the user never declared. Test-injected collectors do not pass through
// this, so a test can still model a listener owned by the same process.
func withoutOwnListeners(pp []ports.ListeningPort) []ports.ListeningPort {
	pid := os.Getpid()
	out := make([]ports.ListeningPort, 0, len(pp))
	for _, row := range pp {
		if row.PID != pid {
			out = append(out, row)
		}
	}
	return out
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

// SetDemand installs the demand callback after construction. The runtime service needs
// the setter because the server and the loop refer to each other.
func (l *Loop) SetDemand(d Demand) {
	if d == nil {
		return
	}
	l.opts.Demand = d
}

// SetPublisher installs the publish callback after the loop is built.
func (l *Loop) SetPublisher(p Publisher) {
	if p == nil {
		return
	}
	l.opts.Publish = p
}

// Cached returns the last published snapshot without scanning.
func (l *Loop) Cached() state.Snapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.haveSnap {
		return state.Rows{}.Normalize().Into(state.Snapshot{At: l.now().Format(time.RFC3339), RuntimeServiceVersion: l.opts.RuntimeServiceVersion})
	}
	return l.snap
}

// Wake nudges a stopped or backed-off loop to scan now. Called when a
// subscriber connects or an RPC reads state.
func (l *Loop) Wake() {
	nudge(l.wake)
	nudge(l.statsWake)
}

// nudge signals ch without blocking. A signal the buffer already holds is left
// alone, so a burst of callers costs one wake-up rather than a queue of them.
func nudge(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// runInBackground starts one auxiliary goroutine and counts it in the wait
// group, so Run returns only once every helper has actually stopped rather
// than merely been told to.
func runInBackground(background *sync.WaitGroup, ctx context.Context, work func(context.Context)) {
	background.Add(1)
	go func() {
		defer background.Done()
		work(ctx)
	}()
}

// Run drives the loop until ctx is cancelled. With zero subscribers it parks on
// Wake and does no work at all; the next RPC or subscription starts it again.
//
// It also runs the stats-only tick (see runStats) and, in production, the
// Docker watcher, each in its own goroutine, and returns only once both have
// stopped. Commits and publication are serialized by commitMu, while their OS
// work may overlap: a 1 s load sample does not have to wait for — or reset — an
// adaptive port scan that may be 5 s apart.
func (l *Loop) Run(ctx context.Context) {
	var background sync.WaitGroup
	runInBackground(&background, ctx, l.runStats)
	runInBackground(&background, ctx, l.runRuntime)
	if l.docker != nil {
		// The watcher refreshes the container list scans read, in its own
		// goroutine for the same reason as the stats tick: nothing on the scan
		// path waits on the Docker CLI.
		runInBackground(&background, ctx, l.docker.Run)
	}
	defer background.Wait()

	timer := time.NewTimer(time.Hour)
	defer timer.Stop()

	for {
		if ctx.Err() != nil {
			return
		}
		include, scanNow, finished := l.waitForDemand(ctx)
		switch {
		case finished:
			return
		case !scanNow:
			continue // a wake arrived; re-read the demand
		}
		if l.waitTick(ctx, timer, include) {
			return
		}
	}
}

// waitTick holds the loop until the cadence says a scan is due, then until
// the timer, cancellation, or a wake. A scan an RPC ran a moment ago is this
// tick's scan. Wake means "the state you are holding may be stale", and a
// scan younger than the current interval is not: without this floor every
// `ports.kill`, every `oberth list` that missed the cache and every write
// woke the loop into a second full scan.
//
// The floor is the loop's cadence and nothing else. A handler that asks for
// a scan gets one: Rescan is a forced scan and never consults this, because
// resolving kill targets against the previous tick is the bug the scan
// counters exist to prevent.
func (l *Loop) waitTick(ctx context.Context, timer *time.Timer, include Include) (stop bool) {
	wait := l.dueIn()
	if wait <= 0 {
		l.scanAndPublishContext(ctx, include)
		wait = l.dueIn()
	}
	if wait <= 0 {
		wait = l.base
	}
	armTimer(timer, wait)
	select {
	case <-ctx.Done():
		return true
	case <-timer.C:
		return false
	case <-l.wake:
		// Somebody is reading or has just subscribed: drop back to the base
		// interval rather than serve them a backed-off cadence.
		l.mu.Lock()
		l.interval = l.base
		l.mu.Unlock()
		return false
	}
}

// armTimer restarts timer for wait. Reset on a timer that may already have
// fired leaves the old value in the channel, so the next select would take it
// immediately and spin; draining first is what makes Reset safe. Stop reports
// whether it stopped the timer, and only then is there certainly nothing to
// drain.
func armTimer(timer *time.Timer, delay time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(delay)
}

// waitForDemand answers what this tick should collect, and whether it should
// collect anything at all. scanNow is false when a wake arrived while parked:
// the caller re-reads Demand, because the wake may have been the subscriber
// that makes scanning worth doing. finished means ctx ended.
func (l *Loop) waitForDemand(ctx context.Context) (include Include, scanNow, finished bool) {
	subscribers, wanted := l.opts.Demand()
	if subscribers > 0 {
		return wanted, true, false
	}
	// Parked: no scanning and no timer, so an idle runtime service costs nothing.
	select {
	case <-ctx.Done():
		return Include{}, false, true
	case <-l.wake:
		return Include{}, false, false
	}
}

// abandoned is the result of a scan that stopped early: no new snapshot, the
// one already cached, nothing changed, and the reason. Every early exit in the
// scan path returns this shape.
func (l *Loop) abandoned(err error) (state.Snapshot, state.Snapshot, bool, error) {
	return state.Snapshot{}, l.Cached(), false, err
}

// scanAndPublish runs one tick and publishes what changed.
func (l *Loop) scanAndPublish(include Include) {
	l.scanAndPublishContext(context.Background(), include)
}

// scanAndPublishContext runs one tick under the run gate, which is what keeps
// two publishes from overlapping. The scan's own result is discarded: this path
// exists to refresh the cache and publish, not to answer a caller.
func (l *Loop) scanAndPublishContext(ctx context.Context, include Include) {
	ctx, cancel := observationContext(ctx)
	defer cancel()

	release, admitted := l.admit(ctx)
	if !admitted {
		return
	}
	defer release()

	l.scanLockedContext(ctx, include, Include{}, true)
}

// admit takes the run gate, reporting false when the caller gave up while
// waiting for it. Nothing is held in that case, so the caller must not release.
func (l *Loop) admit(ctx context.Context) (release func(), admitted bool) {
	if lockContext(ctx, l.runGate) != nil {
		return nil, false
	}
	if ctx.Err() != nil {
		unlock(l.runGate)
		return nil, false
	}
	return func() { unlock(l.runGate) }, true
}

// publish hands the transition to the runtime service and then writes its port
// transitions to the history ring, in that order: a client sees the change
// before the disk does.
func (l *Loop) publish(prev, next state.Snapshot, events []state.Event) {
	l.opts.Publish(prev, next, events)
	l.record(events)
}

// scanReading is one collector call, classified.
//
// The four outcomes are deliberately distinct: rows read whole, rows read in
// part (with the verdict that must travel with them), a collector failure, and
// an observation the caller no longer owns. Only the first two may reach a
// commit; a failure keeps the last snapshot without renewing its freshness; a
// withdrawn observation changes nothing at all.
type scanReading struct {
	rows         []ports.ListeningPort
	source       string
	narrow       bool
	partial      bool
	partialCause error
	verdict      *state.Observation
	failure      error
	ownerGone    bool
}

// read runs the loop's collector and classifies what came back.
func (l *Loop) read(ctx context.Context, include Include) scanReading {
	rows, source, err := l.collect(ctx, include)
	if ctx.Err() != nil {
		// Ownership ended, not a collector failure. Preserve the last outcome
		// and freshness, and leave pending invalidation for a later observer.
		return scanReading{ownerGone: true}
	}
	narrow := source != ""
	if err == nil {
		reading := scanReading{rows: rows, source: source, narrow: narrow}
		if narrow {
			reading.verdict = l.observableObservation(source)
		}
		return reading
	}
	if partial, ok := ports.PartialRows(err); ok {
		// The readable rows are facts; the unreadable ones are not. Publish
		// them together with the verdict so no port looks absent.
		reading := scanReading{
			rows: partial, source: source, narrow: narrow, partial: true, partialCause: err,
			verdict: l.partialObservation(err, source),
		}
		if narrow {
			// Production's narrow collector (the Darwin lsof fallback) either
			// answers whole or reports a simultaneous failure, so a named
			// source with partial rows does not occur there. Keep the coverage
			// sentence authoritative if it ever does.
			reading.verdict = l.observableObservation(source)
		}
		return reading
	}
	return scanReading{source: source, narrow: narrow, failure: err}
}

// partialObservation records that a collector answered with the rows it could
// read and a verdict on the rest. The code and hint come from the collector's
// own error, so a sandboxed agent can tell which kind of blindness this was.
func (l *Loop) partialObservation(reason error, source string) *state.Observation {
	partial := &state.Observation{
		State:     state.ObservationPartial,
		Source:    source,
		CheckedAt: l.nowRFC3339(),
	}
	var observed *ports.ObservationError
	if errors.As(reason, &observed) {
		partial.Code = observed.Code
		partial.Detail = observed.Error()
		partial.Hint = observed.Hint
	}
	return partial
}

// observableObservation records that this scan could see the machine, though a
// narrow source (netstat, ss) cannot show every owner. Publishing the coverage
// alongside the rows is what stops "no owner" from reading as "nothing there".
func (l *Loop) observableObservation(source string) *state.Observation {
	return &state.Observation{
		State:     state.ObservationObservable,
		Source:    source,
		Detail:    ports.NarrowSourceDetail(source),
		CheckedAt: l.nowRFC3339(),
	}
}

// scanLocked performs a scan and swaps it into the cache. It returns the new
// snapshot, the one it replaced, and whether anything a client cares about
// changed.
//
// collect is what this scan asks the OS for. carry is what the subscribers want
// the published snapshot to keep but this caller has no reason to pay for:
// those fields are copied forward from the previous snapshot instead of being
// collected again.
func (l *Loop) scanLocked(collect, carry Include, reportFailure bool) (next, prev state.Snapshot, changed bool, err error) {
	return l.scanLockedContext(context.Background(), collect, carry, reportFailure)
}

// scanLockedContext is one scan, in the order the contract requires: read the
// OS, classify what came back, probe health, attribute against the store,
// probe configured health, then commit and publish under the ordering gates.
//
// Every step between the reading and the commit checks the caller's ownership.
// A withdrawn scan never commits a row, never renews a freshness, and never
// publishes its failure as if it were an observation.
func (l *Loop) scanLockedContext(ctx context.Context, collect, carry Include, reportFailure bool) (next, prev state.Snapshot, changed bool, err error) {
	if err := ctx.Err(); err != nil {
		return l.abandoned(err)
	}
	// Read outside the mutex: Demand takes the server's own lock, which the
	// server holds while calling back into the loop.
	subs, _ := l.opts.Demand()
	wantHealth := collect.Health && l.healthDue()

	gen, refresh := l.beginScan()
	reading := l.read(ctx, collect)
	if reading.ownerGone {
		return l.abandoned(ctx.Err())
	}
	if reading.failure != nil {
		if reportFailure {
			l.opts.Logger.Warn("scan failed, keeping last good state", "error", reading.failure)
		}
		return l.recordScanFailure(ctx, gen, refresh, reading.failure, reportFailure, prev)
	}
	if reading.partial && reportFailure {
		l.opts.Logger.Warn("scan partial, publishing readable rows", "error", reading.partialCause)
	}
	if wantHealth {
		ports.EnrichHealthContext(ctx, reading.rows, HealthTimeout, HealthBudget)
	}
	return l.commitOwnedScan(ctx, subs, collect, carry, wantHealth, gen, refresh, reading)
}

// commitOwnedScan is the ordered half of one scan: attribution that reads the
// store, configured health, then commit and publish. It is serialized against
// every store write's republish, and the publish rides inside that lock so a
// later sequence can never reach a client first. A withdrawn caller abandons
// the scan without committing a row or renewing freshness.
func (l *Loop) commitOwnedScan(
	ctx context.Context,
	subs int,
	collect, carry Include,
	wantHealth bool,
	gen, refresh uint64,
	reading scanReading,
) (next, prev state.Snapshot, changed bool, err error) {
	if err := lockContext(ctx, l.orderGate); err != nil {
		return l.abandoned(err)
	}
	defer unlock(l.orderGate)

	rows, groupRows, err := l.attributeContext(ctx, reading.rows, reading.narrow)
	if err != nil {
		return l.abandoned(err)
	}
	sessionRows, err := l.sessionsContext(ctx, rows)
	if err != nil {
		return l.abandoned(err)
	}
	// Configured health comes after attribution because it is the groups that
	// say which port has a `health:` path. It runs on every tick regardless of
	// `include`: a health path in an oberth.yaml is part of what the service
	// is, not an opt-in statistic. Its budget is what keeps this short enough
	// to sit inside the ordering gate.
	if err := ProbeConfiguredContext(ctx, rows, groupRows, l.ProbeContext, ConfiguredHealthBudget); err != nil {
		return l.abandoned(err)
	}
	if err := l.commitMu.LockContext(ctx); err != nil {
		return l.abandoned(err)
	}
	defer l.commitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return l.abandoned(err)
	}

	// Rebase after slow observation: a captured old run must not overwrite a
	// lifecycle projection published while that observation was in flight.
	projected, _, projectErr := l.projectRuntime(ctx, state.Snapshot{Ports: rows, Groups: groupRows}, true)
	if projectErr != nil {
		return l.abandoned(projectErr)
	}
	rows, groupRows = projected.Ports, projected.Groups
	next, prev, changed = l.commitScan(commit{
		subs:        subs,
		collect:     collect,
		carry:       carry,
		health:      wantHealth,
		seq:         gen,
		refresh:     refresh,
		pp:          reading.rows,
		rows:        rows,
		groups:      groupRows,
		sessions:    sessionRows,
		observation: reading.verdict,
		narrow:      reading.narrow,
	})
	if changed {
		l.publish(prev, next, deriveEvents(prev, next, l.nowRFC3339()))
	}
	return next, prev, changed, nil
}

// recordScanFailure accounts for a collector that failed.
//
// Recording is part of publication ordering too, not just bookkeeping: a
// recovery must not commit between deciding this outcome and reporting it, so
// the commit lock is taken here and the result is published under it. An
// attempt a later scan has already superseded records nothing and reports
// nothing — its failure is no longer news.
func (l *Loop) recordScanFailure(
	ctx context.Context,
	gen, refresh uint64,
	failure error,
	reportFailure bool,
	prev state.Snapshot,
) (state.Snapshot, state.Snapshot, bool, error) {
	if lockErr := l.commitMu.LockContext(ctx); lockErr != nil {
		return l.abandoned(lockErr)
	}
	defer l.commitMu.Unlock()
	if err := ctx.Err(); err != nil {
		return l.abandoned(err)
	}

	l.mu.Lock()
	if gen >= l.outcomeSeq {
		l.outcomeSeq = gen
		l.lastErr = failure
		l.lastAttemptAt = l.now()
		l.refreshAttempted = max(l.refreshAttempted, refresh)
	}
	l.scans++
	prev = l.snap
	current := gen == l.outcomeSeq
	stale := !current && l.haveSnap && l.lastErr == nil
	l.mu.Unlock()

	if stale {
		return prev, prev, false, nil
	}
	if reportFailure && current {
		l.opts.Publish(prev, prev, []state.Event{{
			Kind: "scan_error",
			At:   l.nowRFC3339(),
			Data: map[string]any{"error": failure.Error()},
		}})
	}
	return state.Snapshot{}, prev, false, failure
}

// commit is one finished scan on its way into the cache, in three parts: what
// was asked for, in what order it happened, and what came back.
type commit struct {
	// The request.
	subs    int
	collect Include
	carry   Include
	health  bool

	// Ordering. seq is the number this scan took when its OS half began;
	// refresh is the change counter observed at that same moment.
	seq     uint64
	refresh uint64

	// The findings. observation is non-nil only when the table could not be
	// read whole, or was read from a narrow source — the readable rows are
	// still facts either way. narrow says the source cannot see every socket
	// owner, so an absent `machine:` port is unknown rather than false.
	pp          []ports.ListeningPort
	rows        []state.Port
	groups      []state.Group
	sessions    []state.SessionRecord
	observation *state.Observation
	narrow      bool
}

// superseded reports whether a newer scan has already committed its snapshot.
//
// This scan asked the OS what was listening before that snapshot was captured,
// so committing it now would put back ports the newer scan has already seen go
// — the "no going backwards" rule, enforced here at the commit rather than by
// forbidding two scans from overlapping at all.
func (l *Loop) superseded(seq uint64) bool {
	return l.haveSnap && seq < l.committedSeq
}

// commitScan swaps a finished scan into the cache and adapts the interval. The
// caller holds the ordering gate and commitMu; the swap itself takes l.mu.
func (l *Loop) commitScan(c commit) (next, prev state.Snapshot, changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	prev = l.snap
	l.subs = c.subs
	rows, groupRows := c.rows, c.groups

	l.scans++
	if l.superseded(c.seq) {
		return prev, prev, false
	}
	l.noteScanOutcome(c)
	withStats := carryScanFields(prev, rows, groupRows, c)
	return l.storeCommitted(prev, rows, groupRows, c, withStats)
}

// noteScanOutcome records that this generation produced a reading. An older
// generation must not clear a newer failure or move the attempt clock.
func (l *Loop) noteScanOutcome(c commit) {
	if c.seq >= l.outcomeSeq {
		l.outcomeSeq = c.seq
		l.lastErr = nil
		l.lastAttemptAt = l.now()
	}
}

// carryScanFields copies probe results and stats this scan did not take.
// Between health cadences, for a configured probe the round's budget cut
// short, and for a scan an RPC started that had no reason to pay for a
// subscriber's health, the previous verdict stays. Without it a port
// flickers between "checked" and "unknown" on the wire. Service rows are
// built before health is probed, so the join runs after the carry and
// subscribers see the same readiness as dependencies.
func carryScanFields(prev state.Snapshot, rows []state.Port, groupRows []state.Group, c commit) bool {
	carryHealth(prev.Ports, rows, (c.collect.Health || c.carry.Health) && !c.health)
	if c.carry.Stats && !c.collect.Stats {
		carryStats(prev.Ports, rows)
	}
	syncConfiguredHealth(rows, groupRows)
	return c.collect.Stats || c.carry.Stats
}

// storeCommitted writes the scan into the cache. Nothing a client can see
// keeps the previous sequence and backs the interval off; a visible change
// takes the next sequence and returns the loop to its base interval.
func (l *Loop) storeCommitted(prev state.Snapshot, rows []state.Port, groupRows []state.Group, c commit, withStats bool) (next, outPrev state.Snapshot, changed bool) {
	l.lastScanAt = l.now()
	l.committedSeq = c.seq
	l.refreshObserved = c.refresh
	l.refreshAttempted = max(l.refreshAttempted, c.refresh)
	if c.collect.Stats {
		l.statsScanSeq = c.seq
	}
	l.lastPorts = c.pp
	l.lastNarrow = c.narrow
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
		At:                    l.lastScanAt.Format(time.RFC3339),
		RuntimeServiceVersion: l.opts.RuntimeServiceVersion,
		Observation:           c.observation,
	})
	if l.haveSnap && !snapshotChanged(prev, next, withStats) {
		next.Seq = prev.Seq
		l.snap = next
		l.interval = backoff(l.interval, l.base, l.maxIntervalLocked())
		return next, prev, false
	}
	l.seq++
	next.Seq = l.seq
	l.haveSnap = true
	l.snap = next
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

// healthDue reports whether it is time to probe health again. A loop that has
// never probed is always due.
func (l *Loop) healthDue() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.lastHealthAt.IsZero() {
		return true
	}
	return l.now().Sub(l.lastHealthAt) >= HealthCadence
}

// maxIntervalLocked is the ceiling the backoff may reach right now: 2.5x the
// base while anyone is subscribed, so a live view never lags a change by more
// than that, and 5x when the runtime service only answers RPC reads. At the default 2 s
// base those are the 5 s and 10 s of the contract. Caller holds the mutex.
func (l *Loop) maxIntervalLocked() time.Duration {
	scale := float64(IdleMaxFactor)
	if l.subs > 0 {
		scale = SubscribedMaxFactor
	}
	return time.Duration(float64(l.base) * scale)
}

// backoff multiplies the interval by 1.5, with base as its floor and max as its
// ceiling.
func backoff(d, base, max time.Duration) time.Duration {
	next := time.Duration(float64(d) * BackoffFactor)
	switch {
	case next > max:
		return max
	case next < base:
		return base
	default:
		return next
	}
}

// observationEqual keeps the verdict comparison in the state package: the
// runtime service must use the same rule to decide whether a delta needs a frame.
func observationEqual(a, b *state.Observation) bool {
	return state.SameObservation(a, b)
}

// snapshotChanged reports whether the diff between two snapshots is non-empty.
//
// The observation verdict is part of that diff even though no row changed: a
// scan can resolve from "some rows unreadable" to "whole table read" with the
// same ports in it, and holding the stale verdict back would keep publishing a
// partial table as if it were complete. Publishing nothing leaves the old
// verdict in place, which is what a deduplicated tick must not do when the
// verdict itself moved.
func snapshotChanged(prev, next state.Snapshot, withStats bool) bool {
	if !observationEqual(prev.Observation, next.Observation) {
		return true
	}
	return deltaHasRows(snapshotDelta(prev, next, withStats))
}

func snapshotDelta(prev, next state.Snapshot, withStats bool) state.Delta {
	if withStats {
		return state.DiffWithStats(prev, next)
	}
	return state.Diff(prev, next)
}

// deltaHasRows reports whether any collection in the diff carries a row.
// The observation verdict is compared separately: it can move while every
// row stays put.
func deltaHasRows(d state.Delta) bool {
	return changeNonEmpty(d.Ports) || changeNonEmpty(d.Groups) || changeNonEmpty(d.Sessions)
}

func changeNonEmpty[T any](c state.Change[T]) bool {
	return len(c.Added) > 0 || len(c.Updated) > 0 || len(c.Removed) > 0
}

// carryForward walks next and copies one field forward from each row's
// predecessor in previous.
//
// Both carries below are this walk with a different answer to two questions:
// when does a row still need the field, and what exactly gets copied. Keeping
// the walk in one place is what stops the two from drifting apart, since they
// must agree on which rows count as "the same run".
func carryForward(previous, next []state.Port, alreadySet func(*state.Port) bool, copyFrom func(into, from *state.Port)) {
	if len(previous) == 0 {
		return
	}
	byKey := indexByKey(previous)
	for i := range next {
		row := &next[i]
		if alreadySet(row) {
			continue
		}
		before, listed := byKey[row.Key()]
		if !listed || !sameRuntime(*before, *row) {
			continue
		}
		copyFrom(row, before)
	}
}

// indexByKey makes the previous snapshot addressable by port key, so a carry
// can find each row's predecessor without walking the slice again. The
// pointers are into previous, which the carries never mutate.
func indexByKey(previous []state.Port) map[string]*state.Port {
	byKey := make(map[string]*state.Port, len(previous))
	for i := range previous {
		byKey[previous[i].Key()] = &previous[i]
	}
	return byKey
}

// carryHealth copies health results from the previous snapshot onto rows that
// were not probed this tick.
//
// all says whether to carry every result or only the configured ones. An
// `oberth.yaml` health path is state rather than an opt-in statistic, so its
// last verdict always beats the "unknown" a skipped probe would publish; an
// opt-in probe only survives while somebody is still asking for health.
func carryHealth(previous, next []state.Port, all bool) {
	carryForward(previous, next,
		func(row *state.Port) bool { return row.Health != nil },
		func(into, from *state.Port) {
			if from.Health == nil {
				return
			}
			if all || from.Health.Configured {
				into.Health = from.Health
			}
		})
}

// carryStats copies the previous snapshot's stats onto rows that were not
// sampled this scan. A port seen for the first time keeps a null `stats`; the
// 1 s stats tick fills it in on its next pass.
func carryStats(previous, next []state.Port) {
	mergeStats(previous, next, false)
}

// mergeStats carries stats forward. replace is set only when re-attributing the
// old OS batch, whose embedded stats predate the sampler: there the published
// snapshot wins, while a new OS scan keeps the fresh values it sampled.
func mergeStats(previous, next []state.Port, replace bool) {
	carryForward(previous, next,
		func(row *state.Port) bool { return row.Stats != nil && !replace },
		func(into, from *state.Port) { into.Stats = from.Stats })
}

// keySet is the set of the given port keys, for asking "is this one among
// them" while building events.
func keySet(keys []string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, key := range keys {
		set[key] = true
	}
	return set
}

// indexPorts makes a snapshot's rows addressable by key, so a removed key can
// be turned back into the row a `port_down` event needs to carry.
func indexPorts(rows []state.Port) map[string]state.Port {
	byKey := make(map[string]state.Port, len(rows))
	for _, row := range rows {
		byKey[row.Key()] = row
	}
	return byKey
}

// deriveEvents turns a snapshot transition into the discrete events the tray
// and app show as notifications.
func deriveEvents(prev, next state.Snapshot, at string) []state.Event {
	d := state.DiffWithStats(prev, next)
	events := portTransitionEvents(prev, next, at, d)
	// An unchanged group cannot introduce a new readiness timeout. Reuse the
	// delta already computed instead of rejoining every service on idle ticks.
	if len(d.Groups.Added)+len(d.Groups.Updated) == 0 {
		return events
	}
	return appendReadyTimeouts(events, prev, next, at, changedGroupKeys(d))
}

// portTransitionEvents are the listener notifications: a port came up, went
// down, restarted, or its health status moved. A key that is both removed and
// added in one delta is a restart, not a down followed by an up.
func portTransitionEvents(prev, next state.Snapshot, at string, d state.Delta) []state.Event {
	groups := make([]state.Group, 0, len(prev.Groups)+len(next.Groups))
	groups = append(groups, prev.Groups...)
	groups = append(groups, next.Groups...)
	removed := keySet(d.Ports.Removed)
	events := make([]state.Event, 0, len(d.Ports.Added)+len(d.Ports.Removed))
	restartedKeys := make(map[string]bool, len(d.Ports.Added))
	for i := range d.Ports.Added {
		p := d.Ports.Added[i]
		kind := "port_up"
		if removed[p.Key()] {
			kind, restartedKeys[p.Key()] = "port_restarted", true
		}
		events = append(events, eventForPort(next.Seq, kind, at, p, groups))
	}
	before := indexPorts(prev.Ports)
	for _, key := range d.Ports.Removed {
		if restartedKeys[key] {
			continue
		}
		events = append(events, eventForPort(next.Seq, "port_down", at, before[key], groups))
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
	return events
}

func changedGroupKeys(d state.Delta) map[string]bool {
	changed := make(map[string]bool, len(d.Groups.Added)+len(d.Groups.Updated))
	for _, group := range d.Groups.Added {
		changed[group.Key()] = true
	}
	for _, group := range d.Groups.Updated {
		changed[group.Key()] = true
	}
	return changed
}

// priorServices is the previous snapshot's services, indexed only after a
// ready_timeout actually needs a comparison. Changed groups must not cost
// a walk of every group and every service on the tick.
type priorServices struct {
	groups   map[string]*state.Group
	services map[string]map[string]*state.Service
	prev     []state.Group
}

func (p *priorServices) lookup(group state.Group, name string) (*state.Service, bool) {
	if p.groups == nil {
		p.groups = indexPriorGroups(p.prev)
	}
	if p.services == nil {
		p.services = make(map[string]map[string]*state.Service)
	}
	byName, indexed := p.services[group.Key()]
	if !indexed {
		byName = indexGroupServices(p.groups[group.Key()])
		p.services[group.Key()] = byName
	}
	svc, found := byName[name]
	return svc, found
}

func indexPriorGroups(prev []state.Group) map[string]*state.Group {
	groups := make(map[string]*state.Group, len(prev))
	for i := range prev {
		key := prev[i].Key()
		if _, seen := groups[key]; !seen {
			groups[key] = &prev[i]
		}
	}
	return groups
}

func indexGroupServices(group *state.Group) map[string]*state.Service {
	if group == nil {
		return map[string]*state.Service{}
	}
	byName := make(map[string]*state.Service, len(group.Services))
	for i := range group.Services {
		name := group.Services[i].Name
		if _, seen := byName[name]; !seen {
			byName[name] = &group.Services[i]
		}
	}
	return byName
}

func appendReadyTimeouts(events []state.Event, prev, next state.Snapshot, at string, changed map[string]bool) []state.Event {
	prior := priorServices{prev: prev.Groups}
	for _, group := range next.Groups {
		if !changed[group.Key()] {
			continue
		}
		for _, service := range group.Services {
			previous, existed := prior.lookup(group, service.Name)
			if !newReadyTimeout(service, previous, existed) {
				continue
			}
			events = append(events, readyTimeoutEvent(next.Seq, at, group, service.Name))
		}
	}
	return events
}

// newReadyTimeout is a readiness failure this transition has not already
// reported. The same exit on the previous service row is the same fact.
func newReadyTimeout(service state.Service, previous *state.Service, existed bool) bool {
	if service.LastExit == nil || service.LastExit.Reason != "ready_timeout" {
		return false
	}
	return !existed || !sameServiceExit(previous.LastExit, service.LastExit)
}

func readyTimeoutEvent(seq uint64, at string, group state.Group, service string) state.Event {
	groupName := group.Name
	return state.Event{
		EventID: state.NewEventID(seq, "ready_timeout", group.Key()+":"+service),
		Seq:     seq,
		Kind:    "ready_timeout",
		At:      at,
		Source:  state.EventSourceForGroup(group),
		Group:   &groupName,
		Data:    map[string]any{"service": service, "reason": "ready_timeout"},
	}
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

// readSnapshot is the shape every read shares: refuse at once if the caller is
// already gone, snap the interval back to the base when the read finishes,
// serve the cache when it is fresh enough for this caller, and only then scan.
//
// mode is what separates an ordinary read from a readiness wait — how a scan
// may be shared, and what a failure is allowed to fall back to.
func (l *Loop) readSnapshot(
	ctx context.Context,
	include Include,
	mode scanMode,
	freshness func(Include) (state.Snapshot, bool),
) (state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	defer l.Wake()
	if cached, usable := freshness(include); usable {
		return cached, nil
	}
	return l.scanNowContext(ctx, include, mode)
}

// Snapshot returns the current state for an RPC read. It reuses the cached
// scan when it is younger than CacheTTL and already carries everything the
// caller asked for; otherwise it scans now. Either way it wakes the loop, so
// reading state snaps the interval back to the base.
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
	// Refresh the same registry projection before reusing a listener cache.
	// Periodic and RPC-owned observations have distinct admission gates.
	busy := len(l.runGate) > 0 || len(l.rpcGate) > 0
	snap, have, err := l.refreshRuntime(ctx, busy)
	if err != nil {
		return state.Snapshot{}, err
	}
	// A busy observer does not own the progress of already-known runs.
	if have && busy {
		return snap, nil
	}
	return l.readSnapshot(ctx, include, mayCoalesce, l.cached)
}

// SnapshotForReadiness reads the SAME attributed/health-probed state as an
// ordinary snapshot, but an active dependency wait uses the scan floor rather
// than the idle read TTL. Concurrent waiters reuse a completed fresh round;
// they do not each force an OS scan. Failures are returned, never hidden by a
// last-good snapshot. This neither changes the periodic cadence nor claims that
// a registered run has already bound a listener.
func (l *Loop) SnapshotForReadiness() (state.Snapshot, error) {
	return l.SnapshotForReadinessContext(context.Background())
}

// SnapshotForReadinessContext carries the dependency wait's ownership through
// queueing, collection and configured health. Cancellation is not cached.
func (l *Loop) SnapshotForReadinessContext(ctx context.Context) (state.Snapshot, error) {
	// The wait uses the scan floor rather than the idle TTL, and asks for the
	// plain snapshot: a dependency wait is not a subscriber and has no include
	// set of its own.
	fresh := func(Include) (state.Snapshot, bool) {
		return l.cachedWithin(Include{}, MinScanInterval)
	}
	return l.readSnapshot(ctx, Include{}, readinessScan, fresh)
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
// change to be on the wire before their own reply is, and a kill needs its
// selectors resolved against what is listening this instant.
//
// It is not Invalidate + Snapshot. That pair has a window between the two
// calls: a tick landing in it refreshes lastScanAt from a scan that began
// before the write, Snapshot then finds the cache "fresh" and serves it, and
// the write reaches no one until the next tick. Rescan closes the window by
// holding the gates across both halves.
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
// scanning the machine.
//
// The write changed how the ports the runtime service already knows are named and
// grouped, not which ports exist, so re-running attribution over the last
// scan's own OS rows answers it exactly. It takes the ordering gate, so it
// still cannot interleave with a scan: a scan that started before the write
// commits first and this publish supersedes it, a scan that starts after it
// sees the write. What it no longer does is make a "Save" button wait for
// `lsof`, `ps` and `docker stats`.
//
// It wakes the loop on the way out, so a real scan follows and picks up
// anything that started or stopped meanwhile. Before the first scan, or if the
// work cannot finish inside the cooperative ScanLockBudget, it falls back to a
// real scan or leaves an invalidation for the next observer respectively.
// Filesystem and store callbacks already in progress still have to return; the
// budget does not claim to interrupt arbitrary synchronous I/O.
func (l *Loop) Republish() error {
	// A persisted write owns its follow-up independently of the request that
	// saved it, but admission and cooperative work share one finite budget.
	ctx, cancel := context.WithTimeout(context.Background(), ScanLockBudget)
	defer cancel()
	return l.RepublishContext(ctx)
}

// commitRepublish swaps a re-attributed snapshot into the cache. It is
// commitScan minus everything that belongs to an OS scan: it does not touch the
// scan counters, `lastScanAt`, the interval or the machine's load row, so the
// RPC cache TTL and `runtime service status` read exactly what they would have if the
// write had never happened. Caller holds the ordering gate and commitMu.
func (l *Loop) commitRepublish(
	subscribers int, carry Include,
	rows []state.Port, groupRows []state.Group, sessionRows []state.SessionRecord,
) (next, prev state.Snapshot, changed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.subs = subscribers
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
		At:                    l.now().Format(time.RFC3339),
		RuntimeServiceVersion: l.opts.RuntimeServiceVersion,
		// Republishing re-attributes the last scan; it does not re-read the
		// table, so the partial/healthy verdict stays whatever that scan said.
		Observation: prev.Observation,
	})
	if !snapshotChanged(prev, next, true) {
		// A write that changed nothing a client can see publishes nothing and
		// burns no sequence, the same rule the stats tick follows.
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
	// mayCoalesce is the read path. A scan that began after this call did saw
	// everything this caller could have seen, so waiting for the one in flight
	// and then running a second is pure duplication.
	mayCoalesce scanMode = iota
	// forceScan is the write and kill path. `ports.kill` and `groups.kill`
	// resolve their selectors against the scan they ask for, and a write's
	// fallback republish has to see its own change, so these always scan —
	// never the one that happened to be in flight, and never the one that
	// finished a moment ago.
	forceScan
	// readinessScan is an active dependency wait, not a control mutation.
	// Reuse an observation at most MinScanInterval old, including one that
	// finished while waiting for rpcGate. It never falls back after failure.
	readinessScan
)

// scanNow is the uncached half of both: one scan under the RPC gate, published
// before it returns.
func (l *Loop) scanNow(include Include, mode scanMode) (state.Snapshot, error) {
	return l.scanNowContext(context.Background(), include, mode)
}

func (l *Loop) scanNowContext(ctx context.Context, include Include, mode scanMode) (state.Snapshot, error) {
	return l.scanNowWithin(ctx, include, mode, ScanLockBudget)
}

// scanNowWithin is scanNowContext with the RPC-gate budget supplied. Production
// always passes ScanLockBudget; naming it as a parameter is what keeps the
// decision the budget protects — a busy read falls back to the cache, a busy
// control request refuses — testable without holding a gate for the whole
// production budget.
func (l *Loop) scanNowWithin(ctx context.Context, include Include, mode scanMode, gateBudget time.Duration) (state.Snapshot, error) {
	ctx, cancel := observationContext(ctx)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	// Sampled before the gate: any scan that begins while this call waits takes
	// a higher number, which is what "began after I asked" means without
	// consulting a clock.
	asked := l.scanGeneration()
	if err := lockForContext(ctx, l.rpcGate, gateBudget); err != nil {
		return l.scanWhileBusy(ctx, mode, gateBudget)
	}
	defer unlock(l.rpcGate)
	// Admission is this call's linearization point: a caller who has left by
	// the time the token is in hand must not be handed somebody else's scan as
	// a fresh answer, and must not start one. A withdrawal that lands after
	// this check is not observable here — no handler promises that a
	// cancellation cannot race its return.
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if snap, reused, err := l.reuseIfFresh(asked, include, mode); err != nil || reused {
		return snap, err
	}
	return l.finishScanNow(ctx, include, mode)
}

// scanWhileBusy answers a caller that could not take the RPC gate. Cancellation
// wins over the busy fallback: the caller is gone. Only a read may then fall
// back to the cache. A control request must not resolve targets against an
// observation from before the forced scan it required.
func (l *Loop) scanWhileBusy(ctx context.Context, mode scanMode, gateBudget time.Duration) (state.Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if mode != mayCoalesce {
		return state.Snapshot{}, fmt.Errorf("scanner busy for more than %s; fresh observation unavailable", gateBudget)
	}
	l.opts.Logger.Warn("scanner busy, serving the last snapshot", "waited", gateBudget)
	return l.Cached(), nil
}

// finishScanNow publishes the scan this caller waited for. A withdrawn caller
// gets that error and not a row. A read that failed keeps the last good
// snapshot when one exists; a control request returns the failure.
func (l *Loop) finishScanNow(ctx context.Context, include Include, mode scanMode) (state.Snapshot, error) {
	next, prev, _, err := l.scanLockedContext(ctx, include, l.demandInclude(), false)
	if err := ctx.Err(); err != nil {
		return state.Snapshot{}, err
	}
	if err != nil {
		if mode == mayCoalesce && prev.Seq > 0 {
			return prev, nil
		}
		return state.Snapshot{}, err
	}
	return next, nil
}

// reuseIfFresh answers a read from state that is already good enough for its
// mode, so a burst of readers costs one scan rather than one scan each.
//
// The two modes ask different questions. A readiness wait has to know whether
// the previous readiness round failed — that failure is reusable as an error,
// never as stale success — and otherwise accepts a snapshot no older than the
// scan floor. An ordinary read accepts one that a scan begun after the call
// has already committed, which is the same thing without a clock.
func (l *Loop) reuseIfFresh(asked uint64, include Include, mode scanMode) (state.Snapshot, bool, error) {
	switch mode {
	case readinessScan:
		if err := l.recentReadinessError(); err != nil {
			return state.Snapshot{}, false, err
		}
		if snap, usable := l.cachedWithin(include, MinScanInterval); usable {
			return snap, true, nil
		}
	case mayCoalesce:
		if snap, usable := l.scannedSince(asked, include); usable {
			return snap, true, nil
		}
	}
	return state.Snapshot{}, false, nil
}

// beginScan captures both scan order and the changes known before OS I/O.
func (l *Loop) beginScan() (generation, refresh uint64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.scanSeq++
	return l.scanSeq, l.refreshRequested
}

// scanGeneration is the number of the scan that began most recently.
func (l *Loop) scanGeneration() uint64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.scanSeq
}

// scannedSince serves the cached snapshot when a scan that began strictly after
// generation gen has already committed one, and that snapshot carries whatever
// the caller asked for.
//
// Strictly after, and counted rather than timed: the scan numbered gen is the
// one that was already running (or had just finished) when the caller asked, so
// serving its result would be serving state from before the call. A clock
// cannot draw that line — `time.Now` moves in milliseconds on some platforms,
// which is how a kill's rescan came to be skipped on Windows and nowhere else.
//
// Health never coalesces either way: the probes keep their own cadence, so "a
// scan happened" says nothing about whether it probed.
func (l *Loop) scannedSince(gen uint64, include Include) (state.Snapshot, bool) {
	if include.Health {
		return state.Snapshot{}, false
	}
	return l.cachedCommittedAfter(gen, include)
}

// cachedCommittedAfter is the rest of scannedSince. It takes l.mu itself and
// reports the cached snapshot only when the scan behind it began strictly after
// gen, finished clean, nothing has invalidated it since, and include is
// covered.
func (l *Loop) cachedCommittedAfter(gen uint64, include Include) (state.Snapshot, bool) {
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

// cached is the read path's view of the cache: the snapshot, but only while it
// is younger than CacheTTL and covers what the caller asked for.
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
// A read-driven scan is published like any other, so it must not drop what a
// tick would have carried: without this an `oberth list` in the middle of a
// subscribed session would publish a snapshot with the stats stripped out, and
// every subscriber that opted into them would watch them blink away and back.
// Carrying them forward is also what keeps `docker stats` and a round of health
// probes off the critical path of every kill and every write.
func (l *Loop) demandInclude() Include {
	_, want := l.opts.Demand()
	return want
}

// snapHasStats reports whether the cached snapshot carries stats at all. It
// reads l.snap, so the caller has to hold the mutex.
func (l *Loop) snapHasStats() bool {
	for _, row := range l.snap.Ports {
		if row.Stats != nil {
			return true
		}
	}
	// An empty snapshot counts as carrying stats: there is nothing to be
	// missing, and treating it as covered stops a subscriber on an idle machine
	// from forcing a sample every tick.
	return len(l.snap.Ports) == 0
}

// Status is what `runtime.service.status` reports about the scanner.
type Status struct {
	LastScanAt time.Time

	// Scans counts completed scans; Seq counts begun ones. Comparing the two is
	// how a caller sees that a scan is in flight right now.
	Scans int64
	Seq   uint64

	// IntervalMs is the adaptive cadence at this instant — somewhere between
	// the base and whichever ceiling currently applies. BaseIntervalMs and
	// StatsIntervalMs are the effective settings behind it, after clamping;
	// both are fixed for the runtime service's life, so this is where you confirm that
	// an edit to the config file took effect.
	IntervalMs      int
	BaseIntervalMs  int
	StatsIntervalMs int

	// LastError is the last collection failure, nil when the latest scan was
	// clean.
	LastError error
}

// Status reports the scanner's current counters.
func (l *Loop) Status() Status {
	l.mu.Lock()
	defer l.mu.Unlock()
	return Status{
		Scans:           l.scans,
		Seq:             l.seq,
		LastScanAt:      l.lastScanAt,
		IntervalMs:      milliseconds(l.interval),
		BaseIntervalMs:  milliseconds(l.base),
		StatsIntervalMs: milliseconds(l.statsInterval()),
		LastError:       l.lastErr,
	}
}

// milliseconds renders a duration in whole milliseconds, the unit
// `runtime.service.status` reports intervals in.
func milliseconds(d time.Duration) int { return int(d / time.Millisecond) }
