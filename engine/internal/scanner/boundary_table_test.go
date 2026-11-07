package scanner

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestHealthAdmissionRejectsASpentBudgetAndADepartedCaller(t *testing.T) {
	slots := make(chan struct{}, 1)
	if admitHealthProbe(context.Background(), slots, time.Now().Add(-time.Second)) {
		t.Fatal("a probe was admitted after the round's budget was spent")
	}
	if len(slots) != 0 {
		t.Fatal("a refused probe kept its slot")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if admitHealthProbe(ctx, slots, time.Time{}) {
		t.Fatal("a departed caller was admitted")
	}
	probes := 0
	round, err := runHealthRound(context.Background(), []healthTarget{{
		kind: groups.HealthHTTP, host: "127.0.0.1", port: 9, path: "/",
	}}, func(context.Context, string, int, string, time.Duration) ports.HealthResult {
		probes++
		return ports.HealthResult{Status: "healthy"}
	}, 0)
	if err != nil || probes != 1 || round == nil || !round.probed[0] {
		t.Fatalf("err=%v probes=%d round=%v, a round with budget left must probe", err, probes, round)
	}
}

func TestCommandVerdictIsWrittenOnlyOntoTheServiceThatDeclaredIt(t *testing.T) {
	ok := state.Health{Status: state.HealthOK, Configured: true}
	gg := []state.Group{{Services: []state.Service{{Name: "worker"}}}}
	round := &healthRound{
		probed:  []bool{true, true, true},
		results: []state.Health{ok, ok, ok},
	}
	applyCommandHealth(gg, []healthTarget{
		{kind: groups.HealthCommand, group: -1, service: 0},
		{kind: groups.HealthCommand, group: 5, service: 0},
		{kind: groups.HealthCommand, group: 0, service: 0},
	}, round)
	if gg[0].Services[0].HealthStatus == nil || gg[0].Services[0].HealthStatus.Status != state.HealthOK {
		t.Fatalf("health = %+v, want the in-range command verdict", gg[0].Services[0].HealthStatus)
	}
}

func TestReplacingTheStoreSeedsItsRoots(t *testing.T) {
	first := openStore(t)
	l := loopWith(first)
	if _, err := l.Rescan(Include{}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeConfig(t, dir, "name: after\n")
	second := openStore(t)
	if err := second.AddRoot(dir); err != nil {
		t.Fatal(err)
	}
	l.SetStore(second)
	snap, err := l.Rescan(Include{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Groups) != 1 || snap.Groups[0].Name != "after" {
		t.Fatalf("groups = %+v, the replacement store's root was not read", snap.Groups)
	}
}

type warnCount struct{ n int }

func (h *warnCount) Enabled(context.Context, slog.Level) bool  { return true }
func (h *warnCount) Handle(context.Context, slog.Record) error { h.n++; return nil }
func (h *warnCount) WithAttrs([]slog.Attr) slog.Handler        { return h }
func (h *warnCount) WithGroup(string) slog.Handler             { return h }

type panicRoots struct{ identityLedger }

func (panicRoots) Roots() ([]string, error) { panic("nil store must not be read") }

func TestReloadConfigsReadsRootsOnlyFromALiveStore(t *testing.T) {
	l := New(Options{Store: &panicRoots{}})
	l.opts.Store = nil
	if _, invalid := l.ReloadConfigs(); invalid != nil && len(invalid) != 0 {
		t.Fatalf("invalid = %+v", invalid)
	}

	dir := t.TempDir()
	writeConfig(t, dir, "name: kept\n")
	warns := &warnCount{}
	st := openStore(t)
	if err := st.AddRoot(dir); err != nil {
		t.Fatal(err)
	}
	l = New(Options{Store: st, Logger: slog.New(warns)})
	valid, _ := l.ReloadConfigs()
	if valid != 1 || warns.n != 0 {
		t.Fatalf("valid=%d warns=%d, a successful root read warned or dropped the file", valid, warns.n)
	}
}

func TestOlderScanCannotOverwriteAndAnEqualGenerationCan(t *testing.T) {
	l := New(Options{Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"})})
	if _, _, changed, err := l.scanLocked(Include{}, Include{}, false); err != nil || !changed {
		t.Fatalf("seed changed=%v err=%v", changed, err)
	}
	kept := l.Cached().Seq
	if _, _, changed := l.commitScan(commit{seq: 0, rows: []state.Port{{Port: 10}}}); changed || l.Cached().Seq != kept || l.Cached().Ports[0].Port != 9 {
		t.Fatalf("older generation wrote port %+v seq %d", l.Cached().Ports, l.Cached().Seq)
	}
	if _, _, changed := l.commitScan(commit{seq: l.committedSeq, rows: []state.Port{{Port: 11}}}); !changed || l.Cached().Ports[0].Port != 11 {
		t.Fatalf("equal generation did not commit, port %+v", l.Cached().Ports)
	}
}

func TestStaleScanFailureIsNotNewsAndACurrentOneIs(t *testing.T) {
	var kinds []string
	l := New(Options{
		Scan: fixedRows(ports.ListeningPort{Port: 9, PID: 1, Process: "app"}),
		Publish: func(_, _ state.Snapshot, events []state.Event) {
			for _, ev := range events {
				kinds = append(kinds, ev.Kind)
			}
		},
	})
	if _, _, _, err := l.scanLocked(Include{}, Include{}, false); err != nil {
		t.Fatal(err)
	}
	kinds = nil
	_, _, _, err := l.recordScanFailure(context.Background(), 0, 0, errors.New("old"), true, state.Snapshot{})
	if err != nil || len(kinds) != 0 || l.Cached().Ports[0].Port != 9 {
		t.Fatalf("err=%v kinds=%v cached=%+v, an older failure was published", err, kinds, l.Cached().Ports)
	}
	_, _, _, err = l.recordScanFailure(context.Background(), l.committedSeq+1, 0, errors.New("now"), true, state.Snapshot{})
	if err == nil || err.Error() != "now" || len(kinds) != 1 || kinds[0] != "scan_error" || l.Cached().Ports[0].Port != 9 {
		t.Fatalf("err=%v kinds=%v, a current failure must be reported and the rows kept", err, kinds)
	}
	kinds = nil
	_, _, _, err = l.recordScanFailure(context.Background(), l.outcomeSeq+1, 0, errors.New("quiet"), false, state.Snapshot{})
	if err == nil || len(kinds) != 0 {
		t.Fatalf("err=%v kinds=%v, a failure the caller did not ask to report was published", err, kinds)
	}
}

func TestIntervalBoundaries(t *testing.T) {
	scans := []struct {
		in, want time.Duration
	}{
		{0, BaseInterval},
		{-time.Second, BaseInterval},
		{time.Millisecond, MinScanInterval},
		{MinScanInterval - time.Nanosecond, MinScanInterval},
		{MinScanInterval, MinScanInterval},
		{MinScanInterval + time.Second, MinScanInterval + time.Second},
	}
	for _, tc := range scans {
		if got := resolveScanInterval(tc.in); got != tc.want {
			t.Fatalf("scan %s -> %s, want %s", tc.in, got, tc.want)
		}
	}
	stats := []struct {
		in, want time.Duration
	}{
		{0, StatsInterval},
		{-time.Second, StatsInterval},
		{time.Millisecond, MinStatsInterval},
		{MinStatsInterval, MinStatsInterval},
		{time.Second, time.Second},
	}
	for _, tc := range stats {
		l := New(Options{StatsInterval: tc.in})
		if got := l.statsInterval(); got != tc.want {
			t.Fatalf("stats %s -> %s, want %s", tc.in, got, tc.want)
		}
	}
	base := 2 * time.Second
	max := 10 * time.Second
	if got := backoff(8*time.Second, base, max); got != max {
		t.Fatalf("backoff above the cap = %s", got)
	}
	if got := backoff(time.Second, base, max); got != base {
		t.Fatalf("backoff below the base = %s", got)
	}
	if got := backoff(4*time.Second, base, max); got != 6*time.Second {
		t.Fatalf("backoff inside the range = %s", got)
	}
}

func TestProcessIDsAreSampledOnceAndNonPositiveOnesAreNot(t *testing.T) {
	got := mergePIDs([]int{7, 0, -1, 7}, []int{8, 7})
	if len(got) != 2 || got[0] != 7 || got[1] != 8 {
		t.Fatalf("pids = %v, want 7 then 8", got)
	}
	port := 9
	groups := []state.Group{{Services: []state.Service{
		{Name: "worker", Running: true, PID: intPtr(7)},
		{Name: "zero", Running: true, PID: intPtr(0)},
		{Name: "down", PID: intPtr(8)},
		{Name: "listener", Running: true, PID: intPtr(9), PortActual: &port},
	}}}
	pids := serviceStatsPIDs(groups)
	if len(pids) != 1 || pids[0] != 7 {
		t.Fatalf("service pids = %v, want only 7", pids)
	}
}

func TestANewGroupReadyTimeoutIsNotDropped(t *testing.T) {
	root := "/code/example"
	exit := state.ServiceExit{Reason: "ready_timeout", RunID: "run-1"}
	events := deriveEvents(
		state.Snapshot{},
		state.Snapshot{Seq: 1, Groups: []state.Group{{
			Name: "example", Repo: "example", RootDir: &root,
			Services: []state.Service{{Name: "api", LastExit: &exit}},
		}}},
		"now",
	)
	found := false
	for _, ev := range events {
		if ev.Kind == "ready_timeout" {
			found = true
		}
	}
	if !found {
		t.Fatalf("events = %+v, a newly appeared group's ready timeout was dropped", events)
	}
}

func TestStartTimesInsideToleranceMatchAndOutsideDoNot(t *testing.T) {
	base := time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		recorded string
		sampled  string
		keep     bool
	}{
		{"equal", base.Format(time.RFC3339), base.Format(time.RFC3339), true},
		{"on the tolerance", base.Format(time.RFC3339), base.Add(ports.StartTolerance).Format(time.RFC3339), true},
		{"past the tolerance", base.Format(time.RFC3339), base.Add(ports.StartTolerance + time.Second).Format(time.RFC3339), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pid := 7
			groups := []state.Group{{Services: []state.Service{{
				Name: "worker", Running: true, PID: &pid, StartedAt: &tc.recorded,
			}}}}
			FillServiceStatsContext(context.Background(), groups, func([]int) map[int]ports.ProcSample {
				return map[int]ports.ProcSample{pid: {MemoryRSS: 4, StartedAt: tc.sampled}}
			})
			got := groups[0].Services[0].Stats != nil
			if got != tc.keep {
				t.Fatalf("kept=%v want %v", got, tc.keep)
			}
		})
	}
}
