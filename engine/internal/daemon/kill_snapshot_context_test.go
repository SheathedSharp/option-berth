package daemon

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
)

func TestKillSnapshotPreCancelledSkipsSelectionScan(t *testing.T) {
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		t.Error("cancelled control started selection scan")
		return nil, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snap, err := killSnapshot(ctx, &Request{Runtime: &Runtime{Scanner: l}})
	if !errors.Is(err, context.Canceled) || snap.Seq != 0 {
		t.Fatalf("cancelled selection snapshot=%+v error=%v", snap, err)
	}
}

func TestKillSnapshotCancellationReachesObservation(t *testing.T) {
	entered := make(chan struct{})
	l := scanner.New(scanner.Options{ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := killSnapshot(ctx, &Request{Runtime: &Runtime{Scanner: l}}); done <- err }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("selection scan did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("control cancellation became RPC scan failure: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("selection scan did not join")
	}
	if l.Status().Seq != 0 || l.Status().LastError != nil {
		t.Fatalf("cancelled control scan committed state: %+v", l.Status())
	}
}

func TestKillSnapshotCancelledLegacyObservationCannotSelect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		cancel()
		return []ports.ListeningPort{{PID: 123, Port: 18000}}, nil
	}})
	snap, err := killSnapshot(ctx, &Request{Runtime: &Runtime{Scanner: l}})
	if !errors.Is(err, context.Canceled) || len(snap.Ports) != 0 || l.Status().Seq != 0 {
		t.Fatalf("cancelled observation selected targets: snapshot=%+v error=%v status=%+v", snap, err, l.Status())
	}
}

func TestKillSnapshotCollectorFailureRetainsRPCError(t *testing.T) {
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		return nil, errors.New("collector unavailable")
	}})
	_, err := killSnapshot(context.Background(), &Request{Runtime: &Runtime{Scanner: l}})
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != rpc.CodeInternal || !strings.Contains(re.Message, "scan failed: collector unavailable") {
		t.Fatalf("ordinary collector error contract changed: %v", err)
	}
}

func TestKillSnapshotStillForcesEachControlObservation(t *testing.T) {
	calls := 0
	l := scanner.New(scanner.Options{Scan: func(scanner.Include) ([]ports.ListeningPort, error) {
		calls++
		return nil, nil
	}})
	req := &Request{Runtime: &Runtime{Scanner: l}}
	for i := 0; i < 2; i++ {
		if _, err := killSnapshot(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("control observations were coalesced: %d", calls)
	}
}
