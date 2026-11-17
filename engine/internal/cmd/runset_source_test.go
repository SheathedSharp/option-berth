package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
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

// recordConversions is every file allowed to build a wire run record from local
// evidence. The two conversions in runledger.go are the only ones: a second
// hand-written conversion is how the offline reader lost Argv, how the unobserved
// ending lost it too, and how `restart --run` refused runs this version started.
var recordConversions = map[string]string{
	"runledger.go": "the live and ended conversions themselves",
}

// TestRunRecordsAreConvertedInOnePlace guards the second half of the same bug
// class: one recorded run must look the same to every reader. A projection that
// writes its own rpc.RunRecord literal invents a second shape, and the field it
// forgets is the field a command silently loses.
func TestRunRecordsAreConvertedInOnePlace(t *testing.T) {
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
		files, err := parser.ParseFile(token.NewFileSet(), filepath.Join(dir, name), nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parsing %s: %v", name, err)
		}
		// An empty literal is the zero value a failed lookup returns, not a
		// conversion: only a literal with fields invents a record shape.
		if !buildsRunRecord(files) {
			continue
		}
		if _, allowed := recordConversions[name]; !allowed {
			t.Errorf("%s builds an rpc.RunRecord literal.\n"+
				"Derive it from liveRunRecord or exitRunRecord in runledger.go. A second "+
				"conversion is how the offline and unobserved paths lost Argv while the "+
				"host path kept it.", name)
		}
	}
}

// buildsRunRecord reports whether the file contains a non-empty rpc.RunRecord
// composite literal.
func buildsRunRecord(file *ast.File) bool {
	found := false
	ast.Inspect(file, func(node ast.Node) bool {
		lit, ok := node.(*ast.CompositeLit)
		if !ok || len(lit.Elts) == 0 {
			return true
		}
		sel, ok := lit.Type.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "rpc" && sel.Sel.Name == "RunRecord" {
			found = true
		}
		return true
	})
	return found
}

// The exemption list must not outlive its reason, same rule as above.
func TestRecordConversionExemptionsAreStillNeeded(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for name, reason := range recordConversions {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Errorf("exemption %q (%s) names a file that does not exist", name, reason)
			continue
		}
		if !strings.Contains(string(data), "rpc.RunRecord{") {
			t.Errorf("%s no longer builds a run record; drop its exemption (%s)", name, reason)
		}
	}
}
