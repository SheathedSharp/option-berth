package groups

import (
	"errors"
	"strings"
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

// TestPlanOrdersDependenciesFirst: a service never starts before something it
// declares depends_on.
func TestPlanOrdersDependenciesFirst(t *testing.T) {
	steps, err := Plan(planConfig(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	got := planNames(steps)
	want := []string{"db", "cache", "api", "frontend"}
	if !equalStrings(got, want) {
		t.Fatalf("order = %v, want %v", got, want)
	}
}

// TestPlanWaitsOnEveryDependency: ported and no-port dependencies both need
// readiness checks; groups.start uses a listener for the former and the run
// registry for the latter.
func TestPlanWaitsOnEveryDependency(t *testing.T) {
	steps, err := Plan(planConfig(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, s := range steps {
		switch s.Service.Name {
		case "api":
			if len(s.Waits) != 2 || s.Waits[0].Name != "db" || s.Waits[1].Name != "cache" {
				t.Errorf("api waits = %+v, want db and cache", s.Waits)
			}
		case "frontend":
			if len(s.Waits) != 1 || s.Waits[0].Name != "api" {
				t.Errorf("frontend waits = %+v, want just api", s.Waits)
			}
		default:
			if len(s.Waits) != 0 {
				t.Errorf("%s waits = %+v, want none", s.Service.Name, s.Waits)
			}
		}
	}
}

// TestPlanWaitsOnAutoPorts: a `port: auto` dependency has a port to wait for,
// even though the file names no number.
func TestPlanWaitsOnAutoPorts(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "db", Cmd: "postgres", PortAuto: true},
		{Name: "api", Cmd: "uv run api", PortAuto: true, DependsOn: []string{"db"}},
	}}
	steps, err := Plan(cfg, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(steps) != 2 || len(steps[1].Waits) != 1 || steps[1].Waits[0].Name != "db" {
		t.Fatalf("steps = %+v, want api waiting on db", steps)
	}
}

// TestPlanOnlyFilters keeps the dependency order among what is left and does
// not drag unnamed services in.
func TestPlanOnlyFilters(t *testing.T) {
	steps, err := Plan(planConfig(), []string{"frontend", "db"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := planNames(steps); !equalStrings(got, []string{"db", "frontend"}) {
		t.Fatalf("order = %v, want [db frontend]", got)
	}
	// frontend still declares its wait: api may already be up, and
	// `groups.start` decides what to do about it.
	if len(steps[1].Waits) != 1 || steps[1].Waits[0].Name != "api" {
		t.Fatalf("frontend waits = %+v", steps[1].Waits)
	}

	steps, err = Plan(planConfig(), []string{"api"})
	if err != nil {
		t.Fatalf("Plan --only api: %v", err)
	}
	if len(steps) != 1 || len(steps[0].Waits) != 2 || steps[0].Waits[0].Name != "db" || steps[0].Waits[1].Name != "cache" {
		t.Fatalf("--only api waits = %+v, want db and cache", steps)
	}
}

// TestPlanUnknownOnly reports a typo rather than starting nothing.
func TestPlanUnknownOnly(t *testing.T) {
	_, err := Plan(planConfig(), []string{"ap"})
	var unknown *UnknownServiceError
	if !errors.As(err, &unknown) || unknown.Name != "ap" {
		t.Fatalf("expected an UnknownServiceError for ap, got %v", err)
	}
	if !strings.Contains(err.Error(), "api") {
		t.Errorf("the error should list the real service names: %v", err)
	}
}

// TestPlanKeepsFileOrderAtTheSameDepth: two independent services start in the
// order the author wrote them.
func TestPlanKeepsFileOrderAtTheSameDepth(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "one"}, {Name: "two"}, {Name: "three"},
	}}
	steps, err := Plan(cfg, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := planNames(steps); !equalStrings(got, []string{"one", "two", "three"}) {
		t.Fatalf("order = %v", got)
	}
}

// includedByOf reads one step's IncludedBy, or nil when the service is absent.
func includedByOf(steps []Step, name string) []string {
	for _, step := range steps {
		if step.Service.Name == name {
			return step.IncludedBy
		}
	}
	return nil
}

// TestPlanWithIncludesTransitiveDependencies: --only web brings api and db too,
// and says which named root put them in the plan.
func TestPlanWithIncludesTransitiveDependencies(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "web", DependsOn: []string{"api"}},
		{Name: "api", DependsOn: []string{"db"}},
		{Name: "db"},
	}}
	steps, err := PlanWith(cfg, PlanOptions{Only: []string{"web"}, IncludeDependencies: true})
	if err != nil {
		t.Fatalf("PlanWith: %v", err)
	}
	if got := planNames(steps); !equalStrings(got, []string{"db", "api", "web"}) {
		t.Fatalf("order = %v, want db api web", got)
	}
	if got := includedByOf(steps, "web"); got != nil {
		t.Errorf("web IncludedBy = %v, want nil for the named service", got)
	}
	for _, name := range []string{"api", "db"} {
		if got := includedByOf(steps, name); !equalStrings(got, []string{"web"}) {
			t.Errorf("%s IncludedBy = %v, want [web]", name, got)
		}
	}
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
	steps, err := PlanWith(cfg, PlanOptions{
		Only: []string{"web", "api"}, IncludeDependencies: true,
	})
	if err != nil {
		t.Fatalf("PlanWith: %v", err)
	}
	if got := planNames(steps); !equalStrings(got, []string{"db", "api", "web"}) {
		t.Fatalf("order = %v", got)
	}
	if got := includedByOf(steps, "api"); got != nil {
		t.Errorf("api IncludedBy = %v, want nil because it was named", got)
	}
	if got := includedByOf(steps, "db"); !equalStrings(got, []string{"api", "web"}) {
		t.Errorf("db IncludedBy = %v, want both named roots sorted", got)
	}
	// The named service still waits on what it declares.
	for _, step := range steps {
		if step.Service.Name == "web" && len(step.Waits) != 1 {
			t.Errorf("web waits = %+v, want api", step.Waits)
		}
	}
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
	steps, err := PlanWith(cfg, PlanOptions{Only: []string{"web"}, IncludeDependencies: true})
	if err != nil {
		t.Fatalf("PlanWith: %v", err)
	}
	if got := includedByOf(steps, "base"); !equalStrings(got, []string{"web"}) {
		t.Fatalf("base IncludedBy = %v, want [web] despite two paths", got)
	}
}

