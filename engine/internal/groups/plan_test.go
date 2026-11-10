package groups

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func planConfig() *Config {
	return &Config{
		Path: "/tmp/demo/.oberth.yaml",
		Dir:  "/tmp/demo",
		Name: "demo",
		Services: []Service{
			{Name: "frontend", Cmd: "npm run dev", Port: 5173, DependsOn: []string{"api"}},
			{Name: "api", Cmd: "uv run api", Port: 8000, DependsOn: []string{"db", "cache"}},
			{Name: "db", Cmd: "postgres", Port: 5432},
			{Name: "cache", Cmd: "redis-server"},
		},
	}
}

func planNames(steps []Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Service.Name)
	}
	return out
}

func planSvc(cfg *Config, name string) Service {
	for _, s := range cfg.Services {
		if s.Name == name {
			return s
		}
	}
	panic("no service " + name)
}

func planStep(cfg *Config, name string, waitNames []string, included []string) Step {
	var waits []Service
	for _, w := range waitNames {
		waits = append(waits, planSvc(cfg, w))
	}
	return Step{Service: planSvc(cfg, name), Waits: waits, IncludedBy: included}
}

func requirePlan(t *testing.T, want ...Step) func([]Step, error) {
	t.Helper()
	return func(got []Step, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("plan = %#v, want %#v (names %v, want %v)", got, want, planNames(got), planNames(want))
		}
	}
}

func requireUnknownService(t *testing.T, err error, name, path string, known []string) {
	t.Helper()
	var unknown *UnknownServiceError
	if !errors.As(err, &unknown) {
		t.Fatalf("got %T %v, want *UnknownServiceError", err, err)
	}
	want := &UnknownServiceError{Path: path, Name: name, Known: known}
	if !reflect.DeepEqual(unknown, want) {
		t.Errorf("UnknownServiceError = %#v, want %#v", unknown, want)
	}
	if err.Error() != want.Error() {
		t.Errorf("Error() = %q, want %q", err.Error(), want.Error())
	}
}

func requireUnknownAP(t *testing.T, err error) {
	t.Helper()
	cfg := planConfig()
	requireUnknownService(t, err, "ap", cfg.Path, []string{"frontend", "api", "db", "cache"})
}

// TestPlanOrdersDependenciesFirst: a service never starts before something it
// declares depends_on.
func TestPlanOrdersDependenciesFirst(t *testing.T) {
	cfg := planConfig()
	requirePlan(t,
		planStep(cfg, "db", nil, nil),
		planStep(cfg, "cache", nil, nil),
		planStep(cfg, "api", []string{"db", "cache"}, nil),
		planStep(cfg, "frontend", []string{"api"}, nil),
	)(Plan(cfg, nil))
}

// TestPlanWaitsOnEveryDependency: ported and no-port dependencies both need
// readiness checks; groups.start uses a listener for the former and the run
// registry for the latter.
func TestPlanWaitsOnEveryDependency(t *testing.T) {
	cfg := planConfig()
	requirePlan(t,
		planStep(cfg, "db", nil, nil),
		planStep(cfg, "cache", nil, nil),
		planStep(cfg, "api", []string{"db", "cache"}, nil),
		planStep(cfg, "frontend", []string{"api"}, nil),
	)(Plan(cfg, nil))
}

// TestPlanWaitsOnAutoPorts: a `port: auto` dependency has a port to wait for,
// even though the file names no number.
func TestPlanWaitsOnAutoPorts(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "db", Cmd: "postgres", PortAuto: true},
		{Name: "api", Cmd: "uv run api", PortAuto: true, DependsOn: []string{"db"}},
	}}
	requirePlan(t,
		planStep(cfg, "db", nil, nil),
		planStep(cfg, "api", []string{"db"}, nil),
	)(Plan(cfg, nil))
}

// TestPlanOnlyFilters keeps the dependency order among what is left and does
// not drag unnamed services in.
func TestPlanOnlyFilters(t *testing.T) {
	cfg := planConfig()
	// frontend still declares its wait: api may already be up, and
	// `groups.start` decides what to do about it.
	requirePlan(t,
		planStep(cfg, "db", nil, nil),
		planStep(cfg, "frontend", []string{"api"}, nil),
	)(Plan(cfg, []string{"frontend", "db"}))
	requirePlan(t,
		planStep(cfg, "api", []string{"db", "cache"}, nil),
	)(Plan(cfg, []string{"api"}))
}

// TestPlanUnknownOnly reports a typo rather than starting nothing.
func TestPlanUnknownOnly(t *testing.T) {
	_, err := Plan(planConfig(), []string{"ap"})
	requireUnknownAP(t, err)
}

// TestPlanKeepsFileOrderAtTheSameDepth: two independent services start in the
// order the author wrote them.
func TestPlanKeepsFileOrderAtTheSameDepth(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "one"}, {Name: "two"}, {Name: "three"},
	}}
	requirePlan(t,
		planStep(cfg, "one", nil, nil),
		planStep(cfg, "two", nil, nil),
		planStep(cfg, "three", nil, nil),
	)(Plan(cfg, nil))
}

