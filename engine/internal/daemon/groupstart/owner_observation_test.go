package groupstart

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// The restart bug at its decision point: the stop half leaves a run record
// behind until the reap, and the start half must not report that record as a
// service that is already up.
func TestStartAfterStopDoesNotReuseTheStoppedRun(t *testing.T) {
	previous := runsreg.Default
	runsreg.Default = runsreg.New()
	runsreg.Default.Mirror = false
	runsreg.Default.Alive = func(int) bool { return true }
	runsreg.Default.Parents = func() map[int]int { return nil }
	runsreg.Default.StartTime = nil
	t.Cleanup(func() { runsreg.Default = previous })

	rec := runsreg.Default.Register(runsreg.Record{
		PID: 4242, Group: "example", Name: "worker", StartedAt: time.Unix(1, 0),
	})
	observe := runsreg.Default.StopSignalObserver([]state.Run{{RootPID: rec.PID, ID: rec.ID}}, runsreg.ReasonStopped)
	if observe == nil {
		t.Fatal("no stop observer for the registered run")
	}
	observe(rec.PID)

	rt := &daemon.Runtime{Scanner: scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		return nil, nil
	}})}
	reason, _, up, err := alreadyRunning(context.Background(), rt, "example", groups.Service{Name: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if up {
		t.Fatalf("start decision reused the stopped run: %s", reason)
	}
}

func TestLifecycleObservationPropagatesOwnerCancellation(t *testing.T) {
	previous := runsreg.Default
	runsreg.Default = runsreg.New()
	runsreg.Default.Mirror = false
	t.Cleanup(func() { runsreg.Default = previous })
	for _, book := range []bool{false, true} {
		ctx, cancel := context.WithCancel(context.Background())
		l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
			cancel()
			<-ctx.Done()
			return nil, ctx.Err()
		}})
		rt := &daemon.Runtime{Scanner: l}
		var err error
		if book {
			var b *addressBook
			b, err = newAddressBook(ctx, rt, &groups.Config{}, "example")
			if b != nil {
				t.Fatal("cancelled observation built a port book")
			}
		} else {
			var up bool
			_, _, up, err = alreadyRunning(ctx, rt, "example", groups.Service{Name: "worker"})
			if up {
				t.Fatal("cancelled observation declared readiness")
			}
		}
		cancel()
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("book=%v error=%v", book, err)
		}
	}
}

func TestLifecycleObservationDoesNotHideCollectorFailure(t *testing.T) {
	sentinel := errors.New("collector unavailable")
	rt := &daemon.Runtime{Scanner: scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		return nil, sentinel
	}})}
	b, err := newAddressBook(context.Background(), rt, &groups.Config{}, "example")
	if b != nil || !errors.Is(err, sentinel) {
		t.Fatalf("book=%v error=%v", b, err)
	}
}

func TestCancelledPortResolutionDoesNotAcquireOrCache(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// A nil runtime would panic if allocation started despite cancellation.
	b := &addressBook{cfg: &groups.Config{Services: []groups.Service{{Name: "api", PortAuto: true}}}, ports: map[string]int{}, errs: map[string]error{}}
	_, err := b.portContext(ctx, "api")
	if !errors.Is(err, context.Canceled) || len(b.ports) != 0 || len(b.errs) != 0 {
		t.Fatalf("cancelled port resolution=%v", err)
	}
}
