package docker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"reflect"
	"sync"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
)

// Watcher timing. Every value here trades freshness against the cost of a
// refresh, which is a process launch plus a socket round trip to the daemon.
const (
	// DefaultRefreshInterval is the least time between two routine refreshes.
	// Refreshes are driven by scans, so this only caps how often scans can ask;
	// the daemon passes its base scan interval.
	DefaultRefreshInterval = 2 * time.Second
	// PSTimeout bounds one background `docker ps`. Nothing waits on it — it
	// only decides how soon a wedged Docker is noticed.
	PSTimeout = 5 * time.Second
	// MinBackoff is the wait after the first failed `docker ps`. Each further
	// failure doubles it up to MaxBackoff; one success resets the schedule.
	MinBackoff = 5 * time.Second
	// MaxBackoff caps the wait between attempts on a Docker that keeps failing.
	MaxBackoff = 5 * time.Minute
	// statsWindow is how long after the last Stats call container stats stay
	// worth collecting. Sampling costs a second per refresh, so it only
	// happens while a scan is actually asking.
	statsWindow = 30 * time.Second
)

// health is the last thing the watcher learnt about Docker.
type health int

const (
	healthUnknown health = iota // no attempt has finished yet
	healthOK                    // the last `docker ps` answered
	healthDown                  // installed, but the last attempt failed or timed out
	healthAbsent                // no docker on PATH
)

// WatcherOptions configures a Watcher. Every field may be left zero.
type WatcherOptions struct {
	// Interval is the least time between routine refreshes; zero means
	// DefaultRefreshInterval.
	Interval time.Duration
	// Logger receives one debug line per health change. Nil discards them.
	Logger *slog.Logger
	// OnChange runs after a refresh that changed the container list, so the
	// caller can scan again rather than wait for its next tick.
	OnChange func()
	// Now is the clock; zero means time.Now. Tests substitute a manual one.
	Now func() time.Time
}

// Watcher owns the daemon's cached view of Docker, off the scan path.
//
// Scans used to run `docker ps` inline, inside the gate that `ports.kill` and
// `state.snapshot` queue behind, so a wedged Docker Desktop that never answered
// cost every scan the whole CLITimeout. Now a scan reads whatever list is cached
// (Enrich, Stats) and pokes the watcher; the watcher refreshes in its own
// goroutine, at most once per Interval, and backs off exponentially while Docker
// fails. Container data is therefore at most one refresh stale, and never waited
// for.
type Watcher struct {
	// Refresh policy and side channels, copied from WatcherOptions.
	interval time.Duration
	timeout  time.Duration
	now      func() time.Time
	logger   *slog.Logger
	onChange func()

	// Docker access. Production installs the real CLI; a test replaces one
	// seam without touching the others.
	list     func(context.Context) ([]container, error)
	stats    func([]container) map[string]*ContainerStats
	lookPath func(string) (string, error)

	// poke has one slot: a burst of scans coalesces into one pending refresh.
	poke chan struct{}

	mu sync.Mutex
	// Cached answers. containers is replaced, never modified in place, so a
	// reader may keep the slice after dropping the mutex; statsCache is nil
	// whenever the last attempt failed.
	containers    []container
	statsCache    map[string]*ports.DockerStatsEntry
	statsWantedAt time.Time
	seen          map[int]bool

	// Refresh schedule. urgent asks for an out-of-band attempt; a backoff
	// still outranks it.
	urgent bool
	// lastAttempt and retryAt bound the routine interval and the backoff.
	lastAttempt time.Time
	retryAt     time.Time
	// failures counts consecutive failures; state is the last verdict.
	failures int
	state    health
	lastErr  error
}

