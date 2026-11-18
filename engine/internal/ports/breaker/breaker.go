// Package breaker holds a circuit breaker for one attempt that keeps failing:
// the runtime service's read of the platform's primary listener collector.
//
// The problem it solves is a machine where the primary collector never works.
// macOS 27 denies the TCP table to a child of an unsigned parent, so every scan
// pays a failed `netstat` exec before the fallback answers — at the 2 s default
// cadence that is tens of thousands of failed execs a day, each one buying
// nothing. After a few failures in a row the breaker stops running that attempt
// for a cooldown, and then lets exactly one real probe through: success closes
// it, failure opens it again with the cooldown restarted.
//
// It is a policy, never a fact. A skipped attempt is not evidence about the
// machine, so the caller publishes exactly what the alternatives produce and
// says out loud that the primary did not run; a cancelled probe learned nothing
// and closes nothing.
//
// The state machine holds no goroutine and never reads the clock directly: the
// caller injects now, so every transition is decided by its own time and is
// testable without waiting.
package breaker

import (
	"sync"
	"time"
)

// The numbers a breaker configured with nothing uses. Both are conservative:
// three failures tell a broken tool from one bad pass without a long blind
// spell, and one minute keeps a 2 s scan from paying for the failure on every
// tick while leaving a recovered tool at most a minute away.
const (
	DefaultFailures = 3
	DefaultCooldown = 60 * time.Second
)

// Config is one breaker's numbers. The zero value means the defaults.
type Config struct {
	// Failures is how many failures in a row open the breaker.
	Failures int
	// Cooldown is how long an open breaker skips the attempt before one probe.
	Cooldown time.Duration
}

// resolved fills in every zero field, so nothing downstream has to ask whether a
// number was configured.
func (c Config) resolved() Config {
	if c.Failures <= 0 {
		c.Failures = DefaultFailures
	}
	if c.Cooldown <= 0 {
		c.Cooldown = DefaultCooldown
	}
	return c
}

// Skip is why one attempt did not run. It is what the caller publishes, so it
// carries everything a reader needs to tell "this tool is broken" from "this
// tool has not been tried since it broke": how many failures in a row, when the
// current open window began, and the failure that opened it.
type Skip struct {
	// Collector is the guarded attempt's own name, filled in by the caller that
	// knows it. The breaker only ever guards one thing and does not name it.
	Collector string
	// Failures is how many failures in a row the breaker has seen. While the
	// breaker is open it is at least Config.Failures.
	Failures int
	// Since is when the current open window began: the last time the attempt ran
	// and failed. Each failed or abandoned probe starts a new window, so this is
	// also the most recent evidence against the attempt.
	Since time.Time
	// Cooldown is how long the current window lasts. The next real attempt
	// happens when it ends.
	Cooldown time.Duration
	// Last is the failure that opened the breaker, or the most recent one if a
	// later probe failed too. It is set whenever the breaker is open, because the
	// only way to open is to have seen a failure.
	Last error
}

// state is the three positions of the machine.
type state uint8

const (
	// closed: the attempt runs on every read.
	closed state = iota
	// open: the attempt does not run until the cooldown ends.
	open
	// halfOpen: one probe is in flight, and nothing else may run until it
	// reports.
	halfOpen
)

// Breaker is the state machine.
//
// The mutex is the one piece of machinery inside it, and it is there because two
// scans can be in flight at once — the loop's tick and an RPC's forced scan hold
// different gates — while "exactly one probe" is a promise about both of them.
// The clock is not machinery: every read of it goes through the injected now.
type Breaker struct {
	cfg Config
	now func() time.Time

	mu       sync.Mutex
	state    state
	failures int
	openedAt time.Time
	last     error
}

// New returns a closed breaker. A nil now means time.Now; tests inject their own
// so no transition has to be waited for.
func New(cfg Config, now func() time.Time) *Breaker {
	if now == nil {
		now = time.Now
	}
	return &Breaker{cfg: cfg.resolved(), now: now}
}

// Admit decides whether the guarded attempt may run in this read.
//
// It answers, and it moves the state the answer depends on: while the breaker is
// open nothing may run, and the first call after the cooldown is the one probe,
// so every other caller stays refused until that probe reports. A caller that is
// admitted must report exactly one outcome — Succeeded, Failed or Abandoned —
// before the next read, or the probe slot stays taken.
func (b *Breaker) Admit() (attempt bool, skip Skip) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch b.state {
	case closed:
		return true, Skip{}
	case open:
		if b.now().Sub(b.openedAt) < b.cfg.Cooldown {
			return false, b.skip()
		}
		// The cooldown is over: this caller is the probe, and it is the only one
		// until it reports.
		b.state = halfOpen
		return true, Skip{}
	default:
		// halfOpen: somebody else is already probing.
		return false, b.skip()
	}
}

// Succeeded records that an admitted attempt produced the answer this read
// publishes. A readable row ends the round — even a partial pass or a decoder's
// own verdict, because the tool ran and answered — so it closes the breaker and
// forgets the failures behind it.
func (b *Breaker) Succeeded() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.state = closed
	b.failures = 0
	b.openedAt = time.Time{}
	b.last = nil
}

// Failed records that an admitted attempt left nothing usable, so the caller
// read an alternative instead. Reaching the configured count opens the breaker;
// a failed probe opens it again with the cooldown restarted, so a tool that
// stays broken is tried once per window rather than on every scan.
func (b *Breaker) Failed(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failures++
	if err != nil {
		b.last = err
	}
	if b.state == halfOpen || b.failures >= b.cfg.Failures {
		b.state = open
		b.openedAt = b.now()
	}
}

// Abandoned records that an admitted attempt was withdrawn: the caller gave up,
// or the machine wedged the command. Nothing was learned, so nothing is counted
// and nothing closes — a probe that ends this way only hands its slot back, and
// the next real attempt waits another cooldown rather than repeating a command
// nobody is waiting for.
func (b *Breaker) Abandoned() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state != halfOpen {
		return
	}
	b.state = open
	b.openedAt = b.now()
}

// skip describes the open window. Caller holds the mutex.
func (b *Breaker) skip() Skip {
	return Skip{
		Failures: b.failures,
		Since:    b.openedAt,
		Cooldown: b.cfg.Cooldown,
		Last:     b.last,
	}
}
