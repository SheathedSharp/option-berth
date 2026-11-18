package breaker

import (
	"errors"
	"sync"
	"testing"
	"time"
)

// clock is the injected time. Every transition below is decided by moving it,
// so no test waits.
type clock struct{ at time.Time }

func newClock() *clock {
	return &clock{at: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
}

func (c *clock) now() time.Time          { return c.at }
func (c *clock) advance(d time.Duration) { c.at = c.at.Add(d) }

// failing is the reason a test attempt was refused: the breaker only remembers
// it, so any error will do.
var failing = errors.New("netstat: exited 0 but printed no listening table")

// attempt runs one read's worth of the protocol: it asks, and when it is
// admitted it records outcome.
func attempt(b *Breaker, outcome func()) (bool, Skip) {
	allowed, skip := b.Admit()
	if !allowed {
		return false, skip
	}
	if outcome != nil {
		outcome()
	}
	return true, Skip{}
}

func failed(b *Breaker) func() { return func() { b.Failed(failing) } }

// A working attempt is tried every time: the breaker is not a rate limiter, and
// nothing about a healthy read may change.
func TestClosedBreakerAdmitsEveryRead(t *testing.T) {
	c := newClock()
	b := New(Config{}, c.now)
	for i := range 5 {
		if allowed, _ := attempt(b, func() { b.Succeeded() }); !allowed {
			t.Fatalf("read %d was refused although every read succeeded", i)
		}
	}
}

// The failure count is what opens the breaker, and the sentence it publishes
// carries the numbers a reader needs: how many, since when, and the failure.
func TestBreakerOpensAfterTheConfiguredFailures(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 3, Cooldown: 45 * time.Second}, c.now)

	attempt(b, failed(b))
	attempt(b, failed(b))
	if allowed, _ := attempt(b, failed(b)); !allowed {
		t.Fatal("the third failure was refused instead of being run")
	}
	openedAt := c.now()

	c.advance(44 * time.Second)
	allowed, skip := attempt(b, failed(b))
	if allowed {
		t.Fatal("the fourth read ran the attempt although three failures in a row opened the breaker")
	}
	if skip.Failures != 3 || skip.Cooldown != 45*time.Second || !skip.Since.Equal(openedAt) {
		t.Fatalf("skip = %+v, want three failures since %s with the configured cooldown", skip, openedAt)
	}
	if !errors.Is(skip.Last, failing) {
		t.Fatalf("skip.Last = %v, want the failure that opened the breaker", skip.Last)
	}
}

// Only failures in a row count: one success in between is the tool working, and
// the count starts over.
func TestSuccessRestartsTheFailureCount(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 3}, c.now)
	attempt(b, failed(b))
	attempt(b, failed(b))
	attempt(b, func() { b.Succeeded() })
	attempt(b, failed(b))
	attempt(b, failed(b))
	if allowed, _ := attempt(b, failed(b)); !allowed {
		t.Fatal("two failures after a success were treated as a fourth in a row")
	}
}

// The cooldown is the whole point: while it runs nothing is tried, and when it
// ends exactly one probe goes through.
func TestCooldownEndsInExactlyOneProbe(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 1, Cooldown: 60 * time.Second}, c.now)
	attempt(b, failed(b))

	c.advance(59 * time.Second)
	if allowed, _ := attempt(b, failed(b)); allowed {
		t.Fatal("the attempt ran inside the cooldown")
	}
	c.advance(time.Second)
	allowed, _ := b.Admit()
	if !allowed {
		t.Fatal("the probe was refused when the cooldown ended")
	}
	// The probe is in flight: a second scan arriving now must not run a second
	// command.
	if second, skip := b.Admit(); second {
		t.Fatalf("a second caller probed too; skip = %+v", skip)
	}
}

// A probe that answers closes the breaker, and the next read runs the attempt as
// it did before.
func TestProbeSuccessClosesTheBreaker(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 2, Cooldown: time.Minute}, c.now)
	attempt(b, failed(b))
	attempt(b, failed(b))
	c.advance(time.Minute)

	if allowed, _ := attempt(b, func() { b.Succeeded() }); !allowed {
		t.Fatal("the probe was refused when the cooldown ended")
	}
	if allowed, _ := attempt(b, func() { b.Succeeded() }); !allowed {
		t.Fatal("the breaker did not close after its probe succeeded")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.failures != 0 || !b.openedAt.IsZero() {
		t.Fatalf("state after closing = %d failures since %s, want a clean closed breaker", b.failures, b.openedAt)
	}
}

