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
	codeChanged, codeFiles, codeHint := true, 2, "oberth restart --only api"
	doc.Worktree.Services[0].CodeChangedSinceStart = &codeChanged
	doc.Worktree.Services[0].CodeChangedFiles = &codeFiles
	doc.Worktree.Services[0].CodeHint = &codeHint
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
		`"code_changed_since_start":true`, `"code_changed_files":2`,
		`"code_hint":"oberth restart --only api"`,
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
	// Unknown is absent, not false: only the one checked service carries the
	// field.
	if got := strings.Count(text, `"code_changed_since_start"`); got != 1 {
		t.Fatalf("code_changed_since_start appears %d times, want only the checked service: %s", got, text)
	}
}

// TestBriefStatusCarriesStarting: `--brief --json` is a reader's document for a
// project that may be mid-start, so the in-flight attempt must be there with
// the same facts the full document publishes.
func TestBriefStatusCarriesStarting(t *testing.T) {
	lastPrepare := int64(800)
	doc := statusScopeFixture()
	doc.Worktree.Services[1].Starting = &state.ServiceStarting{
		Phase:         "waiting_dependency",
		StartedAt:     "2026-10-31T10:00:00Z",
		ElapsedMS:     130000,
		ReadyTimeout:  "3m0s",
		WaitingOn:     []string{"db", "cache"},
		LastPrepareMS: &lastPrepare,
	}
	doc.Worktree.Services[2].Starting = &state.ServiceStarting{
		Phase: "prepare", StartedAt: "2026-10-31T10:00:00Z", ElapsedMS: 10, ReadyTimeout: "30s",
	}

	brief := briefStatusFrom(doc)
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, present := range []string{
		`"starting":{"phase":"waiting_dependency","started_at":"2026-10-31T10:00:00Z",` +
			`"elapsed_ms":130000,"ready_timeout":"3m0s","waiting_on":["db","cache"],"last_prepare_ms":800}`,
		`"phase":"prepare"`,
	} {
		if !strings.Contains(text, present) {
			t.Errorf("brief document is missing %s: %s", present, text)
		}
	}
	// Only the services actually starting carry the field.
	if got := strings.Count(text, `"starting":`); got != 2 {
		t.Fatalf("starting appears %d times, want only the two in-flight services: %s", got, text)
	}

	// The projection copies: mutating the snapshot must not change the brief.
	doc.Worktree.Services[1].Starting.WaitingOn[0] = "mutated"
	if got := brief.Worktree.Services[1].Starting.WaitingOn[0]; got != "db" {
		t.Fatalf("waiting_on aliased the snapshot slice: %q", got)
	}
	*doc.Worktree.Services[1].Starting.LastPrepareMS = 1
	if got := *brief.Worktree.Services[1].Starting.LastPrepareMS; got != 800 {
		t.Fatalf("last_prepare_ms aliased the snapshot pointer: %d", got)
	}
	// And the other way: the brief's own slice is not the snapshot's.
	brief.Worktree.Services[1].Starting.WaitingOn[1] = "changed"
	if got := doc.Worktree.Services[1].Starting.WaitingOn[1]; got != "cache" {
		t.Fatalf("the brief slice aliased the snapshot: %q", got)
	}
}

// TestServiceStateTextStartingLine: the human row says how long the attempt has
// been going and what budget it is measured against, and names the waiting
// dependencies only when there are some.
func TestServiceStateTextStartingLine(t *testing.T) {
	noColor(t)
	for _, tc := range []struct {
		name     string
		starting state.ServiceStarting
		want     string
	}{
		{
			name: "prepare with elapsed and waiting",
			starting: state.ServiceStarting{
				Phase: "prepare", ElapsedMS: 130000, ReadyTimeout: "3m0s",
				WaitingOn: []string{"db", "cache"},
			},
			want: "starting · prepare · for 2m10s · budget 3m0s · waiting on db, cache",
		},
		{
			name:     "no waiting_on",
			starting: state.ServiceStarting{Phase: "spawning", ElapsedMS: 600, ReadyTimeout: "30s"},
			want:     "starting · spawning · for 1s · budget 30s",
		},
		{
			name:     "no elapsed",
			starting: state.ServiceStarting{Phase: "waiting_dependency", ReadyTimeout: "30s", WaitingOn: []string{"db"}},
			want:     "starting · waiting_dependency · budget 30s · waiting on db",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := state.Service{Name: "api", Starting: &tc.starting}
			if got := serviceStateText(svc); got != tc.want {
				t.Fatalf("state text = %q, want %q", got, tc.want)
			}
		})
	}
}

// A service that is not starting keeps the old word: the extra parts are the
// attempt's, not the column's.
func TestServiceStateTextUnchangedWithoutStarting(t *testing.T) {
	noColor(t)
	if got := serviceStateText(state.Service{Name: "api"}); got != "not running" {
		t.Fatalf("idle text = %q, want not running", got)
	}
	if got := serviceStateText(state.Service{Name: "api", Running: true}); got != "running" {
		t.Fatalf("running text = %q, want running", got)
	}
}

func TestBriefStatusHumanShowsStartingLine(t *testing.T) {
	noColor(t)
	doc := statusScopeFixture()
	doc.Worktree.Services[1].Starting = &state.ServiceStarting{
		Phase: "waiting_dependency", ElapsedMS: 130000, ReadyTimeout: "3m0s",
		WaitingOn: []string{"db", "cache"},
	}
	out := captureStdout(t, func() { printStatusBrief(doc) })
	want := "starting · waiting_dependency · for 2m10s · budget 3m0s · waiting on db, cache"
	if !strings.Contains(out, want) {
		t.Fatalf("brief output is missing %q:\n%s", want, out)
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

// noColor pins plain text so a state word can be compared exactly.
func noColor(t *testing.T) {
	t.Helper()
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })
}

func intsText(values []int) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, strconv.Itoa(value))
	}
	return strings.Join(parts, ",")
}
