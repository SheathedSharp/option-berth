package cmd

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/draft"
	"github.com/sheathedsharp/option-berth/engine/internal/git"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func statusScopeFixture() statusDocument {
	apiPort, apiActual := 3000, 3001
	dbPort, webPort := 5432, 8080
	crashed := 2
	return statusDocument{
		Scope: display.Scope{Kind: "worktree", Name: "shop", Root: "/work/shop"},
		Worktree: state.Group{
			Name: "shop", Identity: "wt_0123456789ab", Branch: "main",
			Services: []state.Service{
				{Name: "api", Port: &apiPort, PortActual: &apiActual, Running: true, DependsOn: []string{"worker"}},
				{Name: "worker", DependsOn: []string{"db"}},
				{Name: "db", Port: &dbPort, LastExit: &state.ServiceExit{Reason: "crashed", Code: crashed, RunID: "db-run"}},
				{Name: "web", Port: &webPort, Running: true},
			},
			Machine: []state.MachineRef{{Name: "redis", Port: 6379}},
		},
		Ports: []state.Port{
			{Port: apiPort}, {Port: apiActual}, {Port: dbPort}, {Port: webPort}, {Port: 9999},
		},
		Exits: []rpc.RunRecord{
			{ID: "api-run", Name: "api", Reason: "running"},
			{ID: "db-run", Name: "db", Reason: "crashed", ExitCode: &crashed, Hint: "oberth logs db --once"},
			{ID: "web-run", Name: "web", Reason: "running"},
		},
		Changed: &statusChange{
			At:              "2026-10-31T10:00:00Z",
			ServicesStarted: []string{"api", "web"},
			ServicesStopped: []string{"worker", "db"},
			PortsOpened:     []int{apiPort, 9999},
			PortsClosed:     []int{dbPort, webPort},
			Empty:           false,
		},
	}
}

func TestStatusOnlyKeepsServicesAndDependencyClosure(t *testing.T) {
	doc := statusScopeFixture()
	if err := scopeStatusDocument(&doc, []string{"api"}); err != nil {
		t.Fatal(err)
	}

	names := make([]string, 0, len(doc.Worktree.Services))
	for _, svc := range doc.Worktree.Services {
		names = append(names, svc.Name)
	}
	if got, want := strings.Join(names, ","), "api,worker,db"; got != want {
		t.Fatalf("services = %q, want %q", got, want)
	}

	ports := make([]int, 0, len(doc.Ports))
	for _, port := range doc.Ports {
		ports = append(ports, port.Port)
	}
	if got, want := intsText(ports), "3000,3001,5432"; got != want {
		t.Fatalf("ports = %s, want %s", got, want)
	}

	exits := make([]string, 0, len(doc.Exits))
	for _, run := range doc.Exits {
		exits = append(exits, run.Name)
	}
	if got, want := strings.Join(exits, ","), "api,db"; got != want {
		t.Fatalf("exits = %q, want %q", got, want)
	}

	change := doc.Changed
	if got, want := strings.Join(change.ServicesStarted, ","), "api"; got != want {
		t.Fatalf("services_started = %q, want %q", got, want)
	}
	if got, want := strings.Join(change.ServicesStopped, ","), "worker,db"; got != want {
		t.Fatalf("services_stopped = %q, want %q", got, want)
	}
	if got, want := intsText(change.PortsOpened), "3000"; got != want {
		t.Fatalf("ports_opened = %s, want %s", got, want)
	}
	if got, want := intsText(change.PortsClosed), "5432"; got != want {
		t.Fatalf("ports_closed = %s, want %s", got, want)
	}
	if change.Empty {
		t.Fatal("a scoped change with selected facts reports itself empty")
	}
}

func TestStatusOnlyRejectsAnUnknownService(t *testing.T) {
	doc := statusScopeFixture()
	err := scopeStatusDocument(&doc, []string{"api", "nosuch"})
	code, message, hint := describe(err)
	if code != "not_found" || !strings.Contains(message, "nosuch") {
		t.Fatalf("error = %q / %q, want not_found naming the unknown service", code, message)
	}
	if !strings.Contains(hint, "api") || !strings.Contains(hint, "web") {
		t.Fatalf("hint = %q, want the declared service names", hint)
	}
}

