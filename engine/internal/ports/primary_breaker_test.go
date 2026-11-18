package ports

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports/breaker"
)

// lsofTable is one listener as the Darwin alternative prints it. The primary's
// table is empty in these tests, which is the macOS 27 shape: netstat exits 0
// and prints nothing.
const lsofTable = "p900\ncrelayd\nf3\ntIPv6\nn*:18123\n"

// breakerClock is the injected time behind the breaker under test.
type breakerClock struct{ at time.Time }

func newBreakerClock() *breakerClock {
	return &breakerClock{at: time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)}
}

func (c *breakerClock) now() time.Time          { return c.at }
func (c *breakerClock) advance(d time.Duration) { c.at = c.at.Add(d) }

// openPrimaryBreaker opens a breaker the way the scan path does — by running the
// primary and reporting what it produced — so the tests below exercise the same
// transitions production does rather than a hand-built state.
func openPrimaryBreaker(t *testing.T, c *breakerClock, rungs []Collector, run listenerCommand) *breaker.Breaker {
	t.Helper()
	gate := breaker.New(breaker.Config{Failures: 3, Cooldown: time.Minute}, c.now)
	for range 3 {
		if _, err, skip := attemptPrimary(rungs, run, gate); skip != nil || err == nil {
			t.Fatalf("attempt=%+v skip=%+v, want the primary attempted and failing", err, skip)
		}
	}
	return gate
}

// darwinSkipped is the fixture every test below reads: a primary that never
// prints a table and an alternative that answers.
func darwinSkipped() *fakeCollectors {
	return &fakeCollectors{answers: map[string]collectorAnswer{
		"netstat": {},
		"lsof":    {out: lsofTable},
	}}
}

// A held-open breaker is a decision about this process, not about the machine:
// the alternative is read exactly as it would have been, one failed exec
// cheaper, and the primary is named as the reason the table is missing its rows.
func TestOpenBreakerSkipsThePrimaryCommand(t *testing.T) {
	c := newBreakerClock()
	open := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), open.run)
	open.tried = nil

	read, err := readChain("darwin", open.run, gate)
	if err != nil {
		t.Fatalf("lsof answered but the read failed: %v", err)
	}
	if want := []string{"lsof"}; !reflect.DeepEqual(open.tried, want) {
		t.Fatalf("collectors tried = %v, want %v — the primary was not skipped", open.tried, want)
	}
	if read.Source != listenerSourceLsofName || len(read.Rows) != 1 || read.Rows[0].Port != 18123 {
		t.Fatalf("read = %+v, want lsof's table", read)
	}
	if read.Skip == nil {
		t.Fatal("the read did not report that the primary was skipped")
	}
	if read.Skip.Collector != "netstat" || read.Skip.Failures != 3 || read.Skip.Cooldown != time.Minute {
		t.Fatalf("skip = %+v, want the primary named with its failures and cooldown", *read.Skip)
	}
	if !read.Skip.Since.Equal(c.now()) {
		t.Fatalf("skip since %s, want the window that opened at %s", read.Skip.Since, c.now())
	}

	// The same machine read without a breaker must produce the same table: a
	// skip is cheaper, never different.
	unskipped := darwinSkipped()
	want, wantErr := readChain("darwin", unskipped.run, nil)
	if wantErr != nil {
		t.Fatalf("the unskipped read failed: %v", wantErr)
	}
	if want.Skip != nil || !reflect.DeepEqual(read.Rows, want.Rows) || read.Source != want.Source {
		t.Fatalf("skipped read = %+v, unskipped = %+v, want the same table", read, want)
	}
	if got, wantDetail := read.Detail(), want.Detail(); !strings.HasPrefix(got, wantDetail) {
		t.Fatalf("detail = %q, want the coverage sentence %q first", got, wantDetail)
	}
}

// The published sentence has to say both things a reader needs: which collector
// answered, and that the primary's own rows are missing because it did not run.
func TestSkippedPrimaryDetailNamesTheSkipAndItsNumbers(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)

	read, err := readChain("darwin", fake.run, gate)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The coverage sentence is whatever this host publishes for a named
	// alternative; the skip clause is appended to it, never substituted for it.
	coverage := narrowSourceDetail(runtime.GOOS, listenerSourceLsofName)
	clause := read.SkipClause()
	detail := read.Detail()
	if clause == "" || !strings.HasPrefix(detail, coverage) || !strings.HasSuffix(detail, clause) {
		t.Fatalf("detail = %q, want the coverage sentence %q followed by the skip clause %q",
			detail, coverage, clause)
	}
	for _, want := range []string{
		"the primary collector netstat was not run",
		"3 failures in a row since 2026-03-04T05:06:07Z",
		"retried every 1m0s",
	} {
		if !strings.Contains(clause, want) {
			t.Fatalf("skip clause = %q, want it to contain %q", clause, want)
		}
	}
	if read.SkipClause() == "" || !strings.Contains(detail, read.SkipClause()) {
		t.Fatalf("detail = %q, want it to carry the skip clause %q", detail, read.SkipClause())
	}
}

