package servicefacts

import (
	"fmt"
	"math/rand"
	"reflect"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestCaptureSelectsOneCoherentRunRegardlessOfRegistryOrder(t *testing.T) {
	key := Key{"repo@left", "api"}
	base := time.Unix(10, 0)
	records := []RunRecord{
		{Key: key, Run: state.ServiceRun{ID: "old", PID: 1}, StartedAt: base, PortHint: 8001},
		{Key: key, Run: state.ServiceRun{ID: "new", PID: 2}, StartedAt: base.Add(time.Nanosecond), PortHint: 8002},
		{Key: key, Run: state.ServiceRun{ID: "stopping", PID: 3}, StartedAt: base.Add(time.Hour), PortHint: 8003, Stopping: true},
	}
	random := rand.New(rand.NewSource(1))
	for i := 0; i < 30; i++ {
		random.Shuffle(len(records), func(i, j int) { records[i], records[j] = records[j], records[i] })
		facts := Capture(records, []ExitRecord{{Key: key, Exit: state.ServiceExit{RunID: "old-exit"}}})[key]
		if facts.Run == nil || facts.Run.ID != "new" || facts.PortHint != 8002 || facts.Stopping || facts.LastExit != nil {
			t.Fatalf("incoherent selected facts: %+v", facts)
		}
	}
}

func TestCaptureOrdersPublishedTimesAndBreaksTies(t *testing.T) {
	key := Key{"repo", "api"}
	cases := []struct {
		name string
		a, b state.ServiceRun
		want string
	}{
		{"timezone", state.ServiceRun{ID: "later", StartedAt: "2026-09-30T09:00:00Z"}, state.ServiceRun{ID: "earlier", StartedAt: "2026-09-30T10:00:00+02:00"}, "later"},
		{"equal start", state.ServiceRun{ID: "a", PID: 1}, state.ServiceRun{ID: "b", PID: 2}, "b"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			for _, records := range [][]RunRecord{
				{{Key: key, Run: tt.a}, {Key: key, Run: tt.b}},
				{{Key: key, Run: tt.b}, {Key: key, Run: tt.a}},
			} {
				if got := Capture(records, nil)[key].Run; got == nil || got.ID != tt.want {
					t.Fatalf("run = %+v, want %s", got, tt.want)
				}
			}
		})
	}
}

func TestCaptureDetachesInputsAndKeepsExitScope(t *testing.T) {
	left, right := Key{"repo@left", "api"}, Key{"repo@right", "api"}
	runs := []RunRecord{{Key: left, Run: state.ServiceRun{ID: "live"}, Stopping: true}}
	exits := []ExitRecord{
		{Key: left, Exit: state.ServiceExit{RunID: "left-old"}},
		{Key: right, Exit: state.ServiceExit{RunID: "right-new", Code: 2}},
		{Key: right, Exit: state.ServiceExit{RunID: "right-old", Code: 1}},
	}
	snapshot := Capture(runs, exits)
	runs[0].Run.ID = "mutated"
	exits[1].Exit.RunID = "mutated"
	if got := snapshot[left]; got.Run.ID != "live" || !got.Stopping || got.LastExit != nil {
		t.Fatalf("stopping run must suppress old exits: %+v", got)
	}
	if got := snapshot[right]; got.Run != nil || got.LastExit.RunID != "right-new" || got.LastExit.Code != 2 {
		t.Fatalf("exit must remain detached and worktree-scoped: %+v", got)
	}
	if got := Capture([]RunRecord{{Run: state.ServiceRun{ID: "unscoped"}}}, []ExitRecord{{}}); len(got) != 0 {
		t.Fatalf("unscoped records entered snapshot: %+v", got)
	}
}

