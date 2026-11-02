package rpc

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The published protocol is generated from the Go types in this package, so a
// field declared here is a promise every client reads and codes against. The
// schema, docs/cli.md and the handlers are three separate places, and nothing
// makes them agree by itself: Selector.Key and HostParams.Host were both
// published and documented, and no handler ever read either. A client that
// follows the schema gets its input accepted and silently ignored.
//
// So the parameter types are held to a rule the build can check: every field
// of every params type must be selected by code in this repository, or be
// withdrawn here with a reason.

// wireWithdrawn records fields removed from the protocol on purpose. An entry
// is a decision, not an oversight: the field used to be published and no
// handler read it. Keeping the name listed is what stops it quietly returning.
var wireWithdrawn = map[string]string{
	"host": "withdrawn: reserved for remote hosts, but no handler ever read it",
	"key":  "withdrawn: Selector.Key promised expansion before dispatch, which never existed",
}

// paramsTypeNames is the set of Go type names a client can send: every method's
// params type, plus every type reachable from one by value or pointer. A field
// nested inside a params type is published exactly as deeply as the top-level
// one — Selector.Key reaches Main without Selector ever being a method's params
// type — so the closure is what the guard has to inspect.
func paramsTypeNames(t *testing.T, dir string) map[string]bool {
	t.Helper()
	roots := map[string]bool{}
	for _, d := range registry {
		if d.Params != nil {
			roots[goTypeName(d.Params)] = true
		}
	}

	structs := structFields(t, dir)
	out := map[string]bool{}
	queue := make([]string, 0, len(roots))
	for name := range roots {
		queue = append(queue, name)
		out[name] = true
	}
	for len(queue) > 0 {
		name := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		for _, fieldType := range structs[name] {
			if fieldType == "" || out[fieldType] {
				continue
			}
			if _, known := structs[fieldType]; !known {
				continue
			}
			out[fieldType] = true
			queue = append(queue, fieldType)
		}
	}
	return out
}

// structFields maps a struct type name in this package to the bare names of the
// struct types its fields are declared with, so the params closure can walk
// into nested types.
func structFields(t *testing.T, dir string) map[string][]string {
	t.Helper()
	out := map[string][]string{}
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				var fieldTypes []string
				for _, f := range st.Fields.List {
					fieldTypes = append(fieldTypes, bareTypeName(f.Type))
				}
				out[ts.Name.Name] = fieldTypes
				return true
			})
		}
	}
	return out
}

// bareTypeName is the element type name of a field's type expression, ignoring
// pointers, slices and a package qualifier.
func bareTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return bareTypeName(t.X)
	case *ast.ArrayType:
		return bareTypeName(t.Elt)
	case *ast.Ident:
		return t.Name
	case *ast.SelectorExpr:
		return t.Sel.Name
	}
	return ""
}

// goTypeName is the bare Go type name of v, ignoring a slice or pointer wrapper.
func goTypeName(v any) string {
	name := fmt.Sprintf("%T", v)
	name = strings.TrimPrefix(name, "[]")
	name = strings.TrimPrefix(name, "*")
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	return name
}

// wireField is one published struct field: the Go name code must select, the
// JSON name the schema publishes, and the params types that declare it.
type wireField struct {
	GoName   string
	JSONName string
	Owners   []string
}

// publishedFields collects every JSON-published field of every params type.
// The Go field name is what handlers select (and what embedded types such as
// HostParams contribute), so that is the key, not the JSON name.
func publishedFields(t *testing.T, dir string) map[string]*wireField {
	t.Helper()
	params := paramsTypeNames(t, dir)
	fields := map[string]*wireField{}

	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			ast.Inspect(file, func(n ast.Node) bool {
				ts, ok := n.(*ast.TypeSpec)
				if !ok || !params[ts.Name.Name] {
					return true
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok {
					return true
				}
				for _, f := range st.Fields.List {
					if f.Tag == nil {
						continue
					}
					tag, err := strconv.Unquote(f.Tag.Value)
					if err != nil {
						continue
					}
					value, ok := reflect.StructTag(tag).Lookup("json")
					if !ok {
						continue
					}
					jsonName := strings.Split(value, ",")[0]
					if jsonName == "-" {
						continue
					}
					for _, name := range f.Names {
						wf := fields[name.Name]
						if wf == nil {
							wf = &wireField{GoName: name.Name, JSONName: jsonName}
							fields[name.Name] = wf
						}
						wf.Owners = append(wf.Owners, ts.Name.Name)
					}
				}
				return true
			})
		}
	}
	for _, wf := range fields {
		sort.Strings(wf.Owners)
	}
	return fields
}