// NewWatcher constructs a Watcher; it stays idle until Run.
func NewWatcher(options WatcherOptions) *Watcher {
	watcher := &Watcher{
		interval: options.Interval,
		timeout:  PSTimeout,
		logger:   options.Logger,
		onChange: options.OnChange,
		now:      options.Now,
		lookPath: exec.LookPath,
		list:     listContainersCtx,
		stats:    statsFor,
		poke:     make(chan struct{}, 1),
	}
	if watcher.interval <= 0 {
		watcher.interval = DefaultRefreshInterval
	}
	if watcher.logger == nil {
		watcher.logger = slog.New(slog.DiscardHandler)
	}
	if watcher.now == nil {
		watcher.now = time.Now
	}
	return watcher
}

// Run refreshes whenever a scan pokes and a refresh is due, until ctx ends. The
// first refresh starts immediately, so the list is primed before most first
// scans need it.
func (watcher *Watcher) Run(ctx context.Context) {
	for {
		if watcher.shouldRefresh() {
			watcher.refresh(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-watcher.poke:
		}
	}
}

// Poke asks for a refresh. It never blocks, and the refresh only happens if one
// is due.
func (watcher *Watcher) Poke() {
	select {
	case watcher.poke <- struct{}{}:
	default:
	}
}

// Enrich stamps cached container data onto rows and pokes the watcher. It never
// runs a docker command.
func (watcher *Watcher) Enrich(rows []ports.ListeningPort) {
	watcher.mu.Lock()
	cached := watcher.containers
	published := publishedPorts(cached)
	current := make(map[int]bool, len(rows))
	for i := range rows {
		port := rows[i].Port
		current[port] = true
		// A port no container is known to publish may belong to one that
		// started since the last refresh, which is worth an early look.
		if watcher.seen != nil && !watcher.seen[port] && !published[port] {
			watcher.urgent = true
		}
	}
	watcher.seen = current
	watcher.mu.Unlock()

	enrichFrom(rows, cached)
	watcher.Poke()
}

// Stats returns the container stats cached by the last refresh, keyed by
// container name, and keeps the watcher sampling them for statsWindow. It never
// runs a docker command; nil means nothing is cached yet.
func (watcher *Watcher) Stats() map[string]*ports.DockerStatsEntry {
	watcher.mu.Lock()
	requestedAt := watcher.now()
	// A request outside the window restarts it, and that needs a fresh sample
	// rather than whatever happens to be cached.
	if watcher.statsWantedAt.IsZero() || requestedAt.Sub(watcher.statsWantedAt) >= statsWindow {
		watcher.urgent = true
	}
	watcher.statsWantedAt = requestedAt
	cached := watcher.statsCache
	watcher.mu.Unlock()
	watcher.Poke()
	return cached
}

// Healthy reports whether the last `docker ps` answered. Callers use it to skip
// optional docker calls, such as the container graph, while Docker is silent.
func (watcher *Watcher) Healthy() bool {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	return watcher.state == healthOK
}

// shouldRefresh reports whether a refresh belongs now: a backoff that has not
// expired outranks both urgency and the interval.
func (watcher *Watcher) shouldRefresh() bool {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	now := watcher.now()
	if now.Before(watcher.retryAt) {
		return false
	}
	if watcher.urgent || watcher.lastAttempt.IsZero() {
		return true
	}
	return now.Sub(watcher.lastAttempt) >= watcher.interval
}

// refresh runs one `docker ps` — plus the stats, when someone wants them — and
// records the outcome. Only Run calls it, so two never overlap.
func (watcher *Watcher) refresh(ctx context.Context) {
	wantStats := watcher.beginAttempt()

	// Not installed is not a failure: no exec, no backoff, no noise. LookPath
	// is a few stat calls, cheap enough to repeat, so a docker installed later
	// is picked up on the next refresh.
	if _, err := watcher.lookPath("docker"); err != nil {
		watcher.recordAbsent()
		return
	}

	running, err, timedOut := watcher.callList(ctx)
	if ctx.Err() != nil {
		return // shutting down; this attempt says nothing about Docker
	}
	if err != nil {
		if timedOut {
			err = fmt.Errorf("`docker ps` did not answer within %s", watcher.timeout)
		}
		watcher.recordFailure(err)
		return
	}

	var sampled map[string]*ports.DockerStatsEntry
	if wantStats {
		sampled = statsEntries(watcher.stats(running))
	}
	watcher.recordSuccess(running, sampled)
}

// beginAttempt marks an attempt as started and answers whether stats were wanted
// for it. Clearing urgency here means a request arriving during the refresh asks
// for another one.
func (watcher *Watcher) beginAttempt() bool {
	watcher.mu.Lock()
	defer watcher.mu.Unlock()
	watcher.urgent = false
	now := watcher.now()
	watcher.lastAttempt = now
	return !watcher.statsWantedAt.IsZero() && now.Sub(watcher.statsWantedAt) < statsWindow
}

// callList runs `docker ps` under the probe timeout and reports whether the
// timeout is what ended it.
func (watcher *Watcher) callList(ctx context.Context) ([]container, error, bool) {
	probeCtx, cancel := context.WithTimeout(ctx, watcher.timeout)
	defer cancel()
	running, err := watcher.list(probeCtx)
	return running, err, errors.Is(probeCtx.Err(), context.DeadlineExceeded)
}

// recordFailure keeps the last good list, stops serving stats and waits out the
// next backoff step.
func (watcher *Watcher) recordFailure(err error) {
	watcher.mu.Lock()
	watcher.failures++
	wait := backoffFor(watcher.failures)
	watcher.retryAt = watcher.now().Add(wait)
	was := watcher.state
	watcher.state = healthDown
	watcher.lastErr = err
	watcher.statsCache = nil
	watcher.mu.Unlock()

	// One line per transition, not per attempt: a Docker that stays down for an
	// hour would otherwise fill the log.
	if was != healthDown {
		watcher.logger.Debug("docker is not answering; scans keep the last container list",
			"error", err, "retry_in", wait)
	}
}

// recordSuccess stores the new list and resets the backoff.
func (watcher *Watcher) recordSuccess(running []container, sampled map[string]*ports.DockerStatsEntry) {
	watcher.mu.Lock()
	was, failedAttempts := watcher.state, watcher.failures
	watcher.state = healthOK
	watcher.failures = 0
	watcher.retryAt = time.Time{}
	watcher.lastErr = nil
	changed := !reflect.DeepEqual(watcher.containers, running)
	watcher.containers = running
	watcher.statsCache = sampled
	watcher.mu.Unlock()

	if was == healthDown {
		watcher.logger.Debug("docker is answering again", "failed_attempts", failedAttempts)
	}
	if changed {
		watcher.announce()
	}
}

// recordAbsent drops the cached list: with no docker on PATH there is nothing
// left to attribute ports to.
func (watcher *Watcher) recordAbsent() {
	watcher.mu.Lock()
	was := watcher.state
	hadContainers := len(watcher.containers) > 0
	watcher.state = healthAbsent
	watcher.failures = 0
	watcher.retryAt = time.Time{}
	watcher.lastErr = nil
	watcher.containers = nil
	watcher.statsCache = nil
	watcher.mu.Unlock()

	if was != healthAbsent {
		watcher.logger.Debug("docker CLI not found; container enrichment is off")
	}
	if hadContainers {
		watcher.announce()
	}
}

// announce tells the caller the container list changed, so it can scan again
// instead of waiting for its next tick.
func (watcher *Watcher) announce() {
	if watcher.onChange != nil {
		watcher.onChange()
	}
}

// backoffFor is the wait after the n-th consecutive failure: MinBackoff, doubled
// once per further failure, capped at MaxBackoff.
func backoffFor(failures int) time.Duration {
	wait := MinBackoff
	for attempt := 1; attempt < failures; attempt++ {
		if wait >= MaxBackoff/2 {
			return MaxBackoff
		}
		wait *= 2
	}
	return wait
}
