package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/claims"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/scanner"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/sheathedsharp/option-berth/internal/store"
)

// This adapter tests daemon wiring only. Raw-record exclusion is tested against
// the actual production registry in runsreg/group_quiescence_test.go.
type releaseGuardFixture struct {
	noRuns
	err    error
	before func()
	calls  int
}

func (g *releaseGuardFixture) WithNoGroupRuns(ctx context.Context, group, path string, mutate func() (int, error)) (int, error) {
	g.calls++
	if g.err != nil {
		return 0, g.err
	}
	if g.before != nil {
		g.before()
	}
	return mutate()
}

func releaseFixture(t *testing.T) (*Runtime, *servicePortRelease, *releaseGuardFixture) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "fixture.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := &groups.Config{Dir: dir, Path: filepath.Join(dir, groups.ConfigName), Services: []groups.Service{{Name: "web", PortAuto: true}}}
	project, worktree := claims.Identity(dir, "", "")
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := store.ClaimRow{Port: 12501, Key: claims.ServiceKey(project, worktree, "web"), Project: project, Worktree: worktree, CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
	if err := st.Claims().Put(row); err != nil {
		t.Fatal(err)
	}
	rt := &Runtime{Store: st}
	guard := &releaseGuardFixture{}
	rt.SetRuns(guard)
	plan, err := captureServicePortRelease(context.Background(), rt, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return rt, plan, guard
}

func TestServicePortReleaseRequiresPostStopEvidence(t *testing.T) {
	for _, kind := range []string{"scan failure", "cancel", "permission", "not found", "partial tree", "unknown owner", "foreign owner", "remaining group"} {
		t.Run(kind, func(t *testing.T) {
			rt, plan, guard := releaseFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			after := state.Snapshot{}
			var scanErr error
			rows := []state.KillResult{{OK: true}}
			switch kind {
			case "scan failure":
				scanErr = errors.New("collector unavailable")
			case "cancel":
				cancel()
			case "permission":
				rows[0] = state.KillResult{OK: false, Code: killer.CodePermissionDenied}
			case "not found":
				rows[0] = state.KillResult{OK: false, Code: killer.CodeNotFound}
			case "partial tree":
				rows = append(rows, state.KillResult{OK: false, Code: killer.CodePermissionDenied})
			case "unknown owner":
				after.Ports = []state.Port{{Port: 12501}}
			case "foreign owner":
				after.Ports = []state.Port{{Port: 12501, Group: ptr("other"), PID: 424242}}
			case "remaining group":
				after.Ports = []state.Port{{Port: 12502, Group: ptr("demo"), PID: 424242}}
			}
			if n, err := plan.release(ctx, rt, "demo", after, scanErr, rows); n != 0 || err == nil {
				t.Fatalf("release = %d, %v", n, err)
			}
			held, err := rt.Store.Claims().List()
			if err != nil || len(held) != 1 {
				t.Fatalf("lost reservation: %+v, %v", held, err)
			}
			if guard.calls != 0 {
				t.Fatal("invalid observation reached commit guard")
			}
		})
	}
}

func TestServicePortReleaseUsesOriginalReservationAndAuthority(t *testing.T) {
	for _, kind := range []string{"refresh before commit", "registered replacement", "old registry", "different store"} {
		t.Run(kind, func(t *testing.T) {
			rt, plan, guard := releaseFixture(t)
			switch kind {
			case "refresh before commit":
				guard.before = func() {
					row := plan.observed.Rows[0]
					row.ExpiresAt = row.ExpiresAt.Add(time.Minute)
					if err := rt.Store.Claims().Put(row); err != nil {
						t.Fatal(err)
					}
				}
			case "registered replacement":
				guard.err = errors.New("replacement run remains")
			case "old registry":
				rt.SetRuns(noRuns{})
			case "different store":
				rt.Store = nil
			}
			if n, err := plan.release(context.Background(), rt, "demo", state.Snapshot{}, nil, []state.KillResult{{OK: true}}); n != 0 || err == nil {
				t.Fatalf("release = %d, %v", n, err)
			}
			rows, err := plan.store.Claims().List()
			if err != nil || len(rows) != 1 {
				t.Fatalf("lost original reservation: %+v, %v", rows, err)
			}
		})
	}
}

func TestServicePortReleaseSuccessAndRepeatedDown(t *testing.T) {
	rt, plan, guard := releaseFixture(t)
	result, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, nil, []state.KillResult{{OK: true}})
	if err != nil {
		t.Fatal(err)
	}
	env := result.(rpc.KillEnvelope)
	if !env.OK || env.Released != 1 || guard.calls != 1 {
		t.Fatalf("result = %+v, guard=%d", env, guard.calls)
	}
	// A new empty capture must not delete a reservation acquired afterwards.
	empty := &servicePortRelease{store: plan.store, configPath: plan.configPath}
	if err := plan.store.Claims().Put(plan.observed.Rows[0]); err != nil {
		t.Fatal(err)
	}
	if n, err := empty.release(context.Background(), rt, "demo", state.Snapshot{}, nil, nil); n != 0 || err != nil {
		t.Fatalf("empty release = %d, %v", n, err)
	}
	rows, err := plan.store.Claims().List()
	if err != nil || len(rows) != 1 {
		t.Fatalf("new claim lost: %+v %v", rows, err)
	}
}