func TestCollectorTransfersSnapshotWithoutSharingOnReuse(t *testing.T) {
	key := Key{"repo", "api"}
	var collector Collector
	collector.AddExit(ExitRecord{Key: key, Exit: state.ServiceExit{RunID: "old"}})
	collector.AddRun(RunRecord{Key: key, Run: state.ServiceRun{ID: "first"}})
	first := collector.Snapshot()
	collector.AddRun(RunRecord{Key: key, Run: state.ServiceRun{ID: "second"}})
	second := collector.Snapshot()
	second[key].Run.ID = "mutated second"
	if first[key].Run.ID != "first" || first[key].LastExit != nil {
		t.Fatalf("collector reuse mutated an earlier snapshot: %+v", first[key])
	}
	if collector.Snapshot() != nil {
		t.Fatal("collector retained transferred facts")
	}
}

func TestResolverReusesIndexesWithoutAllocatingPerService(t *testing.T) {
	group := "repo"
	key := Key{group, "api"}
	facts := Capture([]RunRecord{{Key: key, Run: state.ServiceRun{ID: "live"}}}, nil)
	listeners := []state.Port{
		{Group: &group, Port: 8000, Run: &state.Run{ID: "old", Name: "api"}},
		{Group: &group, Port: 8000, Run: &state.Run{ID: "live", Name: "api"}},
	}
	resolver := NewResolver(facts, listeners)
	declaration := Declaration{Key: key, Port: 8000}
	var result Result
	if allocations := testing.AllocsPerRun(100, func() { result = resolver.Resolve(declaration) }); allocations != 0 {
		t.Fatalf("per-service allocations = %v, want zero after indexing", allocations)
	}
	if result.Run == nil || result.Listener != &listeners[1] {
		t.Fatalf("result = %+v", result)
	}
	if allocations := testing.AllocsPerRun(100, func() {
		result = NewResolver(facts, nil).Resolve(declaration)
	}); allocations != 0 {
		t.Fatalf("portless resolver allocated indexes: %v", allocations)
	}
	if !result.Running || result.Listener != nil {
		t.Fatalf("portless result = %+v", result)
	}
}

func TestSummarizeOwnershipAndLifecycle(t *testing.T) {
	group, other := "repo@left", "repo@right"
	key := Key{group, "api"}
	live := &state.ServiceRun{ID: "current", PID: 42}
	exit := &state.ServiceExit{RunID: "finished", Code: 1}
	cases := []struct {
		name        string
		declaration Declaration
		evidence    Evidence
		listeners   []state.Port
		running     bool
		run         bool
		listener    int
		lastExit    bool
	}{
		{name: "portless worker", evidence: Evidence{Run: live}, running: true, run: true, listener: -1},
		{name: "no evidence", listener: -1},
		{name: "finished run", evidence: Evidence{LastExit: exit}, listener: -1, lastExit: true},
		{name: "observed is not owned", declaration: Declaration{Port: 8080}, listeners: []state.Port{{Group: &group, Port: 8080, PID: 77}}, running: true},
		{name: "foreign worktree", declaration: Declaration{Port: 8080}, listeners: []state.Port{{Group: &other, Port: 8080}}, listener: -1},
		{name: "unknown worktree", declaration: Declaration{Port: 8080}, listeners: []state.Port{{Port: 8080}}, listener: -1},
		{name: "another service owns port", declaration: Declaration{Port: 8080}, listeners: []state.Port{{Group: &group, Port: 8080, Run: &state.Run{Name: "worker", Group: group}}}, listener: -1},
		{name: "foreign run contradicts group", declaration: Declaration{Port: 8080}, listeners: []state.Port{{Group: &group, Port: 8080, Run: &state.Run{Name: "api", Group: other}}}, listener: -1},
		{name: "old generation listener", evidence: Evidence{Run: live}, listeners: []state.Port{{Group: &group, Run: &state.Run{Name: "api", ID: "old"}}}, running: true, run: true, listener: -1},
		{name: "exited generation listener", evidence: Evidence{LastExit: exit}, declaration: Declaration{Port: 8080}, listeners: []state.Port{{Group: &group, Port: 8080, Run: &state.Run{Name: "api", ID: exit.RunID}}}, listener: -1, lastExit: true},
		{name: "current descendant", evidence: Evidence{Run: live}, listeners: []state.Port{{Group: &group, PID: 99, Run: &state.Run{Name: "api", ID: live.ID, RootPID: live.PID}}}, running: true, run: true},
		{name: "auto port", declaration: Declaration{PortAuto: true}, evidence: Evidence{Run: live, PortHint: 9000}, listeners: []state.Port{{Group: &group, Port: 9000}}, running: true, run: true},
		{name: "hint does not override fixed declaration", declaration: Declaration{Port: 8080}, evidence: Evidence{Run: live, PortHint: 9000}, listeners: []state.Port{{Group: &group, Port: 9000}}, running: true, run: true, listener: -1},
		{name: "first eligible observation", declaration: Declaration{Port: 8080}, evidence: Evidence{Run: live}, listeners: []state.Port{
			{Group: &group, Port: 8080, Run: &state.Run{Name: "api", ID: "old"}},
			{Group: &group, Port: 9000, DisplayName: "api"},
			{Group: &group, Port: 8080, Run: &state.Run{Name: "api", ID: live.ID}},
		}, running: true, run: true, listener: 1},
		{name: "stopping without listener", evidence: Evidence{Run: live, Stopping: true}, listener: -1},
		{name: "stopping listener is still observed", evidence: Evidence{Run: live, Stopping: true}, listeners: []state.Port{{Group: &group, Run: &state.Run{Name: "api", ID: live.ID}}}, running: true},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			tt.declaration.Key = key
			snapshot := Snapshot{key: tt.evidence}
			got := Summarize([]Declaration{tt.declaration}, snapshot, tt.listeners)[0]
			if got.Running != tt.running || (got.Run != nil) != tt.run || (got.LastExit != nil) != tt.lastExit {
				t.Fatalf("result = %+v; want running=%v run=%v exit=%v", got, tt.running, tt.run, tt.lastExit)
			}
			if tt.listener < 0 {
				if got.Listener != nil {
					t.Fatalf("unexpected listener: %+v", got.Listener)
				}
			} else if got.Listener != &tt.listeners[tt.listener] {
				t.Fatalf("listener = %+v, want input %d", got.Listener, tt.listener)
			}
			if !reflect.DeepEqual(snapshot[key], tt.evidence) {
				t.Fatal("reducer changed registry evidence")
			}
		})
	}
}

