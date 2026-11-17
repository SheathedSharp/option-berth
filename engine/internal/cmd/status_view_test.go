package cmd

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/draft"
	"github.com/sheathedsharp/option-berth/engine/internal/git"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func statusScopeFixture() statusDocument {
	apiPort, apiActual := 3000, 3001
	dbPort, webPort := 5432, 8080
	apiRun := "api-run"
	crashed := 2
	return statusDocument{
		Scope: display.Scope{Kind: "worktree", Name: "shop", Root: "/work/shop"},
		Worktree: state.Group{
			Name: "shop", Identity: "wt_0123456789ab", Branch: "main",
			Services: []state.Service{
				{Name: "api", Port: &apiPort, PortActual: &apiActual, Running: true, RunID: &apiRun,
					DependsOn: []string{"worker"}, Stats: &state.Stats{CPUPercent: 2, MemoryRSS: 20}},
				{Name: "worker", DependsOn: []string{"db"}},
				{Name: "db", Port: &dbPort, LastExit: &state.ServiceExit{Reason: "crashed", Code: crashed, RunID: "db-run"}},
				{Name: "web", Port: &webPort, Running: true,
					Stats: &state.Stats{CPUPercent: 5, MemoryRSS: 50}},
			},
			Machine: []state.MachineRef{{Name: "redis", Port: 6379}},
		},
		Ports: []state.Port{
			{Port: apiPort}, {Port: apiActual, Run: &state.Run{ID: apiRun, Group: "shop", Name: "api"}},
			{Port: dbPort}, {Port: webPort}, {Port: 9999},
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

	if doc.Worktree.RunningServices != 1 {
		t.Fatalf("running_services = %d, want only the selected api", doc.Worktree.RunningServices)
	}
	res := doc.Worktree.Resources
	if res == nil || res.Services != 1 || res.CPUPercent != 2 || res.MemoryRSS != 20 {
		t.Fatalf("resources = %+v, want only api's sample", res)
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
		`"git"`, `"changed"`, `"drafts"`, `"attn_path"`,
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
		`"machine":[{"name":"redis","port":6379,"listening":false,"listening_known":false}]`,
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

// TestBriefStatusMarksAListenerNotStartedByOberth: `running` says a listener
// exists, not that option-berth owns it. When the run record is gone and a
// foreign process holds the declared port, the brief document must name that
// process; otherwise a restart reads as if it acted on a run option-berth owns.
func TestBriefStatusMarksAListenerNotStartedByOberth(t *testing.T) {
	doc := statusScopeFixture()
	// api's own run is gone; the listener on its actual port remains.
	doc.Worktree.Services[0].RunID = nil
	doc.Ports[1] = state.Port{
		Port: 3001, PID: 20792, Command: "python3 -u svc.py 3001",
		Group: strPtrOrNil("shop"),
	}

	brief := briefStatusFrom(doc)
	svc := brief.Worktree.Services[0]
	if svc.PortHolder == nil {
		t.Fatal("brief service has no holder for a listener owned by another process")
	}
	if svc.PortHolder.Kind != state.PortHolderListener ||
		svc.PortHolder.PID != 20792 || svc.PortHolder.Port != 3001 {
		t.Fatalf("holder = %+v, want the foreign listener on 3001", svc.PortHolder)
	}
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"port_holder":{"port":3001,`) {
		t.Fatalf("brief = %s, want the holder on the running service", raw)
	}

	// A managed listener is not a holder; the fixture's api run owns 3001.
	if holder := briefStatusFrom(statusScopeFixture()).Worktree.Services[0].PortHolder; holder != nil {
		t.Fatalf("managed listener reported holder %+v", holder)
	}
}

// TestBriefStatusNamesAnotherRunsListener: a listener attributed to a run of
// another worktree is still not this service's run. The holder's kind and
// run identity say which run it is, so a reader does not have to guess from a
// bare port number.
func TestBriefStatusNamesAnotherRunsListener(t *testing.T) {
	for _, tc := range []struct {
		name  string
		run   *state.Run
		kind  state.PortHolderKind
		group string
	}{
		{"another worktree", &state.Run{ID: "other-run", Group: "shop@other", Name: "api"},
			state.PortHolderRun, "shop@other"},
		{"an earlier generation", &state.Run{ID: "old-run", Group: "shop", Name: "api"},
			state.PortHolderPrevious, "shop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc := statusScopeFixture()
			doc.Worktree.Services[0].RunID = nil
			doc.Ports[1].Run = tc.run
			doc.Ports[1].PID = 20792

			holder := briefStatusFrom(doc).Worktree.Services[0].PortHolder
			if holder == nil || holder.Kind != tc.kind ||
				holder.Group != tc.group || holder.RunID != tc.run.ID {
				t.Fatalf("holder = %+v, want %s run %s", holder, tc.kind, tc.run.ID)
			}
		})
	}
}

// The executable facts are runtime facts: a brief reader must see the same
// artifact verdict the full document publishes.
func TestBriefStatusCarriesTheExecutableFacts(t *testing.T) {
	doc := statusScopeFixture()
	changed := true
	doc.Worktree.Services[0].ExecutablePath = strPtrOrNil("/work/shop/bin/api")
	doc.Worktree.Services[0].ExecutableModifiedAt = strPtrOrNil("2026-11-13T10:02:00Z")
	doc.Worktree.Services[0].ExecutableChangedSinceStart = &changed

	brief := briefStatusFrom(doc)
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"executable_path":"/work/shop/bin/api"`,
		`"executable_modified_at":"2026-11-13T10:02:00Z"`,
		`"executable_changed_since_start":true`,
	} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("brief document is missing %s: %s", want, raw)
		}
	}
}

// TestBriefStatusCarriesPendingHealth: the brief document is what an agent
// reads in the window between run registration and the first health probe.
// `starting` is already gone by then, so without the pending verdict a running
// service would carry no health at all and "not probed yet" would be
// indistinguishable from "probe unavailable".
func TestBriefStatusCarriesPendingHealth(t *testing.T) {
	doc := statusScopeFixture()
	doc.Worktree.Services[0].HealthStatus = &state.Health{Status: state.HealthUnknown, Reason: "pending"}

	brief := briefStatusFrom(doc)
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"health":{"status":"unknown","reason":"pending"}`) {
		t.Fatalf("brief document lost the pending health: %s", raw)
	}
}

// Every listener must have one home: a running service's row, an owning run,
// or unowned. Attribution follows run identity, not the service name: a stale
// generation that still calls itself `api` is not the running `api`'s run.
func TestBriefStatusAttributesEveryListener(t *testing.T) {
	doc := statusScopeFixture()
	// Port 3000 is the declared api port, but this is an older api generation,
	// not api's current run on 3001.
	doc.Ports[0].Run = &state.Run{ID: "api-old", Group: "shop", Name: "api", RootPID: 42}
	// One ad-hoc run with two listeners.
	doc.Ports[4].Run = &state.Run{ID: "adhoc-run", Group: "shop", Name: "finance-web", RootPID: 64530}
	doc.Ports = append(doc.Ports, state.Port{Port: 9998,
		Run: &state.Run{ID: "adhoc-run", Group: "shop", Name: "finance-web", RootPID: 64530}})

	brief := briefStatusFrom(doc)
	if len(brief.Worktree.AdHocRuns) != 2 {
		t.Fatalf("run attribution = %+v, want the stale api generation and the ad-hoc run", brief.Worktree.AdHocRuns)
	}
	byRun := make(map[string]briefAdHocRun, len(brief.Worktree.AdHocRuns))
	for _, run := range brief.Worktree.AdHocRuns {
		byRun[run.RunID] = run
	}
	stale, ok := byRun["api-old"]
	if !ok || stale.Name != "api" || intsText(stale.Ports) != "3000" {
		t.Fatalf("stale generation = %+v, want api on 3000", stale)
	}
	adHoc, ok := byRun["adhoc-run"]
	if !ok || adHoc.Name != "finance-web" || adHoc.Group != "shop" || adHoc.PID != 64530 {
		t.Fatalf("ad-hoc run = %+v", adHoc)
	}
	if got, want := intsText(adHoc.Ports), "9998,9999"; got != want {
		t.Fatalf("ad hoc ports = %s, want %s", got, want)
	}
	if len(brief.Worktree.UnownedPorts) != 2 ||
		brief.Worktree.UnownedPorts[0].Port != 5432 || brief.Worktree.UnownedPorts[1].Port != 8080 {
		t.Fatalf("unowned ports = %+v, want 5432 and 8080", brief.Worktree.UnownedPorts)
	}
	// The flat list stays for compatibility.
	if got, want := intsText(brief.Ports), "3000,3001,5432,8080,9999,9998"; got != want {
		t.Fatalf("ports = %s, want %s", got, want)
	}
	raw, err := json.Marshal(brief)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"unowned_ports":[{"port":5432},{"port":8080}]`) {
		t.Fatalf("brief = %s, want every unowned listener marked", raw)
	}
}

// The run's recorded start line is on the run record, not in the listener row.
// The brief joins by pid and generation and publishes it, so an agent can
// rebuild the run without first triggering a refusal to make a hint print.
func TestBriefStatusCarriesTheRecordedRunCommand(t *testing.T) {
	doc := statusScopeFixture()
	doc.Ports[4].Run = &state.Run{ID: "adhoc-run", Group: "shop", Name: "finance-web", RootPID: 64530}
	doc.Runs = []rpc.RunRecord{{
		ID: "adhoc-run", PID: 64530, Group: "shop", Name: "finance-web",
		Cmd: "yarn dev", Cwd: "/work/shop/web", StartedAt: "2026-11-16T09:00:00Z", Status: "running",
	}}

	brief := briefStatusFrom(doc)
	var run *briefAdHocRun
	for i := range brief.Worktree.AdHocRuns {
		if brief.Worktree.AdHocRuns[i].RunID == "adhoc-run" {
			run = &brief.Worktree.AdHocRuns[i]
		}
	}
	if run == nil {
		t.Fatalf("run attribution = %+v", brief.Worktree.AdHocRuns)
	}
	if run.Cmd != "yarn dev" || run.Cwd != "/work/shop/web" || run.StartedAt != "2026-11-16T09:00:00Z" {
		t.Fatalf("recorded run facts = %+v", *run)
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
		// A service with nothing outstanding still publishes an empty array and
		// a null duration, the same shape the full document uses.
		`"starting":{"phase":"prepare","started_at":"2026-10-31T10:00:00Z",` +
			`"elapsed_ms":10,"ready_timeout":"30s","waiting_on":[],"last_prepare_ms":null}`,
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
			name:     "sub-second attempt does not read as zero",
			starting: state.ServiceStarting{Phase: "spawning", ElapsedMS: 200, ReadyTimeout: "30s"},
			want:     "starting · spawning · for <1s · budget 30s",
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

func TestStatusShowsServiceAndWorktreeResources(t *testing.T) {
	doc := statusScopeFixture()
	doc.Worktree.Services[0].Stats = &state.Stats{CPUPercent: 12.5, MemoryRSS: 2 << 20}
	doc.Worktree.Services[1].Stats = &state.Stats{CPUPercent: 7, MemoryRSS: 3 << 20}
	doc.Worktree.RunningServices = 2
	doc.Worktree.Resources = &state.Resources{CPUPercent: 19.5, MemoryRSS: 5 << 20, Services: 2}

	out := captureStdout(t, func() { printStatusBrief(doc) })
	for _, want := range []string{"cpu 12.5%", "mem 2.0M", "2 declared running", "cpu 19.5%", "mem 5.0M"} {
		if !strings.Contains(out, want) {
			t.Errorf("resource output is missing %q:\n%s", want, out)
		}
	}

	brief := briefStatusFrom(doc)
	if brief.Worktree.RunningServices != 2 || brief.Worktree.Resources == nil ||
		brief.Worktree.Services[0].Stats == nil || brief.Worktree.Services[0].Stats.MemoryRSS != 2<<20 {
		t.Fatalf("brief resources = %+v", brief.Worktree)
	}
}

func TestStatusResourcesKeepUnmeasuredApartFromZero(t *testing.T) {
	doc := statusScopeFixture()
	doc.Worktree.RunningServices = 1
	doc.Worktree.Resources = nil

	out := captureStdout(t, func() { printStatusBrief(doc) })
	if !strings.Contains(out, "1 declared running") {
		t.Fatalf("output = %q, want the running count", out)
	}
	if strings.Contains(out, "cpu 0.0%") || strings.Contains(out, "mem 0B") {
		t.Fatalf("output = %q, want no measured zero for an unsampled service", out)
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

// The listener line is the human half of the brief attribution: a port is
// named by the run that owns it, or marked unowned with the process behind it.
func TestStatusListenersNameTheRunOrMarkUnowned(t *testing.T) {
	noColor(t)
	doc := statusScopeFixture()
	doc.Ports[4].Run = &state.Run{ID: "adhoc-run", Group: "shop", Name: "finance-web", RootPID: 64530}
	doc.Ports[4].PID = 64530
	doc.Ports[1].PID = 4242 // api's PortActual is covered by the service row
	doc.Ports[2].PID = 999
	doc.Ports[2].Command = "postgres"

	out := captureStdout(t, func() { printStatusListeners(doc) })
	for _, want := range []string{"9999 (run finance-web)", "5432 (unowned: postgres, pid 999)"} {
		if !strings.Contains(out, want) {
			t.Errorf("listener output is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "3001") {
		t.Errorf("listener line repeated a running service's own port:\n%s", out)
	}
}

// The summary says what it counts. With no declared service running but an
// ad-hoc run alive, it must not read as "nothing is running".
func TestStatusSummarySaysDeclaredAndStillShowsAdHocRuns(t *testing.T) {
	noColor(t)
	doc := statusScopeFixture()
	doc.Worktree.Services = nil
	doc.Worktree.RunningServices = 0
	doc.Worktree.Resources = nil
	doc.Ports = []state.Port{{Port: 8082,
		Run: &state.Run{ID: "adhoc", Group: "shop", Name: "finance-web", RootPID: 7}}}

	out := captureStdout(t, func() {
		printStatusResources(doc)
		printStatusListeners(doc)
	})
	for _, want := range []string{"0 declared running", "8082 (run finance-web)"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary output is missing %q:\n%s", want, out)
		}
	}
}

// A run started with `oberth start -d` and holding no listener is still a run
// option-berth owns. Status has to name it: `down` stops it, and a reader who
// cannot see it falls back to scanning `ps`. The brief contract says ad_hoc_runs
// names such runs while they are alive; without this a portless run is invisible
// and only its absence from every section reveals it.
func TestBriefStatusNamesALiveRunThatHoldsNoListener(t *testing.T) {
	doc := statusScopeFixture()
	doc.Runs = []rpc.RunRecord{{
		ID: "portless-run", PID: 4242, Group: "shop", Name: "portless",
		Cmd: "sh -c while true", Cwd: "/work/shop",
		StartedAt: "2026-11-16T09:00:00Z", Status: "running",
	}}

	brief := briefStatusFrom(doc)
	var found *briefAdHocRun
	for i := range brief.Worktree.AdHocRuns {
		if brief.Worktree.AdHocRuns[i].RunID == "portless-run" {
			found = &brief.Worktree.AdHocRuns[i]
		}
	}
	if found == nil {
		t.Fatalf("ad_hoc_runs = %+v, want the live portless run named", brief.Worktree.AdHocRuns)
	}
	if found.Name != "portless" || found.PID != 4242 || found.Cmd != "sh -c while true" {
		t.Fatalf("portless run = %+v, want its recorded facts", *found)
	}
	if found.Ports == nil || len(found.Ports) != 0 {
		t.Fatalf("ports = %v, want an empty array rather than a missing field", found.Ports)
	}
}

// A run a running service row already names must not be listed twice: the
// service row is its home. A portless declared worker therefore stays out of
// ad_hoc_runs while its service row is running.
func TestBriefStatusDoesNotRepeatARunAServiceRowNames(t *testing.T) {
	doc := statusScopeFixture()
	doc.Runs = []rpc.RunRecord{{
		ID: "api-run", PID: 4242, Group: "shop", Name: "api",
		Cmd: "go run ./cmd/api", Cwd: "/work/shop", Status: "running",
	}}

	brief := briefStatusFrom(doc)
	for _, run := range brief.Worktree.AdHocRuns {
		if run.RunID == "api-run" {
			t.Fatalf("ad_hoc_runs = %+v, want no row for a run its service row already names", brief.Worktree.AdHocRuns)
		}
	}
}

// An empty `services` array means two different things, and a brief reader
// cannot ask the full document to tell them apart. "No oberth.yaml declares
// this worktree" sends the reader to `oberth init`; "the manifest declares no
// services" says the checkout is configured and idle. Publishing only the
// empty list makes the first look like the second. Issue #384.
func TestBriefStatusSaysWhenNoManifestDeclaresTheServices(t *testing.T) {
	doc := statusScopeFixture()
	doc.Worktree.Services = []state.Service{}
	doc.Ports = nil
	doc.manifestMissing = true

	raw, err := json.Marshal(briefStatusFrom(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"manifest_missing":true`) {
		t.Fatalf("brief = %s, want the missing-manifest fact published", raw)
	}
}

// The fact is absent when a manifest did declare the worktree: an explicit
// false would be a second spelling of "nothing to say here".
func TestBriefStatusOmitsManifestMissingWhenAManifestExists(t *testing.T) {
	doc := statusScopeFixture()
	raw, err := json.Marshal(briefStatusFrom(doc))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "manifest_missing") {
		t.Fatalf("brief = %s, want no manifest_missing key for a declared worktree", raw)
	}
}

// The full document is the one a reader asks when it wants everything, so it
// must name the live runs the brief names. Leaving Runs only on the brief made
// `status --json` blind to a portless run while `status --json --brief` showed
// it — the same worktree, two different answers.
func TestFullStatusDocumentPublishesLiveRuns(t *testing.T) {
	doc := statusDocument{
		Worktree: state.Group{Name: "shop"},
		Ports:    []state.Port{},
		Runs: []rpc.RunRecord{{
			ID: "portless-run", PID: 4242, Group: "shop", Name: "portless",
			Cmd: "sh -c while true", Status: "running",
		}},
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"runs":[{`) || !strings.Contains(string(raw), `"name":"portless"`) {
		t.Fatalf("full status = %s, want the live run published", raw)
	}
}

// Both documents are projections of one run set, so a run the brief names is a
// run the full document names. This is the invariant that keeps a future
// command from answering the same question from a different source.
func TestBriefAndFullDocumentsNameTheSameRuns(t *testing.T) {
	doc := statusScopeFixture()
	doc.Runs = []rpc.RunRecord{{
		ID: "spare-run", PID: 4243, Group: "shop", Name: "spare",
		Cmd: "sleep 300", Status: "running",
	}}

	full, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	brief, err := json.Marshal(briefStatusFrom(doc))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(full), "spare-run") {
		t.Fatalf("full status = %s, want the spare run", full)
	}
	if !strings.Contains(string(brief), "spare-run") {
		t.Fatalf("brief status = %s, want the spare run", brief)
	}
}

// A worktree whose name is unknown must claim nothing. The resolver's "" means
// "every run in the ledger", which explains a refusal about an unknown project
// but would attribute every groupless run to a nameless document. The guard
// lives where the document is built, so no projection has to repeat it.
func TestNamelessWorktreeClaimsNoRuns(t *testing.T) {
	resetRouting(t)
	noRuntimeServiceReachable(t)
	if got := statusRuns(context.Background(), ""); got != nil {
		t.Fatalf("nameless worktree claimed %+v", got)
	}
}
