package groups

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// renameWorld is a project with a main checkout and a worktree, both running,
// plus an unrelated repository called "store", resolved and built the way the
// daemon publishes them.
func renameWorld(t *testing.T, withConfig bool) (checkouts, *Index, []state.Group) {
	t.Helper()
	c := newCheckouts(t, "shop", "feat")
	if withConfig {
		writeFile(t, filepath.Join(c.main, ConfigName), "# the shop\nname: shop\nports: [8000]\n")
		writeFile(t, filepath.Join(c.wt, ConfigName), "name: shop\nports: [8000]\n")
	}
	other := mkdir(t, filepath.Dir(c.main), "store")
	mkdir(t, other, ".git")

	x := NewIndex()
	for _, dir := range []string{c.main, c.wt, other} {
		x.Observe(dir)
	}
	pp := Resolve([]state.Port{
		nativePort(8000, c.main), nativePort(8001, c.wt), nativePort(9000, other),
	}, fakeRuns{7100: {group: "started", name: "job"}}, x)
	pp = append(pp, Resolve([]state.Port{nativePort(7100, c.main)},
		fakeRuns{7100: {group: "started", name: "job"}}, x)...)
	return c, x, Groups(pp, x)
}

func requireRenameError(t *testing.T, err error, kind RenameErrorKind, msg, hint string) {
	t.Helper()
	var re *RenameError
	if !errors.As(err, &re) {
		t.Fatalf("got %T %v, want *RenameError kind %d", err, err, kind)
	}
	if re.Kind != kind || re.Msg != msg || re.Hint != hint {
		t.Errorf("RenameError = kind %d msg %q hint %q, want %d / %q / %q", re.Kind, re.Msg, re.Hint, kind, msg, hint)
	}
	if re.Error() != msg {
		t.Errorf("Error() = %q, want %q", re.Error(), msg)
	}
}

// requireRenamePlan compares the complete write intent. A project rename must
// not silently lose an alias, redirect a file write or add unrelated groups.
func requireRenamePlan(t *testing.T, published []state.Group, index *Index, from string, want RenamePlan) *RenamePlan {
	t.Helper()
	plan, err := PlanRename(published, index, from, want.Project)
	if err != nil {
		t.Fatalf("rename %s to %s: %v", from, want.Project, err)
	}
	if !reflect.DeepEqual(plan, &want) {
		t.Fatalf("rename intent = %#v, want %#v", plan, want)
	}
	return plan
}

func TestPlanRenameFileProject(t *testing.T) {
	c, x, published := renameWorld(t, true)
	requireRenamePlan(t, published, x, "shop@feat", RenamePlan{
		Main: c.main, ConfigPath: filepath.Join(c.main, ConfigName), Project: "market",
		Renames: map[string]string{"shop": "market", "shop@feat": "market@feat"},
	})
}

func TestPlanRenameAutoProject(t *testing.T) {
	c, x, published := renameWorld(t, false)
	plan := requireRenamePlan(t, published, x, "shop", RenamePlan{
		Main: c.main, Project: "market", Renames: map[string]string{"shop": "market", "shop@feat": "market@feat"},
	})
	// Applying the write intent is a separate operation; the resolver must agree
	// with both checkout names, not just with the plan's own map.
	x.SetAliases(map[string]string{plan.Main: plan.Project})
	checkNaming(t, x, map[string]string{"main": c.main, "linked": c.wt}, []namingExpectation{
		{"main", 8000, "market", ""}, {"linked", 8001, "market@feat", ""},
	})
}

func TestPlanRenameRefusals(t *testing.T) {
	c, x, gg := renameWorld(t, false)
	cases := []struct {
		name, from, to, msg, hint string
		kind                      RenameErrorKind
	}{
		{"unknown group", "nope", "market", "no project named nope", "run `oberth status` inside its worktree", RenameNotFound},
		{"collides with another project", "shop", "store", "a group named store already exists", "pick another name", RenameConflict},
		{"a run's --group", "started", "market", "started is named by the --group of the `oberth start` run in it, not by a project", "restart the run with `oberth start --group market`", RenameRefused},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			plan, err := PlanRename(gg, x, tt.from, tt.to)
			requireRenameError(t, err, tt.kind, tt.msg, tt.hint)
			if tt.kind != RenameConflict && plan != nil {
				t.Errorf("PlanRename returned a plan alongside %v", err)
			}
			if tt.kind == RenameConflict {
				want := &RenamePlan{Main: c.main, Project: "store", Renames: map[string]string{"shop": "store", "shop@feat": "store@feat"}}
				if !reflect.DeepEqual(plan, want) {
					t.Errorf("conflict plan = %#v, want the intended write %#v", plan, want)
				}
			}
		})
	}

	// Renaming to the current name is a no-op, not a conflict with itself.
	requireRenamePlan(t, gg, x, "shop@feat", RenamePlan{
		Main: c.main, Project: "shop", Renames: map[string]string{},
	})
}

