package groupstart

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/runsreg"
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

	rt := &runtimesvc.HostContext{Scanner: scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
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

// A run that has been signalled is still published as running until its exit
// is observed, because "accepted signal" is not "process exited". The start
// decision must not read that row as a service that is already up: restart
// sends the stop and then starts, and the snapshot it sees in between is
// exactly this row. The registry is the authority on whether this engine has a
// live run for a service; the snapshot only answers for listeners it did not
// start.
func TestStartAfterStopIgnoresTheStoppedSnapshotRow(t *testing.T) {
	previous := runsreg.Default
	runsreg.Default = runsreg.New()
	runsreg.Default.Mirror = false
	runsreg.Default.Alive = func(int) bool { return true }
	runsreg.Default.Parents = func() map[int]int { return nil }
	runsreg.Default.StartTime = nil
	t.Cleanup(func() { runsreg.Default = previous })

	rec := runsreg.Default.Register(runsreg.Record{
		PID: 4343, Group: "example", Name: "worker", StartedAt: time.Unix(1, 0),
	})
	observe := runsreg.Default.StopSignalObserver([]state.Run{{RootPID: rec.PID, ID: rec.ID}}, runsreg.ReasonStopped)
	if observe == nil {
		t.Fatal("no stop observer for the registered run")
	}
	observe(rec.PID)

	dir := t.TempDir()
	path := filepath.Join(dir, groups.ConfigName)
	if err := os.WriteFile(path, []byte("name: example\nservices:\n  - name: worker\n    cmd: sleep 60\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		return nil, nil
	}})
	if err := l.LoadConfig(path); err != nil {
		t.Fatal(err)
	}
	l.SetRuns(func() groups.Registry { return runsreg.Default })
	rt := &runtimesvc.HostContext{Scanner: l}

	snap, err := l.SnapshotForReadinessContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row, found := serviceRow(snap, "example", "worker")
	if !found || !row.Running {
		t.Fatalf("the snapshot no longer reproduces the case: %+v", snap.Groups)
	}

	reason, _, up, err := alreadyRunning(context.Background(), rt, "example", groups.Service{Name: "worker"})
	if err != nil {
		t.Fatal(err)
	}
	if up {
		t.Fatalf("start decision reused a run that already has a stop signal: %s", reason)
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
		rt := &runtimesvc.HostContext{Scanner: l}
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
	rt := &runtimesvc.HostContext{Scanner: scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
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