// TestPlanKeepsDependencyDepthBeforeLaterFileOrder pins the exact walk the
// planner uses: a service is emitted only after its dependencies, even when an
// independent service sits earlier in the file. A breadth-first "ready set"
// sort would emit worker before db here.
func TestPlanKeepsDependencyDepthBeforeLaterFileOrder(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "worker"},
		{Name: "db"},
	}}
	requirePlan(t,
		planStep(cfg, "db", nil, nil),
		planStep(cfg, "api", []string{"db"}, nil),
		planStep(cfg, "worker", nil, nil),
	)(Plan(cfg, nil))
}

// TestPlanWithIncludesTransitiveDependencies: --only web brings api and db too,
// and says which named root put them in the plan.
func TestPlanWithIncludesTransitiveDependencies(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "web", DependsOn: []string{"api"}},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "db"},
	}}
	requirePlan(t,
		planStep(cfg, "db", nil, []string{"web"}),
		planStep(cfg, "api", []string{"db"}, []string{"web"}),
		planStep(cfg, "web", []string{"api"}, nil),
	)(PlanWith(cfg, PlanOptions{Only: []string{"web"}, IncludeDependencies: true}))
}

// TestPlanWithKeepsExplicitIdentity: a service both named and depended on is
// explicit and carries no IncludedBy, while its own dependency is still
// attributed to every named root that reaches it.
func TestPlanWithKeepsExplicitIdentity(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "web", DependsOn: []string{"api"}},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "db"},
	}}
	requirePlan(t,
		planStep(cfg, "db", nil, []string{"api", "web"}),
		planStep(cfg, "api", []string{"db"}, nil),
		planStep(cfg, "web", []string{"api"}, nil),
	)(PlanWith(cfg, PlanOptions{Only: []string{"web", "api"}, IncludeDependencies: true}))
}

// TestPlanWithDiamondCountsEachRootOnce: two paths from a named root to the
// same dependency still list that root once.
func TestPlanWithDiamondCountsEachRootOnce(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "web", DependsOn: []string{"api", "worker"}},
		{Name: "api", DependsOn: []string{"base"}},
		{Name: "worker", DependsOn: []string{"base"}},
		{Name: "base"},
	}}
	requirePlan(t,
		planStep(cfg, "base", nil, []string{"web"}),
		planStep(cfg, "api", []string{"base"}, []string{"web"}),
		planStep(cfg, "worker", []string{"base"}, []string{"web"}),
		planStep(cfg, "web", []string{"api", "worker"}, nil),
	)(PlanWith(cfg, PlanOptions{Only: []string{"web"}, IncludeDependencies: true}))
}

// TestPlanWithDuplicatesAreOneEntry: naming a service twice is not two starts.
func TestPlanWithDuplicatesAreOneEntry(t *testing.T) {
	cfg := planConfig()
	requirePlan(t,
		planStep(cfg, "db", nil, []string{"api"}),
		planStep(cfg, "cache", nil, []string{"api"}),
		planStep(cfg, "api", []string{"db", "cache"}, nil),
	)(PlanWith(cfg, PlanOptions{Only: []string{"api", " api ", "api"}, IncludeDependencies: true}))
}

// TestPlanWithUnknownOnlyStillFails: closure does not turn a typo into a
// silent partial start.
func TestPlanWithUnknownOnlyStillFails(t *testing.T) {
	_, err := PlanWith(planConfig(), PlanOptions{Only: []string{"ap"}, IncludeDependencies: true})
	requireUnknownAP(t, err)
}

// TestPlanWithoutClosureKeepsRestartsMeaning: Plan and PlanWith(false) keep the
// old only-services behaviour, which restart depends on.
func TestPlanWithoutClosureKeepsRestartsMeaning(t *testing.T) {
	cfg := planConfig()
	want := planStep(cfg, "api", []string{"db", "cache"}, nil)
	requirePlan(t, want)(Plan(cfg, []string{"api"}))
	requirePlan(t, want)(PlanWith(cfg, PlanOptions{Only: []string{"api"}}))
}

// TestPlanWithCyclicHandBuiltConfigTerminates: Load rejects cycles, but a
// config constructed in code must not make the closure walk spin.
func TestPlanWithCyclicHandBuiltConfigTerminates(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}}
	done := make(chan struct {
		steps []Step
		err   error
	}, 1)
	go func() {
		steps, err := PlanWith(cfg, PlanOptions{Only: []string{"a"}, IncludeDependencies: true})
		done <- struct {
			steps []Step
			err   error
		}{steps, err}
	}()
	select {
	case got := <-done:
		// The walk emits b (reached from a, IncludedBy [a]) then a (the named
		// root). Each still waits on the other. This is not file order.
		requirePlan(t,
			planStep(cfg, "b", []string{"a"}, []string{"a"}),
			planStep(cfg, "a", []string{"b"}, nil),
		)(got.steps, got.err)
	case <-time.After(time.Second):
		t.Fatal("PlanWith did not terminate on a cyclic hand-built config")
	}
}
