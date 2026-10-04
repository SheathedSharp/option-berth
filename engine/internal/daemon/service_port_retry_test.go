package daemon

import (
	"context"
	"errors"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
	"testing"
)

type busyReleaseFixture struct{}

func (busyReleaseFixture) Error() string                     { return "fixture registry busy" }
func (busyReleaseFixture) RetryableReservationRelease() bool { return true }

func TestFinishGroupStopReobservesTransientRegistryContention(t *testing.T) {
	rt, plan, guard := releaseFixture(t)
	guard.err = busyReleaseFixture{}
	scans := 0
	rt.Scanner = scanner.New(scanner.Options{Runs: rt.RunRegistry, ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
		scans++
		if !claimsMu.TryLock() {
			t.Fatal("rescan ran while holding claims lock")
		}
		claimsMu.Unlock()
		guard.err = nil
		return nil, nil
	}})
	value, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, []state.KillResult{{OK: true}})
	if err != nil {
		t.Fatal(err)
	}
	if env := value.(rpc.KillEnvelope); !env.OK || env.Released != 1 {
		t.Fatal(env)
	}
	if scans != 1 || guard.calls != 2 {
		t.Fatalf("fresh scans=%d guard calls=%d", scans, guard.calls)
	}
}

func TestFinishGroupStopRetryNeverSkipsNewEvidence(t *testing.T) {
	for _, kind := range []string{"listener", "scan error", "changed reservation", "still busy"} {
		t.Run(kind, func(t *testing.T) {
			rt, plan, guard := releaseFixture(t)
			guard.err = busyReleaseFixture{}
			scans := 0
			rt.Scanner = scanner.New(scanner.Options{Runs: rt.RunRegistry, ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
				scans++
				if kind != "still busy" {
					guard.err = nil
				}
				switch kind {
				case "listener":
					return []ports.ListeningPort{{Port: 12501, PID: 424242}}, nil
				case "scan error":
					return nil, errors.New("fixture incomplete scan")
				case "changed reservation":
					row := plan.observed.Rows[0]
					row.ExpiresAt = row.ExpiresAt.Add(1)
					if err := rt.Store.Claims().Put(row); err != nil {
						t.Fatal(err)
					}
				}
				return nil, nil
			}})
			if _, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, []state.KillResult{{OK: true}}); err == nil {
				t.Fatal("unconfirmed release succeeded")
			}
			if scans < 1 || scans > 3 {
				t.Fatalf("retry scan count=%d", scans)
			}
			held, err := rt.Store.Claims().List()
			if err != nil || len(held) != 1 {
				t.Fatalf("reservation lost: %v %v", held, err)
			}
		})
	}
}

func TestFinishGroupStopNeverRetriesPermanentFailureOrCancellation(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		rt, plan, guard := releaseFixture(t)
		guard.err = errors.New("fixture permanent failure")
		ctx, cancel := context.WithCancel(context.Background())
		if canceled {
			guard.err = busyReleaseFixture{}
			cancel()
		}
		defer cancel()
		rt.Scanner = scanner.New(scanner.Options{ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
			t.Error("unexpected retry scan")
			return nil, nil
		}})
		if _, err := finishGroupStop(ctx, &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, nil); err == nil {
			t.Fatal("failure became success")
		}
		held, _ := rt.Store.Claims().List()
		if len(held) != 1 {
			t.Fatal("lost reservation")
		}
	}
}
