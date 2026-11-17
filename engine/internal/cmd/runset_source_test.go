package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runSetReaders is every file allowed to read run state directly, and why. The
// resolver and its offline half are the answer itself; the other two ask a
// different question ("which ports did this one run get", "which run owns this
// listener") and would not be answered by the worktree run set.
//
// A new entry here is a deliberate decision, not an accident: adding one is how
// a fifth private copy of "which runs exist" would enter the tree, and the
// review that adds it is the point.
var runSetReaders = map[string]string{
	"runledger.go":       "the resolver itself",
	"logs_offline.go":    "the resolver's offline half (mirror + store)",
	"groupread.go":       "port attribution builds a registry view, not a run list",
	"start.go":           "waits for the ports one just-spawned run bound",
	"start_addresses.go": "overlays one run's assigned address onto its group row",
}

// TestRunSetIsDerivedInOnePlace is the guard for a bug class, not a bug. Four
// separate feedback issues (#391, #392, #393, #395) were one mistake: each
// command derived "which runs does this worktree have" from whichever source it
// happened to hold, so a fix in one command left the same answer wrong in the
// next. The resolver in runledger.go is the single owner; this test fails when a
// file starts reading the registry, the mirror or runs.list on its own, so the
// next author has to answer the question in the resolver or justify an exemption
// here.
func TestRunSetIsDerivedInOnePlace(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		body := string(data)
		readsRuns := strings.Contains(body, "runs.Load()") || strings.Contains(body, `"runs.list"`)
		if !readsRuns {
			continue
		}
		if _, allowed := runSetReaders[name]; !allowed {
			t.Errorf("%s reads run state directly.\n"+
				"Ask worktreeRuns in runledger.go instead — it is the one answer to "+
				"\"which runs belong to this worktree\". Reading the mirror, the registry "+
				"or runs.list here is how #391/#392/#393/#395 became four bugs instead of one.", name)
		}
	}
}

// The exemptions above are the whole allow-list, so a file that stops reading
// run state should leave it: a stale exemption is a place the next author would
// think is already reviewed. This keeps the list honest.
func TestRunSetReaderExemptionsAreStillNeeded(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for name, reason := range runSetReaders {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("exemption %q (%s) names a file that does not exist", name, reason)
			continue
		}
		body := string(data)
		if !strings.Contains(body, "runs.Load()") && !strings.Contains(body, `"runs.list"`) {
			t.Errorf("%s no longer reads run state; drop its exemption (%s)", name, reason)
		}
	}
}
