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

func renameKind(err error) (RenameErrorKind, bool) {
	var re *RenameError
	if errors.As(err, &re) {
		return re.Kind, true
	}
	return 0, false
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
	_, x, gg := renameWorld(t, false)

	tests := []struct {
		name, from, to string
		want           RenameErrorKind
	}{
		{"unknown group", "nope", "market", RenameNotFound},
		{"collides with another project", "shop", "store", RenameConflict},
		{"a run's --group", "started", "market", RenameRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := PlanRename(gg, x, tt.from, tt.to)
			kind, ok := renameKind(err)
			if !ok || kind != tt.want {
				t.Fatalf("err = %v, want kind %d", err, tt.want)
			}
		})
	}

	// Renaming to the current name is a no-op, not a conflict with itself.
	plan, err := PlanRename(gg, x, "shop@feat", "shop")
	if err != nil || len(plan.Renames) != 0 {
		t.Fatalf("same name = %+v, %v", plan, err)
	}
}

func TestPlanRenameConflictsOnAWorktreeName(t *testing.T) {
	_, x, gg := renameWorld(t, false)
	gg = append(gg, state.Group{Name: "market@feat", Source: state.SourceStart})
	if _, err := PlanRename(gg, x, "shop", "market"); err == nil {
		t.Fatal("renaming onto an existing <project>@<worktree> succeeded")
	} else if kind, _ := renameKind(err); kind != RenameConflict {
		t.Fatalf("err = %v, want a conflict", err)
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
	for _, name := range []string{"", "a@b", "a/b", `a\b`, "a b", "tab\tname"} {
		if ValidProjectName(name) == nil {
			t.Errorf("ValidProjectName(%q) accepted it", name)
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
		t.Errorf("config name = %q", cfg.Name)
	}
	data, _ := os.ReadFile(path)
	got := string(data)
	for _, want := range []string{"# the shop", "name: market # short", "# first", "port: 8000"} {
		if !strings.Contains(got, want) {
			t.Errorf("file lost %q:\n%s", want, got)
		}
	}

	// A file with no name: key gets one, at the top.
	bare := filepath.Join(tempTree(t), ConfigName)
	writeFile(t, bare, "services:\n  - name: api\n")
	if _, err := SetConfigName(bare, "market"); err != nil {
		t.Fatalf("SetConfigName without a name key: %v", err)
	}
	data, _ = os.ReadFile(bare)
	if !strings.HasPrefix(string(data), "name: market\n") {
		t.Errorf("name not inserted first:\n%s", data)
	}

	// A name that does not validate leaves the file alone.
	before, _ := os.ReadFile(path)
	if _, err := SetConfigName(path, "bad name"); err == nil {
		t.Fatal("wrote a name with a space")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Error("a refused rename changed the file")
	}
}