// A probe that fails again reopens the breaker with the cooldown restarted, so a
// tool that stays broken is tried once per window, never once per scan.
func TestProbeFailureRestartsTheCooldown(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 1, Cooldown: time.Minute}, c.now)
	attempt(b, failed(b))
	firstWindow := c.now()

	c.advance(time.Minute)
	attempt(b, func() { b.Failed(failing) })
	if c.now().Equal(firstWindow) {
		t.Fatal("the test did not move the clock")
	}
	allowed, skip := attempt(b, failed(b))
	if allowed {
		t.Fatal("a failed probe did not reopen the breaker")
	}
	if !skip.Since.Equal(c.now()) || skip.Failures != 2 {
		t.Fatalf("skip = %+v, want the window restarted at %s with two failures", skip, c.now())
	}
	c.advance(59 * time.Second)
	if allowed, _ := attempt(b, failed(b)); allowed {
		t.Fatal("the restarted cooldown was not honoured")
	}
	c.advance(time.Second)
	if allowed, _ := b.Admit(); !allowed {
		t.Fatal("no probe after the restarted cooldown")
	}
}

// A withdrawn probe is not evidence either way. It must not close the breaker,
// and it must not turn into a probe on every following read.
func TestAbandonedProbeDoesNotClose(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 2, Cooldown: time.Minute}, c.now)
	attempt(b, failed(b))
	attempt(b, failed(b))
	c.advance(time.Minute)

	if allowed, _ := attempt(b, func() { b.Abandoned() }); !allowed {
		t.Fatal("the probe was refused when the cooldown ended")
	}
	allowed, skip := attempt(b, func() { b.Failed(failing) })
	if allowed {
		t.Fatal("an abandoned probe closed the breaker")
	}
	if skip.Failures != 2 || !skip.Since.Equal(c.now()) {
		t.Fatalf("skip = %+v, want the failures unchanged and the window restarted", skip)
	}
}

// A withdrawn attempt while the breaker is closed is not a failure: a caller who
// gives up says nothing about the tool.
func TestAbandonedAttemptWhileClosedCountsNothing(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 2}, c.now)
	for i := range 5 {
		if allowed, _ := attempt(b, func() { b.Abandoned() }); !allowed {
			t.Fatalf("read %d was refused although no attempt ever failed", i)
		}
	}
}

// The zero value means the documented defaults, so a caller that configures
// nothing still gets the conservative numbers rather than an open or hair-trigger
// breaker.
func TestZeroConfigUsesTheDocumentedDefaults(t *testing.T) {
	c := newClock()
	b := New(Config{}, c.now)
	attempt(b, failed(b))
	attempt(b, failed(b))
	if allowed, _ := attempt(b, failed(b)); !allowed {
		t.Fatalf("the third failure was refused; the default threshold is %d", DefaultFailures)
	}
	allowed, skip := attempt(b, failed(b))
	if allowed {
		t.Fatal("the breaker did not open at the default threshold")
	}
	if skip.Cooldown != DefaultCooldown {
		t.Fatalf("cooldown = %s, want the default %s", skip.Cooldown, DefaultCooldown)
	}
	c.advance(DefaultCooldown - time.Nanosecond)
	if allowed, _ := attempt(b, failed(b)); allowed {
		t.Fatal("the default cooldown was not honoured")
	}
	c.advance(time.Nanosecond)
	if allowed, _ := b.Admit(); !allowed {
		t.Fatal("no probe after the default cooldown")
	}
}

// Two scans can be in flight at once, and "exactly one probe" is a promise about
// both of them: a cooldown ending under concurrent reads still produces one
// attempt, not one per caller.
func TestConcurrentReadsProduceOneProbe(t *testing.T) {
	c := newClock()
	b := New(Config{Failures: 1, Cooldown: time.Minute}, c.now)
	attempt(b, failed(b))
	c.advance(time.Minute)

	var wg sync.WaitGroup
	var mu sync.Mutex
	admitted := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if allowed, _ := b.Admit(); allowed {
				mu.Lock()
				admitted++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if admitted != 1 {
		t.Fatalf("admitted = %d probes, want exactly one", admitted)
	}
}