func TestPlanRenameConflictsOnAWorktreeName(t *testing.T) {
	c, x, gg := renameWorld(t, false)
	gg = append(gg, state.Group{Name: "market@feat", Source: state.SourceStart})
	plan, err := PlanRename(gg, x, "shop", "market")
	requireRenameError(t, err, RenameConflict, "a group named market@feat already exists", "pick another name")
	want := &RenamePlan{Main: c.main, Project: "market", Renames: map[string]string{"shop": "market", "shop@feat": "market@feat"}}
	if !reflect.DeepEqual(plan, want) {
		t.Errorf("conflict plan = %#v, want the intended write %#v", plan, want)
	}
}

func TestPlanRenameConfigOutsideACheckout(t *testing.T) {
	dir := mkdir(t, tempTree(t), "loose")
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, "name: loose\nports: [8000]\n")
	x := NewIndex()
	x.Observe(dir)
	requireRenamePlan(t, Groups(nil, x), x, "loose", RenamePlan{
		ConfigPath: path, Project: "tidy", Renames: map[string]string{"loose": "tidy"},
	})
}

func TestValidProjectName(t *testing.T) {
	refused := []struct{ name, want string }{
		{"", "the new name is empty"},
		{"a@b", `"a@b" contains @, which separates a project from its worktree`},
		{"a/b", `"a/b" contains a slash or whitespace; names are a single word`},
		{`a\b`, `"a\\b" contains a slash or whitespace; names are a single word`},
		{"a b", `"a b" contains a slash or whitespace; names are a single word`},
		{"tab\tname", "\"tab\\tname\" contains a slash or whitespace; names are a single word"},
	}
	for _, c := range refused {
		err := ValidProjectName(c.name)
		if err == nil || err.Error() != c.want {
			t.Errorf("ValidProjectName(%q) = %v, want %q", c.name, err, c.want)
		}
	}
	for _, name := range []string{"shop", "my-app", "app_2.0"} {
		if err := ValidProjectName(name); err != nil {
			t.Errorf("ValidProjectName(%q) = %v", name, err)
		}
	}
}

func TestSetConfigNameKeepsTheFile(t *testing.T) {
	dir := tempTree(t)
	path := filepath.Join(dir, ConfigName)
	writeFile(t, path, "# the shop\nname: shop # short\nservices:\n  # first\n  - name: api\n    port: 8000\n")

	cfg, err := SetConfigName(path, "market")
	if err != nil {
		t.Fatalf("SetConfigName: %v", err)
	}
	if cfg.Name != "market" {
		t.Errorf("config name = %q, want market", cfg.Name)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	const rewritten = "# the shop\nname: market # short\nservices:\n  # first\n  - name: api\n    port: 8000\n"
	if string(got) != rewritten {
		t.Errorf("rewritten file = %q, want comments and services kept around name: market", got)
	}

	bare := filepath.Join(tempTree(t), ConfigName)
	writeFile(t, bare, "services:\n  - name: api\n")
	if _, err := SetConfigName(bare, "market"); err != nil {
		t.Fatalf("SetConfigName without a name key: %v", err)
	}
	data, err := os.ReadFile(bare)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	const inserted = "name: market\nservices:\n  - name: api\n"
	if string(data) != inserted {
		t.Errorf("name not inserted first:\n%s", data)
	}

	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if _, err := SetConfigName(path, "bad name"); err == nil {
		t.Fatal("wrote a name with a space")
	} else {
		var re *RenameError
		if errors.As(err, &re) {
			t.Errorf("refused name was wrapped as *RenameError %v, want a config identity error", re)
		}
		if !strings.Contains(err.Error(), ConfigName) || !strings.Contains(err.Error(), `name "bad name" contains " "; names may not contain slashes or whitespace`) {
			t.Errorf("refused error = %q, want a message that names %s and the invalid name", err, ConfigName)
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(before) != string(after) {
		t.Error("a refused rename changed the file")
	}
}
