package runsreg

import (
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// testRegistry hands back a registry whose liveness answer comes from the pids
// named, and which reads neither the host process table nor a runs.json.
func testRegistry(alive ...int) *Registry {
	live := make(map[int]bool, len(alive))
	for _, pid := range alive {
		live[pid] = true
	}
	reg := New()
	reg.Mirror = false
	reg.Alive = func(pid int) bool { return live[pid] }
	reg.Parents = func() map[int]int { return nil }
	// A synthetic pid has no host identity to read. Tests exercising PID reuse
	// inject StartTime explicitly, just as ancestry tests inject Parents.
	reg.StartTime = nil
	return reg
}

// registerAll fills the registry with the records in the order given.
func registerAll(reg *Registry, recs ...Record) {
	for _, rec := range recs {
		reg.Register(rec)
	}
}

// runIDs names the runs in list order, so a failure message shows the order the
// registry answered with rather than a struct dump.
func runIDs(recs []Record) []string {
	ids := make([]string, len(recs))
	for i, rec := range recs {
		ids[i] = rec.ID
	}
	return ids
}

// The helper must stay independent of the host even when a synthetic PID
// happens to exist there. Checking the hook before List makes this regression
// deterministic: it fails without invoking ps or depending on a PID collision.
func TestTestRegistryDoesNotReadHostProcessIdentity(t *testing.T) {
	r := testRegistry(100)
	if r.StartTime != nil {
		t.Fatal("testRegistry retained a host process identity reader; synthetic PIDs require explicit identity evidence")
	}
	r.Register(Record{ID: "synthetic", PID: 100, StartedAt: time.Unix(1, 0)})
	if got := r.List(); len(got) != 1 || got[0].ID != "synthetic" {
		t.Fatalf("synthetic live run was pruned without identity evidence: %+v", got)
	}
}

// The test-only override must not be moved into the production constructor.
// Do not invoke the reader here: its platform behavior is tested in ports.
func TestNewRegistryKeepsProcessIdentityReader(t *testing.T) {
	if r := New(); r.StartTime == nil {
		t.Fatal("production registry lost its PID reuse identity reader")
	}
}

// List answers oldest first whatever order the runs arrived in, and Unregister
// removes a run exactly once.
func TestRegisterListAndUnregister(t *testing.T) {
	reg := testRegistry(4100, 4200)
	base := time.Date(2026, 2, 3, 9, 15, 0, 0, time.UTC)
	registerAll(reg,
		Record{ID: "later", PID: 4200, Group: "shop", Name: "api", StartedAt: base.Add(90 * time.Second)},
		Record{ID: "earlier", PID: 4100, Group: "shop", Name: "web", StartedAt: base},
	)

	listed := reg.List()
	if len(listed) != 2 {
		t.Fatalf("List = %d runs, want 2", len(listed))
	}
	if listed[0].ID != "earlier" || listed[1].ID != "later" {
		t.Fatalf("List = %v, want the older run first", runIDs(listed))
	}

	if !reg.Unregister(4100) {
		t.Fatal("Unregister(4100) reported nothing to remove")
	}
	if reg.Unregister(4100) {
		t.Fatal("Unregister(4100) removed the same run twice")
	}
	if remaining := reg.List(); len(remaining) != 1 {
		t.Fatalf("List after unregister = %v, want one run left", runIDs(remaining))
	}
}

// A pid registered again keeps the run id it already had, so the code snapshot
// and the exit history recorded under that id stay attached to it.
func TestRegisterKeepsTheIDOfAReregisteredPID(t *testing.T) {
	reg := testRegistry(4300)
	first := reg.Register(Record{ID: "keep", PID: 4300, Group: "shop", Name: "web"})
	second := reg.Register(Record{PID: 4300, Group: "shop", Name: "web"})
	if second.ID != first.ID || second.ID != "keep" {
		t.Fatalf("re-registered id = %q (first %q), want keep", second.ID, first.ID)
	}
}

// Prune drops the run whose process is gone and keeps the live one.
func TestPruneDropsDeadRuns(t *testing.T) {
	reg := testRegistry(4400)
	registerAll(reg,
		Record{ID: "live", PID: 4400, Group: "shop", Name: "web"},
		Record{ID: "dead", PID: 4499, Group: "shop", Name: "gone"},
	)

	reg.Prune()
	left := reg.List()
	if len(left) != 1 || left[0].PID != 4400 {
		t.Fatalf("Prune left %v, want only pid 4400", runIDs(left))
	}
}

func TestRunAttributesAPortByItsPPIDAncestry(t *testing.T) {
	reg := testRegistry(4500)
	reg.Register(Record{ID: "run-4500", PID: 4500, Group: "itest", Name: "web"})
	// oberth start (4500) -> npm (4600) -> node (4700) -> esbuild (4800)
	reg.Parents = func() map[int]int { return map[int]int{4800: 4700, 4700: 4600, 4600: 4500} }

	run, ok := reg.Run(state.Port{PID: 4800, PPID: 4700})
	if !ok {
		t.Fatal("a descendant's port was attributed to no run")
	}
	if run.Group != "itest" || run.Name != "web" || run.RootPID != 4500 {
		t.Fatalf("Run(descendant) = %+v, want itest/web rooted at 4500", run)
	}

	if unrelated, ok := reg.Run(state.Port{PID: 4900, PPID: 1}); ok {
		t.Fatalf("an unrelated listener was attributed to %+v", unrelated)
	}
}

// TestRunAttributesTheLinuxScannerShape is the shape a Linux scan hands the
// resolver: `ss -tlnp` reports the listening pid and nothing else, so the row
// arrives with PPID 0 and no run of its own. Attribution then has only the
// process table to work with, and it has to reach the `oberth start` that owns
// the listener through it.
func TestRunAttributesTheLinuxScannerShape(t *testing.T) {
	reg := testRegistry(4500)
	reg.Register(Record{ID: "run-4500", PID: 4500, Group: "itest", Name: "web"})
	// oberth start (4500) -> the listener it spawned (4900); ss gave no ppid.
	reg.Parents = func() map[int]int { return map[int]int{4900: 4500, 4500: 42} }

	run, ok := reg.Run(state.Port{PID: 4900, PPID: 0})
	if !ok {
		t.Fatal("a listener spawned by a run was not attributed to it")
	}
	if run.ID != "run-4500" || run.Group != "itest" || run.Name != "web" || run.RootPID != 4500 {
		t.Fatalf("Run = %+v, want itest/web rooted at 4500", run)
	}
}

// TestRunAttributesTheRegisteredPIDItself covers `oberth start` in the
// foreground: the run is registered under the pid of the child it spawned, and
// that child holds the socket. No walk is needed, so the answer must not depend
// on the process table being readable at all.
func TestRunAttributesTheRegisteredPIDItself(t *testing.T) {
	reg := testRegistry(4300)
	reg.Register(Record{ID: "run-4300", PID: 4300, Group: "itest", Name: "web"})
	reg.Parents = func() map[int]int { t.Fatal("the process table should not be needed"); return nil }

	run, ok := reg.Run(state.Port{PID: 4300})
	if !ok {
		t.Fatal("the registered run's own port was attributed to nothing")
	}
	if run.ID != "run-4300" || run.Name != "web" || run.RootPID != 4300 {
		t.Fatalf("Run = %+v, want the registered run", run)
	}
}

// The parent the scanner already resolved is evidence the registry holds, so it
// answers before the process table is read.
func TestRunUsesTheDirectParentBeforeTheProcessTable(t *testing.T) {
	reg := testRegistry(4500)
	reg.Register(Record{ID: "run-4500", PID: 4500, Group: "itest", Name: "web"})
	reg.Parents = func() map[int]int { t.Fatal("the process table should not be needed"); return nil }

	if run, ok := reg.Run(state.Port{PID: 4600, PPID: 4500}); !ok || run.Group != "itest" {
		t.Fatalf("Run(child) = %+v/%v, want the parent's group", run, ok)
	}
}

// TestRunIgnoresAStaleStamp: the port's own run fields are the attribution
// layer's output, not a second registry. A stamp the live registry cannot
// reproduce is not ownership — unknown stays unknown.
func TestRunIgnoresAStaleStamp(t *testing.T) {
	r := testRegistry()
	run, ok := r.Run(state.Port{
		PID: 400,
		Run: &state.Run{ID: "x", Group: "from-file", Name: "web", RootPID: 100},
	})
	if ok {
		t.Fatalf("Run = %+v/%v, want no ownership from a stale stamp", run, ok)
	}
}

// legacyRunsHome points HOME and USERPROFILE at a throwaway directory: that is
// where the runs.json the import takes over lives.
func legacyRunsHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func TestImportLegacyTakesOverRunsJSON(t *testing.T) {
	legacyRunsHome(t)
	pid := os.Getpid()
	if err := runs.Add(runs.Entry{PID: pid, Tag: "legacy", ID: "old-1", Cmd: "npm run dev"}); err != nil {
		t.Fatalf("writing the legacy runs.json: %v", err)
	}
	if _, err := os.Stat(runs.Path()); err != nil {
		t.Fatalf("runs.json was not written: %v", err)
	}

	reg := New()
	reg.Mirror = false
	moved, err := reg.ImportLegacy()
	if err != nil || moved != 1 {
		t.Fatalf("ImportLegacy = %d/%v, want 1/nil", moved, err)
	}
	if _, err := os.Stat(runs.Path()); !os.IsNotExist(err) {
		t.Fatalf("runs.json survived the import: %v", err)
	}

	imported := reg.List()
	if len(imported) != 1 {
		t.Fatalf("List after the import = %v, want the imported run", runIDs(imported))
	}
	// A `option-berth run --tag legacy` entry carries no group or name of its own, so
	// the tag stands for both (the documented alias).
	if imported[0].Group != "legacy" || imported[0].Name != "legacy" || imported[0].ID != "old-1" {
		t.Fatalf("imported record = %+v", imported[0])
	}
}

func TestImportLegacyRewritesTheFileItOwns(t *testing.T) {
	legacyRunsHome(t)
	pid := os.Getpid()
	if err := runs.Add(runs.Entry{PID: pid, Tag: "legacy", ID: "old-1"}); err != nil {
		t.Fatalf("writing the legacy runs.json: %v", err)
	}

	reg := New() // mirroring on: the runtime service owns the file from here
	moved, err := reg.ImportLegacy()
	if err != nil || moved != 1 {
		t.Fatalf("ImportLegacy = %d/%v, want 1/nil", moved, err)
	}

	var rewritten struct {
		Entries map[string]runs.Entry `json:"runs"`
	}
	data, err := os.ReadFile(runs.Path())
	if err != nil {
		t.Fatalf("the runtime service did not rewrite runs.json: %v", err)
	}
	if err := json.Unmarshal(data, &rewritten); err != nil {
		t.Fatalf("runs.json is not the documented shape: %v", err)
	}
	entry, ok := rewritten.Entries[itoa(pid)]
	if !ok {
		t.Fatalf("runs.json = %s, want an entry for pid %d", data, pid)
	}
	if entry.Group != "legacy" || entry.Name != "legacy" {
		t.Fatalf("mirrored entry = %+v, want the tag standing in for group and name", entry)
	}
}

func itoa(i int) string { return strconv.Itoa(i) }

// TestPruneDropsARecycledPID: a pid that now belongs to a newer process is not
// the run the record names. This is the imported-runs case after a runtime service
// restart — nothing reaps those processes, so liveness alone cannot tell. Only
// positive evidence prunes; an absent answer keeps the run.
func TestPruneDropsARecycledPID(t *testing.T) {
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name    string
		start   time.Time
		found   bool
		keepRun bool
	}{
		{"the recorded process", base, true, true},
		{"an earlier process is not positive reuse evidence", base.Add(-time.Hour), true, true},
		{"a process started moments later", base.Add(time.Second), true, true},
		{"exact tolerance boundary", base.Add(ports.StartTolerance), true, true},
		{"just beyond the tolerance", base.Add(ports.StartTolerance + time.Nanosecond), true, false},
		{"a pid reused by a newer process", base.Add(time.Hour), true, false},
		{"no table answer is unknown", time.Time{}, false, true},
		{"unavailable timestamp is ignored", base.Add(time.Hour), false, true},
		{"zero timestamp is unknown even when found", time.Time{}, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := testRegistry(321)
			calls := 0
			r.StartTime = func(pid int) (time.Time, bool) {
				calls++
				if pid != 321 {
					t.Fatalf("identity queried for PID %d, want 321", pid)
				}
				return tc.start, tc.found
			}
			r.Register(Record{ID: "a", PID: 321, Group: "g", Name: "api", StartedAt: base})
			r.Prune()
			if calls != 1 {
				t.Fatalf("identity queries = %d, want 1", calls)
			}
			if _, found := r.Lookup(321); found != tc.keepRun {
				t.Fatalf("run kept=%v, want %v", found, tc.keepRun)
			}
		})
	}
}

func TestPruneMirrorCompareAndDeletePreservesReregistration(t *testing.T) {
	for _, window := range []string{"identity judgment window", "memory delete window"} {
		t.Run(window, func(t *testing.T) {
			r, old := mirroredRegistry(t)
			newer := old
			newer.ID = "new"
			newer.StartedAt = old.StartedAt.Add(time.Second)
			if window == "identity judgment window" {
				// Alive is actually executed; Alive=false would short-circuit
				// a StartTime hook and never perform the intended registration.
				r.Alive = func(int) bool { r.Register(newer); return false }
				r.Prune()
			} else {
				// The new gate excludes this interleaving within one registry.
				// Replay an already-delayed disk effect to prove the CAS guard,
				// without a test callback reentering Register while holding it.
				r.Unregister(old.PID)
				r.Register(newer)
				r.mirrorMu.Lock()
				r.mirrorRemoveLocked(old)
				r.mirrorMu.Unlock()
			}
			requireMirroredRun(t, r, newer)
		})
	}
}