func TestSummarizeScopedIndexMatchesExhaustiveJoin(t *testing.T) {
	random := rand.New(rand.NewSource(2))
	for iteration := 0; iteration < 100; iteration++ {
		var declarations []Declaration
		facts := Snapshot{}
		for g := 0; g < 3; g++ {
			for s := 0; s < 10; s++ {
				key := Key{fmt.Sprintf("repo@%d", g), fmt.Sprintf("svc-%d", s)}
				declarations = append(declarations, Declaration{Key: key, Port: 8000 + random.Intn(5), PortAuto: s%2 == 0})
				facts[key] = Evidence{PortHint: 9000 + random.Intn(5)}
			}
		}
		var listeners []state.Port
		for p := 0; p < 60; p++ {
			group := fmt.Sprintf("repo@%d", random.Intn(4))
			port := 8000 + random.Intn(5)
			if p%2 == 0 {
				port += 1000
			}
			listeners = append(listeners, state.Port{Group: &group, Port: port, DisplayName: fmt.Sprintf("svc-%d", random.Intn(12))})
		}
		before := append([]state.Port(nil), listeners...)
		got := Summarize(declarations, facts, listeners)
		for i, declaration := range declarations {
			var want *state.Port
			for p := range listeners {
				row := &listeners[p]
				if *row.Group == declaration.Key.Group && (row.Port == declaration.Port ||
					(declaration.PortAuto && row.Port == facts[declaration.Key].PortHint) || row.DisplayName == declaration.Key.Service) {
					want = row
					break
				}
			}
			if got[i].Listener != want || got[i].Running != (want != nil) || got[i].Run != nil {
				t.Fatalf("iteration %d service %+v: got %+v, want listener %+v", iteration, declaration.Key, got[i], want)
			}
		}
		if !reflect.DeepEqual(before, listeners) {
			t.Fatal("reducer changed input listeners")
		}
	}
}
