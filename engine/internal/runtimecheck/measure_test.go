package runtimecheck

// Opt-in native measurements: all collectors/process facts/providers are fake.
// No daemon Run, real process, socket, probe, credential or network is used.
import (
	"context"
	"encoding/json"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/runsreg"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRuntimeMeasurement(t *testing.T) {
	if os.Getenv("OBERTH_RUNTIME_MEASURE") != "1" {
		t.Skip("opt-in fake-source comparative measurement")
	}
	const samples = 20
	var durations []time.Duration
	var collections atomic.Int32
	for i := 0; i < samples; i++ {
		root := t.TempDir()
		reg := runsreg.New()
		reg.Mirror = false
		reg.Alive = func(int) bool { return true }
		reg.StartTime = func(int) (time.Time, bool) { return time.Time{}, false }
		reg.Parents = func() map[int]int { return nil }
		var blocked atomic.Bool
		entered, release := make(chan struct{}), make(chan struct{})
		loop := scanner.New(scanner.Options{Runs: func() groups.Registry { return reg }, SampleStats: func([]int) map[int]ports.ProcSample { return nil }, ScanContext: func(ctx context.Context, _ scanner.Include) ([]ports.ListeningPort, error) {
			collections.Add(1)
			if blocked.Load() {
				close(entered)
				select {
				case <-release:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}
			return nil, nil
		}})
		for _, name := range []string{"a", "b", "c"} {
			dir := filepath.Join(root, name)
			if err := os.MkdirAll(dir, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(dir, "oberth.yaml")
			if err := os.WriteFile(file, []byte("name: "+name+"\nservices:\n  - name: worker\n    cmd: synthetic-worker\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := loop.LoadConfig(file); err != nil {
				t.Fatal(err)
			}
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		if _, err := loop.SnapshotAllContext(ctx, scanner.Include{}); err != nil {
			cancel()
			t.Fatal(err)
		}
		blocked.Store(true)
		done := make(chan error, 1)
		go func() { _, err := loop.RescanContext(ctx, scanner.Include{}); done <- err }()
		select {
		case <-entered:
		case <-ctx.Done():
			cancel()
			t.Fatal("fake collector did not enter")
		}
		reg.Register(runsreg.Record{ID: "r2", PID: 92010, Group: "a", Name: "worker", StartedAt: time.Now()})
		// Match lifecycle invalidation in the baseline production handler.
		loop.Invalidate()
		timer := time.AfterFunc(15*time.Millisecond, func() { blocked.Store(false); close(release) })
		started := time.Now()
		snap, err := loop.SnapshotAllContext(ctx, scanner.Include{})
		durations = append(durations, time.Since(started))
		// Join every own collector/timer before returning; no detached timeout work.
		if scanErr := <-done; scanErr != nil {
			cancel()
			t.Fatal(scanErr)
		}
		timer.Stop()
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, g := range snap.Groups {
			for _, s := range g.Services {
				if g.Name == "a" {
					found = s.Running && s.RunID != nil && *s.RunID == "r2"
				} else if s.Running {
					t.Fatal("cross-worktree ownership")
				}
			}
		}
		if !found {
			t.Fatal("registered worker missing")
		}
	}
	sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
	t.Logf("MEASURE {\"kind\":\"busy_worker\",\"samples\":%d,\"status_reads\":%d,\"os_collections\":%d,\"median_us\":%d,\"p95_us\":%d}", samples, samples, collections.Load(), durations[samples/2].Microseconds(), durations[18].Microseconds())
}
func TestAttentionReadMeasurement(t *testing.T) {
	if os.Getenv("OBERTH_RUNTIME_MEASURE") != "1" {
		t.Skip("opt-in scaled fake-source comparative measurement")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan state.StateChanged, 2)
	ready := make(chan struct{})
	var once sync.Once
	var reads atomic.Int32
	done := make(chan runner.SubscriptionStats, 1)
	go func() {
		stats, err := measurementSubscribe(ctx, func(context.Context, string, string) (runner.Status, error) {
			reads.Add(1)
			once.Do(func() { close(ready) })
			var s runner.Status
			s.Scope.Root = "/synthetic/a"
			return s, nil
		}, changes)
		if err != nil {
			t.Error(err)
		}
		done <- stats
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("no initial status")
	}
	// Same scaled 250ms timeline, 5ms checks, changes at 75ms and 160ms.
	first := time.NewTimer(75 * time.Millisecond)
	<-first.C
	changes <- state.StateChanged{Seq: 1, Source: &state.EventSource{WorktreeID: "a"}, Changed: []string{"service_changed"}}
	second := time.NewTimer(85 * time.Millisecond)
	<-second.C
	changes <- state.StateChanged{Seq: 2, Source: &state.EventSource{WorktreeID: "a"}, Changed: []string{"service_changed"}}
	finish := time.NewTimer(90 * time.Millisecond)
	<-finish.C
	cancel()
	var stats runner.SubscriptionStats
	select {
	case stats = <-done:
	case <-time.After(time.Second):
		t.Fatal("task worker did not join")
	}
	if stats.ModelCalls != 0 || stats.NoEventPolls != stats.Polls {
		t.Fatalf("normal fake status called provider: %+v", stats)
	}
	t.Logf("MEASURE {\"kind\":\"idle_attention\",\"timeline_ms\":250,\"interval_ms\":5,\"candidate_calibration_ms\":50,\"events\":2,\"status_reads\":%d,\"model_calls\":%d}", reads.Load(), stats.ModelCalls)
}
func TestRuntimeFormatCompatibility(t *testing.T) {
	path := os.Getenv("OBERTH_COMPAT_PATH")
	if path == "" {
		t.Skip("opt-in prior-version compatibility fixture")
	}
	if !filepath.IsAbs(path) {
		t.Fatal("absolute owned temporary fixture required")
	}
	stamp := time.Date(2026, 11, 14, 0, 0, 0, 123456789, time.UTC)
	if os.Getenv("OBERTH_COMPAT_WRITE") == "1" {
		st, err := store.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		if err = st.AppendRunExit(store.RunExitRow{ID: "compat-run", PID: 42, Group: "a", Name: "worker", StartedAt: stamp, ExitedAt: stamp.Add(time.Second), LogPath: "/synthetic/a.log", LogOffset: 987, Reason: "crashed"}); err != nil {
			t.Fatal(err)
		}
		if err = st.Claims().Put(store.ClaimRow{Port: 17001, Key: "a/worker", Project: "a", Worktree: "gone", Since: stamp, Until: stamp.Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if err = st.RecordWorktreeIdentity(store.WorktreeIdentity{Identity: "wt_0123456789ab", Root: "/synthetic/deleted", Group: "a", Project: "a", Worktree: "gone"}); err != nil {
			t.Fatal(err)
		}
		if err = st.Close(); err != nil {
			t.Fatal(err)
		}
		payload, err := json.Marshal(runs.Registry{Runs: map[int]runs.Entry{42: {ID: "compat-live", PID: 42, Group: "a", Name: "worker", StartedAt: stamp.Format(time.RFC3339Nano), LogPath: "/synthetic/live.log", LogOffset: 789, PortHint: 17002, SpecHash: "captured"}}})
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path+".json", payload, 0600); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	exits, err := st.RunExits(10)
	if err != nil || len(exits) != 1 || exits[0].ID != "compat-run" || exits[0].LogOffset != 987 || !exits[0].StartedAt.Equal(stamp) {
		t.Fatalf("exit mismatch %+v %v", exits, err)
	}
	claims, err := st.Claims().List()
	if err != nil || len(claims) != 1 || claims[0].Port != 17001 {
		t.Fatal("claims mismatch", err)
	}
	identity, found, err := st.WorktreeIdentity("wt_0123456789ab")
	if err != nil || !found || identity.Worktree != "gone" {
		t.Fatal("identity mismatch", err)
	}
	payload, err := os.ReadFile(path + ".json")
	if err != nil {
		t.Fatal(err)
	}
	// Typed wire reader only: Load() would probe/prune real PID 42, so never use it.
	var ledger runs.Registry
	if err = json.Unmarshal(payload, &ledger); err != nil {
		t.Fatal(err)
	}
	live := ledger.Runs[42]
	if live.ID != "compat-live" || live.LogOffset != 789 || live.PortHint != 17002 || live.StartedAt != stamp.Format(time.RFC3339Nano) || live.SpecHash != "captured" {
		t.Fatal("live mirror format mismatch")
	}
}
