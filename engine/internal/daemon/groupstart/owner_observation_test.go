package groupstart

import (
	"context"
	"errors"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

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
			_, up, err = alreadyRunning(ctx, rt, "example", groups.Service{Name: "worker"})
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