func TestStatusOnlyRejectsAnEmptyName(t *testing.T) {
	doc := statusScopeFixture()
	err := scopeStatusDocument(&doc, []string{" "})
	if got := exitCodeFor(err); got != exitUsage {
		t.Fatalf("exit code = %d, want usage %d", got, exitUsage)
	}
}

func TestStatusOnlyTrimsAndDeduplicatesNames(t *testing.T) {
	doc := statusScopeFixture()
	if err := scopeStatusDocument(&doc, []string{" api ", "api"}); err != nil {
		t.Fatal(err)
	}
	if len(doc.Worktree.Services) != 3 {
		t.Fatalf("services = %+v, want api and its two dependencies", doc.Worktree.Services)
	}
}

func TestBriefStatusKeepsOnlyRuntimeFacts(t *testing.T) {
	doc := statusScopeFixture()
	doc.Worktree.Services[0].HealthStatus = &state.Health{Status: "ok", Code: 200}
	doc.Worktree.Services[0].ManifestRuntimeMismatch = true
	doc.Worktree.Services[1].LastExit = &state.ServiceExit{Reason: "crashed", Code: 2, RunID: "worker-run"}
	doc.Exits = append([]rpc.RunRecord{{
		ID: "worker-run", Name: "worker", Reason: "crashed", ExitCode: intPtr(2),
		Hint: "oberth logs worker --once", ExitedAt: "2026-10-31T09:00:00Z",
	}}, doc.Exits...)
	doc.Git = &git.Tree{Snapshot: git.Snapshot{Staged: 3}}
	doc.Drafts = append(doc.Drafts, draft.Pending{Group: "shop", Agent: "claude"})

	doc.Worktree.Services[0].Cmd = "npm run dev"
	doc.Worktree.Services[0].Cwd = "/work/shop"
	doc.Worktree.Services[0].ManifestHash = strPtrOrNil("mh")
	doc.Worktree.Services[0].PID = intPtr(4242)

	brief := briefStatusFrom(doc)
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)

	for _, absent := range []string{
		`"cmd"`, `"cwd"`, `"manifest_hash"`, `"last_exit"`, `"pid"`, `"log_path"`,
		`"git"`, `"changed"`, `"drafts"`, `"attention_path"`,
	} {
		if strings.Contains(text, absent) {
			t.Errorf("brief document contains %s: %s", absent, text)
		}
	}
	for _, present := range []string{
		`"name":"api"`, `"running":true`, `"port":3000`, `"port_actual":3001`,
		`"health":{"status":"ok","code":200}`, `"manifest_runtime_mismatch":true`,
		`"exit_reason":"crashed"`, `"exit_code":2`, `"hint":"oberth logs worker --once"`,
		`"machine":[{"name":"redis","port":6379,"listening":false}]`,
		`"identity":"wt_0123456789ab"`,
	} {
		if !strings.Contains(text, present) {
			t.Errorf("brief document is missing %s: %s", present, text)
		}
	}
	if brief.Ports == nil || brief.Exits == nil || brief.Worktree.Services == nil {
		t.Fatalf("brief arrays must not be null: %+v", brief)
	}
}

func TestBriefStatusHumanSkipsRepositoryAndChange(t *testing.T) {
	doc := statusScopeFixture()
	doc.Git = &git.Tree{Snapshot: git.Snapshot{Staged: 3}}
	doc.Drafts = append(doc.Drafts, draft.Pending{Group: "shop", Agent: "claude"})

	out := captureStdout(t, func() { printStatusBrief(doc) })
	for _, absent := range []string{"last look", "staged", "drafts", "first look"} {
		if strings.Contains(out, absent) {
			t.Errorf("brief output contains %q:\n%s", absent, out)
		}
	}
	for _, present := range []string{"api", "worker", "db", "depends on"} {
		if !strings.Contains(out, present) {
			t.Errorf("brief output is missing %q:\n%s", present, out)
		}
	}
}

func intsText(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
