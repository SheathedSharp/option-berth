package cmd

import (
	"go/parser"
	"go/token"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The model adapter is optional. "Optional" has to mean something the build can
// check, not a comment: if a control-path package ever imports internal/attn/model,
// then a missing key, an expired setting or a slow provider becomes able to
// reach `status`, `up`, `down`, `restart` or `logs`. These two tests turn that
// promise into a failing build.

// dependencyClosure returns the packages the given pattern depends on, with the
// module prefix stripped so the assertions read as repo paths.
func dependencyClosure(t *testing.T, pattern string) []string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is unavailable; dependency assertions need `go list`")
	}
	out, err := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", pattern).CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps %s: %v\n%s", pattern, err, out)
	}
	module := modulePath(t)
	paths := strings.Fields(string(out))
	for i, path := range paths {
		paths[i] = strings.TrimPrefix(path, module+"/")
	}
	return paths
}

func modulePath(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "list", "-m").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -m: %v\n%s", err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestDaemonAndScannerNeverImportTheModelClient pins the two long-running
// processes: the daemon observes facts and the scanner reduces them, and
// neither may acquire a network client. This is the boundary that lets a
// user run the whole engine with no key and no settings file at all.
func TestDaemonAndScannerNeverImportTheModelClient(t *testing.T) {
	for _, pkg := range []string{"../daemon", "../scanner", "../groups", "../ports", "../servicefacts"} {
		t.Run(filepath.Base(pkg), func(t *testing.T) {
			for _, dep := range dependencyClosure(t, pkg) {
				if dep == "internal/attn/model" || strings.HasPrefix(dep, "internal/attn/model/") {
					t.Fatalf("%s depends on %s: the model client is off the control path", pkg, dep)
				}
			}
		})
	}
}

// TestControlPathCommandsNeverImportTheModelClient reads the import blocks of the
// commands an agent uses to inspect and change running services. A transitive
// check would also pass today, but this one says which file went wrong, and it
// keeps working while a package is being moved around.
func TestControlPathCommandsNeverImportTheModelClient(t *testing.T) {
	const jevImport = `"github.com/sheathedsharp/option-berth/engine/internal/attn/model"`
	files := []string{"status.go", "up.go", "down.go", "restart.go", "logs.go", "start.go"}
	fset := token.NewFileSet()
	for _, name := range files {
		file, err := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, spec := range file.Imports {
			path, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				t.Fatalf("%s: unquote %s: %v", name, spec.Path.Value, err)
			}
			if path == "github.com/sheathedsharp/option-berth/engine/internal/attn/model" || strings.Contains(path, "internal/attn/model") {
				t.Errorf("%s imports %s; the optional model client stays out of the control path", name, jevImport)
			}
		}
	}
}