func TestFinishGroupStopReportsRetainedReservationsAsRPCFailure(t *testing.T) {
	rt, plan, _ := releaseFixture(t)
	result, err := finishGroupStop(context.Background(), &Request{Runtime: rt}, "demo", plan, state.Snapshot{}, errors.New("incomplete observation"), nil)
	var rpcErr *rpc.Error
	if result != nil || !errors.As(err, &rpcErr) || rpcErr.Code != rpc.CodeInternal {
		t.Fatalf("result=%+v error=%v", result, err)
	}
}

func TestNilReleasePlanNeverTouchesStore(t *testing.T) {
	// Dry runs, scoped stops and manifests without auto ports have no plan.
	var plan *servicePortRelease
	if n, err := plan.release(context.Background(), &Runtime{}, "demo", state.Snapshot{}, nil, nil); n != 0 || err != nil {
		t.Fatalf("release = %d, %v", n, err)
	}
}

// Exercise the real handler's formerly unconditional empty-target branch. The
// scanner is explicitly empty and the registry has no PID targets: this test
// definition never needs a host process, a listener, or a signal adapter.
func TestGroupsKillEmptyStopUsesReservationGuard(t *testing.T) {
	for _, kind := range []string{"success", "guard rejects", "refresh during scan", "dry run", "scoped stop"} {
		t.Run(kind, func(t *testing.T) {
			rt, plan, guard := releaseFixture(t)
			if err := os.WriteFile(plan.configPath, []byte("name: demo\nservices:\n  - name: web\n    cmd: fixture\n    port: auto\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			scans := 0
			rt.Scanner = scanner.New(scanner.Options{Runs: rt.RunRegistry, ScanContext: func(context.Context, scanner.Include) ([]ports.ListeningPort, error) {
				scans++
				if kind == "refresh during scan" {
					row := plan.observed.Rows[0]
					row.ExpiresAt = row.ExpiresAt.Add(time.Minute)
					if err := rt.Store.Claims().Put(row); err != nil {
						return nil, err
					}
				}
				return nil, nil
			}})
			if kind == "guard rejects" {
				guard.err = errors.New("still registered")
			}
			params := rpc.GroupsKillParams{ConfigPath: &plan.configPath, Release: true}
			if kind == "dry run" {
				params.DryRun = true
			}
			if kind == "scoped stop" {
				params.Only = []string{"web"}
			}
			raw, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			request := &Request{Runtime: rt, Method: "groups.kill", Params: raw}
			value, err := handleGroupsKill(context.Background(), request)
			if kind == "guard rejects" || kind == "refresh during scan" {
				if err == nil {
					t.Fatal("handler released an unconfirmed reservation")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				env := value.(rpc.KillEnvelope)
				want := 0
				if kind == "success" {
					want = 1
				}
				if !env.OK || env.Released != want {
					t.Fatalf("envelope = %+v, want released=%d", env, want)
				}
			}
			if scans != 1 {
				t.Fatalf("empty stop cost %d scans, want one", scans)
			}
			held, readErr := rt.Store.Claims().List()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if kind == "success" {
				if len(held) != 0 {
					t.Fatalf("reservations remained: %+v", held)
				}
				value, err = handleGroupsKill(context.Background(), request)
				if err != nil || value.(rpc.KillEnvelope).Released != 0 {
					t.Fatalf("repeated down = %+v, %v", value, err)
				}
			} else if len(held) != 1 {
				t.Fatalf("reservation disappeared: %+v", held)
			}
			if (kind == "dry run" || kind == "scoped stop") && guard.calls != 0 {
				t.Fatal("non-release call reached commit guard")
			}
		})
	}
}