// When nothing answers, the read is still the primary's failure: a sandbox that
// denied the table keeps its code, and the sentence says this round did not
// retry it instead of pretending it just failed.
func TestSkippedPrimaryKeepsItsCodeWhenNoAlternativeAnswers(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)
	fake.answers["lsof"] = collectorAnswer{stderr: "lsof: permission denied\n", err: collectorExit(1)}
	fake.tried = nil

	read, err := readChain("darwin", fake.run, gate)
	var observed *ObservationError
	if !errors.As(err, &observed) || observed.Code != CodeObservationEmpty {
		t.Fatalf("error = %+v, want the primary's %q", err, CodeObservationEmpty)
	}
	for _, want := range []string{
		"netstat: not run",
		"retried every 1m0s",
		"last failure: netstat: exited 0 but printed no listening table",
		"fallback:",
		"lsof: incomplete listener observation",
		"permission denied",
	} {
		if !strings.Contains(observed.Error(), want) {
			t.Fatalf("detail = %q, want it to contain %q", observed.Error(), want)
		}
	}
	if len(read.Rows) != 0 || read.Source != "" || read.Skip == nil {
		t.Fatalf("read = %+v, want nothing published and the skip reported", read)
	}
	if want := []string{"lsof"}; !reflect.DeepEqual(fake.tried, want) {
		t.Fatalf("collectors tried = %v, want only the alternative", fake.tried)
	}
}

// The probe has to be a real read. A cooldown ending with a primary that works
// runs the command again, closes the breaker, and publishes the primary's own
// table under no source.
func TestHalfOpenProbeIsARealRead(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)

	c.advance(time.Minute)
	fake.answers["netstat"] = collectorAnswer{out: darwinTCPHeader +
		"tcp4 0 0 127.0.0.1.18120 *.* LISTEN 0 0 128 128 node:42 0 0\n"}
	fake.tried = nil

	read, err := readChain("darwin", fake.run, gate)
	if err != nil {
		t.Fatalf("probe read: %v", err)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(fake.tried, want) {
		t.Fatalf("collectors tried = %v, want the probe to run the real command", fake.tried)
	}
	if read.Source != "" || read.Skip != nil || len(read.Rows) != 1 || read.Rows[0].Port != 18120 {
		t.Fatalf("read = %+v, want the primary's own table", read)
	}

	fake.tried = nil
	if _, err := readChain("darwin", fake.run, gate); err != nil {
		t.Fatalf("read after the probe: %v", err)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(fake.tried, want) {
		t.Fatalf("collectors tried = %v, want the closed breaker to read the primary", fake.tried)
	}
}

// A withdrawn probe learned nothing about the machine, so it must not close the
// breaker: the next read still skips the primary rather than paying for a
// command nobody is waiting for.
func TestCancelledProbeDoesNotCloseTheBreaker(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)

	c.advance(time.Minute)
	fake.answers["netstat"] = collectorAnswer{err: context.Canceled}
	if _, err := readChain("darwin", fake.run, gate); !errors.Is(err, context.Canceled) {
		t.Fatalf("probe read error = %v, want the cancellation itself", err)
	}

	fake.tried = nil
	if _, err := readChain("darwin", fake.run, gate); err != nil {
		t.Fatalf("read after the cancelled probe: %v", err)
	}
	if want := []string{"lsof"}; !reflect.DeepEqual(fake.tried, want) {
		t.Fatalf("collectors tried = %v, want the cancelled probe to leave the breaker open", fake.tried)
	}
}

// A chain with one rung has no alternative to read instead, so the breaker never
// holds it back: the only collector runs however open the breaker is.
func TestSingleRungChainIsNeverSkipped(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)

	single := &fakeCollectors{answers: map[string]collectorAnswer{
		"netstat": {out: "Proto  Local Address          Foreign Address        State           PID\n" +
			"TCP    0.0.0.0:18120          0.0.0.0:0              LISTENING       4242\n"},
	}}
	read, err := readChain("windows", single.run, gate)
	if err != nil {
		t.Fatalf("single-rung read: %v", err)
	}
	if want := []string{"netstat"}; !reflect.DeepEqual(single.tried, want) {
		t.Fatalf("collectors tried = %v, want the only rung to run", single.tried)
	}
	if read.Skip != nil || read.Source != "" || len(read.Rows) != 1 {
		t.Fatalf("read = %+v, want the primary's own table", read)
	}
}

// A merged read honours the same decision: the held rung is left out of the
// union, its name is missing from sources, and the skip says why — never a row
// invented to fill the gap.
func TestMergedReadLeavesTheHeldPrimaryOutOfTheUnion(t *testing.T) {
	c := newBreakerClock()
	fake := darwinSkipped()
	gate := openPrimaryBreaker(t, c, chain("darwin"), fake.run)
	fake.tried = nil

	read, err := readChainSources("darwin", fake.run, gate, 9)
	if err != nil {
		t.Fatalf("merged read: %v", err)
	}
	if want := []string{"lsof"}; !reflect.DeepEqual(fake.tried, want) {
		t.Fatalf("collectors tried = %v, want the primary skipped", fake.tried)
	}
	if want := []string{"lsof"}; !reflect.DeepEqual(read.Sources, want) {
		t.Fatalf("sources = %v, want %v", read.Sources, want)
	}
	if read.Skip == nil || len(read.Rows) != 1 || read.Rows[0].Port != 18123 {
		t.Fatalf("read = %+v, want lsof's row and the skip reported", read)
	}
	if !strings.Contains(read.Detail(), "the primary collector netstat was not run") {
		t.Fatalf("detail = %q, want the skip named in the merged sentence", read.Detail())
	}

	unskipped := darwinSkipped()
	want, wantErr := readChainSources("darwin", unskipped.run, nil, 9)
	if wantErr != nil {
		t.Fatalf("the unskipped merged read failed: %v", wantErr)
	}
	if !reflect.DeepEqual(read.Rows, want.Rows) || !reflect.DeepEqual(read.Sources, want.Sources) {
		t.Fatalf("skipped merge = %+v, unskipped = %+v, want the same union", read, want)
	}
}