// TestPlanWithDuplicatesAreOneEntry: naming a service twice is not two starts.
func TestPlanWithDuplicatesAreOneEntry(t *testing.T) {
	steps, err := PlanWith(planConfig(), PlanOptions{
		Only: []string{"api", " api ", "api"}, IncludeDependencies: true,
	})
	if err != nil {
		t.Fatalf("PlanWith: %v", err)
	}
	if got := planNames(steps); !equalStrings(got, []string{"db", "cache", "api"}) {
		t.Fatalf("order = %v", got)
	}
}

// TestPlanWithUnknownOnlyStillFails: closure does not turn a typo into a
// silent partial start.
func TestPlanWithUnknownOnlyStillFails(t *testing.T) {
	_, err := PlanWith(planConfig(), PlanOptions{Only: []string{"ap"}, IncludeDependencies: true})
	var unknown *UnknownServiceError
	if !errors.As(err, &unknown) || unknown.Name != "ap" {
		t.Fatalf("expected an UnknownServiceError for ap, got %v", err)
	}
}

// TestPlanWithoutClosureKeepsRestartsMeaning: Plan and PlanWith(false) keep the
// old only-services behaviour, which restart depends on.
func TestPlanWithoutClosureKeepsRestartsMeaning(t *testing.T) {
	for name, steps := range map[string][]Step{
		"Plan": func() []Step {
			steps, err := Plan(planConfig(), []string{"api"})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			return steps
		}(),
		"PlanWith": func() []Step {
			steps, err := PlanWith(planConfig(), PlanOptions{Only: []string{"api"}})
			if err != nil {
				t.Fatalf("PlanWith: %v", err)
			}
			return steps
		}(),
	} {
		if got := planNames(steps); !equalStrings(got, []string{"api"}) {
			t.Errorf("%s kept %v, want only api", name, got)
		}
		if got := includedByOf(steps, "api"); got != nil {
			t.Errorf("%s api IncludedBy = %v, want nil", name, got)
		}
	}
}

// TestPlanWithCyclicHandBuiltConfigTerminates: Load rejects cycles, but a
// config constructed in code must not make the closure walk spin.
func TestPlanWithCyclicHandBuiltConfigTerminates(t *testing.T) {
	cfg := &Config{Name: "demo", Services: []Service{
		{Name: "a", DependsOn: []string{"b"}},
		{Name: "b", DependsOn: []string{"a"}},
	}}
	done := make(chan []Step, 1)
	go func() {
		steps, err := PlanWith(cfg, PlanOptions{Only: []string{"a"}, IncludeDependencies: true})
		if err != nil {
			t.Errorf("PlanWith: %v", err)
		}
		done <- steps
	}()
	select {
	case steps := <-done:
		if len(steps) != 2 {
			t.Fatalf("steps = %v, want both services once", planNames(steps))
		}
	case <-time.After(time.Second):
		t.Fatal("PlanWith did not terminate on a cyclic hand-built config")
	}
}
