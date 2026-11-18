package scanner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/ports/breaker"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// openLoopBreaker drives a loop's own breaker to open the way the scan path
// does: by reporting the failures the primary collector produced.
func openLoopBreaker(t *testing.T, l *Loop, failures int) {
	t.Helper()
	if l.primary == nil {
		t.Fatal("the loop has no primary breaker")
	}
	for range failures {
		attempt, skip := l.primary.Admit()
		if !attempt {
			t.Fatalf("attempt refused with %+v before the breaker opened", skip)
		}
		l.primary.Failed(errors.New("netstat: exited 0 but printed no listening table"))
	}
	if attempt, _ := l.primary.Admit(); attempt {
		t.Fatal("the breaker stayed closed after the configured failures")
	}
}

// A read that skipped the primary publishes the same verdict it did before: the
// table is still observable through the alternative, absence is still unknown,
// and the degradation clock does not move. The only thing that changes is the
// sentence, which now says why the primary's own rows are missing.
func TestSkippedPrimaryPublishesTheSameObservation(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	rows := []ports.ListeningPort{{Port: 8123, PID: 42, BindAddress: "127.0.0.1"}}
	skip := false
	l := New(Options{
		RuntimeServiceVersion: "test",
		Now:                   func() time.Time { return now },
		ScanSource: func(context.Context, Include) (ports.Read, error) {
			read := ports.Read{Rows: rows, Source: "lsof"}
			if skip {
				read.Skip = &breaker.Skip{
					Collector: "netstat", Failures: 3, Since: now, Cooldown: time.Minute,
					Last: errors.New("netstat: exited 0 but printed no listening table"),
				}
			}
			return read, nil
		},
	})

	failing, _, _, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || failing.Observation == nil {
		t.Fatalf("first scan = %+v, %v", failing.Observation, err)
	}
	if failing.Observation.Source != "lsof" || failing.Observation.DegradedSince == "" {
		t.Fatalf("observation = %+v, want the narrow verdict and its start", failing.Observation)
	}

	now = now.Add(30 * time.Second)
	skip = true
	skipping, _, changed, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil {
		t.Fatalf("skipping scan: %v", err)
	}
	got := skipping.Observation
	if got == nil || got.State != state.ObservationObservable || got.Source != "lsof" || len(got.Sources) != 0 {
		t.Fatalf("observation = %+v, want the same narrow verdict", got)
	}
	if got.DegradedSince != failing.Observation.DegradedSince || got.RecoveredAt != "" {
		t.Fatalf("observation = %+v, want the degradation clock untouched", got)
	}
	if !changed {
		t.Fatal("the skip sentence did not reach the wire")
	}
	for _, want := range []string{
		"read with lsof",
		"a port missing from it is unknown, not free",
		"the primary collector netstat was not run",
		"3 failures in a row",
	} {
		if !strings.Contains(got.Detail, want) {
			t.Fatalf("detail = %q, want it to contain %q", got.Detail, want)
		}
	}
	if len(skipping.Ports) != 1 || skipping.Ports[0].Port != 8123 {
		t.Fatalf("ports = %+v, want the alternative's row", skipping.Ports)
	}
}

// A partial table read while the primary was held back keeps the collector's own
// verdict first and says the primary did not run after it: both facts decide what
// a missing port means.
func TestSkippedPrimaryUnderAPartialReadKeepsBothVerdicts(t *testing.T) {
	since := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	partial := &ports.ObservationError{
		Code:    ports.CodeObservationIncomplete,
		Detail:  "netstat: unreadable row",
		Hint:    "run `oberth doctor --only listener_observation`",
		Partial: true,
		Rows:    []ports.ListeningPort{{Port: 18120, PID: 42, BindAddress: "127.0.0.1"}},
	}
	l := New(Options{
		RuntimeServiceVersion: "test",
		SourceLimit:           2,
		ScanSources: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{
				Rows:    partial.Rows,
				Sources: []string{"ss", "netstat"},
				Skip: &breaker.Skip{
					Collector: "ss", Failures: 4, Since: since, Cooldown: time.Minute,
				},
			}, partial
		},
	})

	read, _, _, err := l.scanLocked(Include{}, Include{}, false)
	if err != nil || read.Observation == nil {
		t.Fatalf("scan = %+v, %v", read.Observation, err)
	}
	got := read.Observation
	if got.State != state.ObservationPartial || got.Code != ports.CodeObservationIncomplete {
		t.Fatalf("observation = %+v, want the collector's own partial verdict", got)
	}
	if !strings.HasPrefix(got.Detail, "netstat: unreadable row") {
		t.Fatalf("detail = %q, want the collector's evidence first", got.Detail)
	}
	if !strings.Contains(got.Detail, "the primary collector ss was not run") {
		t.Fatalf("detail = %q, want the skip named after it", got.Detail)
	}
}

// The configured numbers are the numbers the loop runs with, including the
// decision to turn the breaker off: nothing about this is inferred at scan time.
func TestPrimaryBreakerIsBuiltFromTheConfiguredNumbers(t *testing.T) {
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	l := New(Options{Now: func() time.Time { return now }, PrimaryFailures: 2, PrimaryCooldown: 45 * time.Second})
	openLoopBreaker(t, l, 2)
	attempt, skip := l.primary.Admit()
	if attempt || skip.Failures != 2 || skip.Cooldown != 45*time.Second {
		t.Fatalf("skip = %+v, want the configured two failures and 45s cooldown", skip)
	}

	if off := New(Options{}); off.primary != nil {
		t.Fatal("a loop configured with no failures still gates its primary")
	}
}

// The breaker is per-process state. Nothing about it is written to the store, so
// a runtime service that restarts tries the primary again instead of inheriting a
// decision made by a process that is gone.
func TestPrimaryBreakerIsNotPersisted(t *testing.T) {
	st := openStore(t)
	rows := []ports.ListeningPort{{Port: 8123, PID: 42, BindAddress: "127.0.0.1"}}
	opts := Options{
		RuntimeServiceVersion: "test",
		Store:                 st,
		PrimaryFailures:       3,
		ScanSource: func(context.Context, Include) (ports.Read, error) {
			return ports.Read{Rows: rows, Source: "lsof"}, nil
		},
	}
	running := New(opts)
	openLoopBreaker(t, running, 3)

	restarted := New(opts)
	if restarted.primary == nil {
		t.Fatal("the restarted loop has no breaker")
	}
	if attempt, skip := restarted.primary.Admit(); !attempt {
		t.Fatalf("a new loop inherited an open breaker: %+v", skip)
	}
	first, _, _, err := restarted.scanLocked(Include{}, Include{}, false)
	if err != nil {
		t.Fatalf("scan after restart: %v", err)
	}
	if first.Observation == nil || strings.Contains(first.Observation.Detail, "was not run") {
		t.Fatalf("observation = %+v, want no inherited skip", first.Observation)
	}
}