// selectedOnType reports whether the field is selected from a value whose
// static type is one of owners. Matching on the field name alone is not enough:
// Selector.Key would otherwise be "read" by the dozens of unrelated `.Key`
// selections on map keys and servicefact keys, which is exactly how a dead
// field hides.
func selectedOnType(t *testing.T, root string, owners []string, field string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found {
			return nil
		}
		if d.IsDir() {
			switch d.Name() {
			case "testdata", "node_modules", "vendor":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			return nil
		}
		found = fileSelectsField(file, owners, field)
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return found
}

// fileSelectsField looks for <typed value>.<field> inside one file, using the
// parameters and local variables a handler binds by type. A value qualifies
// when its declared type names one of owners.
func fileSelectsField(file *ast.File, owners []string, field string) bool {
	owner := func(name string) bool {
		name = strings.TrimPrefix(name, "*")
		if i := strings.LastIndex(name, "."); i >= 0 {
			name = name[i+1:]
		}
		for _, o := range owners {
			if o == name {
				return true
			}
		}
		return false
	}

	// Values known to carry an owner type: handler params, receivers and
	// locals declared with one.
	typed := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.FuncDecl:
			if x.Recv != nil {
				for _, f := range x.Recv.List {
					for _, name := range f.Names {
						if typeExprMatches(f.Type, owner) {
							typed[name.Name] = true
						}
					}
				}
			}
			if x.Type.Params != nil {
				for _, f := range x.Type.Params.List {
					for _, name := range f.Names {
						if typeExprMatches(f.Type, owner) {
							typed[name.Name] = true
						}
					}
				}
			}
		case *ast.AssignStmt:
			for i, rhs := range x.Rhs {
				if i >= len(x.Lhs) {
					break
				}
				id, ok := x.Lhs[i].(*ast.Ident)
				if !ok {
					continue
				}
				if composite, ok := rhs.(*ast.CompositeLit); ok && typeExprMatches(composite.Type, owner) {
					typed[id.Name] = true
				}
			}
		case *ast.ValueSpec:
			if typeExprMatches(x.Type, owner) {
				for _, name := range x.Names {
					typed[name.Name] = true
				}
			}
		}
		return true
	})

	found := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok || sel.Sel == nil || sel.Sel.Name != field {
			return true
		}
		if id, ok := sel.X.(*ast.Ident); ok && typed[id.Name] {
			found = true
		}
		return !found
	})
	return found
}

// typeExprMatches reports whether a type expression names one of owners.
func typeExprMatches(expr ast.Expr, owner func(string) bool) bool {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return typeExprMatches(t.X, owner)
	case *ast.ArrayType:
		return typeExprMatches(t.Elt, owner)
	case *ast.Ident:
		return owner(t.Name)
	case *ast.SelectorExpr:
		return owner(t.Sel.Name)
	}
	return false
}

// TestEveryPublishedParamFieldIsRead rejects a published field that no handler
// reads. This is the guard that would have caught Selector.Key and
// HostParams.Host before either reached a client.
func TestEveryPublishedParamFieldIsRead(t *testing.T) {
	dir := packageDir(t)
	root := filepath.Dir(filepath.Dir(dir)) // engine/

	for _, wf := range publishedFields(t, dir) {
		if reason, withdrawn := wireWithdrawn[wf.JSONName]; withdrawn {
			t.Errorf("%s publishes %q again, but the field was %s",
				strings.Join(wf.Owners, "/"), wf.JSONName, reason)
			continue
		}
		if !selectedOnType(t, root, wf.Owners, wf.GoName) {
			t.Errorf("%s publishes %q (Go field %s), but no code in this repository reads it; "+
				"read it, or withdraw it with an entry in wireWithdrawn",
				strings.Join(wf.Owners, "/"), wf.JSONName, wf.GoName)
		}
	}
}

// packageDir is this package's directory.
func packageDir(t *testing.T) string {
	t.Helper()
	_, here, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate this package")
	}
	return filepath.Dir(here)
}
