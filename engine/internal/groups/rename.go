package groups

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// RenamePlan is what renaming a project changes.
//
// The rename always applies to the project, whichever checkout named it: the
// main checkout's group becomes Project, and every linked worktree's group
// becomes `<Project>@<worktree>`.
type RenamePlan struct {
	// Project is the requested new name.
	Project string
	// Main is the main checkout's root, the key an alias is stored under; empty
	// when the project is an `oberth.yaml` that is not a checkout's root.
	Main string
	// ConfigPath is the `oberth.yaml` that receives the new `name:`. Empty means
	// the project has no file of its own, so the name is stored as an alias of
	// Main.
	ConfigPath string
	// Renames maps each group name that changes to its new name; empty when the
	// project already carries the requested one.
	Renames map[string]string
}

// RenameErrorKind tells why a rename cannot be done.
type RenameErrorKind int

const (
	// RenameNotFound means no group carries the name the caller gave.
	RenameNotFound RenameErrorKind = iota
	// RenameRefused means the group exists, but its name is not the project's
	// to change: a pin or a run's --group names it, or nothing does.
	RenameRefused
	// RenameConflict means a group outside the project already carries a name
	// this rename would hand out.
	RenameConflict
)

// RenameError is a rename that cannot be done.
type RenameError struct {
	Kind RenameErrorKind // why the rename cannot be done
	Msg  string          // the sentence shown to the user
	Hint string          // what to do instead, when there is something
}

func (e *RenameError) Error() string { return e.Msg }

// ValidProjectName checks a new project name against the group-name rules and
// one more: it refuses `@`, the character separating a project from its
// worktree in a checkout's group name.
func ValidProjectName(name string) error {
	if name == "" {
		return fmt.Errorf("the new name is empty")
	}
	if strings.ContainsRune(name, '@') {
		return fmt.Errorf("%q contains @, which separates a project from its worktree", name)
	}
	if strings.ContainsAny(name, "/\\ \t\n\r") {
		return fmt.Errorf("%q contains a slash or whitespace; names are a single word", name)
	}
	return nil
}

// PlanRename works out what renaming the group from to `to` means, given the
// published groups gg and the index behind them. Nothing is changed here: the
// caller writes the file or the alias and republishes.
func PlanRename(gg []state.Group, x *Index, from, to string) (*RenamePlan, error) {
	group := publishedGroup(gg, from)
	if group == nil {
		return nil, &RenameError{Kind: RenameNotFound,
			Msg: "no project named " + from, Hint: "run `oberth status` inside its worktree"}
	}
	co, inRepo := groupCheckout(group)
	if inRepo && group.Name == x.CheckoutName(co) {
		return planProject(gg, x, co.Main, to)
	}

	// Not a checkout's own group: whatever named it is not the project.
	if group.Source == state.SourceStart {
		return nil, &RenameError{Kind: RenameRefused,
			Msg:  from + " is named by the --group of the `oberth start` run in it, not by a project",
			Hint: "restart the run with `oberth start --group " + to + "`"}
	}
	if plan, ok := configPlan(x, group, inRepo, co, from, to); ok {
		return plan, conflicts(gg, plan)
	}
	if inRepo && co.Linked() {
		return nil, &RenameError{
			Kind: RenameRefused,
			Msg:  from + " belongs to a project nested in worktree " + co.Worktree,
			Hint: "rename it from the main checkout's copy of its " + ConfigName,
		}
	}
	return nil, &RenameError{
		Kind: RenameRefused,
		Msg:  from + " is neither a git checkout nor a " + ConfigName + " project, so it has no name to change",
		Hint: "Compose names its own projects; `option-berth assign` pins ports to a group you choose",
	}
}

// publishedGroup is the group with this name, or nil.
func publishedGroup(gg []state.Group, name string) *state.Group {
	for i := range gg {
		if gg[i].Name == name {
			return &gg[i]
		}
	}
	return nil
}

// groupCheckout is the checkout a published group lives in.
func groupCheckout(group *state.Group) (Checkout, bool) {
	if group.RootDir == nil {
		return Checkout{}, false
	}
	return Locate(*group.RootDir)
}

// configPlan renames a `oberth.yaml` project that is not a checkout's root:
// nested in one, or outside git altogether. Its file is its name. A worktree
// copy of a committed file is not the project, so it has no plan.
func configPlan(x *Index, group *state.Group, inRepo bool, co Checkout, from, to string) (*RenamePlan, bool) {
	if group.ConfigPath == nil || (inRepo && co.Linked()) {
		return nil, false
	}
	cfg, ok := x.ByPath(*group.ConfigPath)
	if !ok || x.GroupOf(cfg) != group.Name {
		return nil, false
	}
	plan := &RenamePlan{ConfigPath: cfg.Path, Project: to, Renames: map[string]string{}}
	if from != to {
		plan.Renames[from] = to
	}
	return plan, true
}

// planProject renames the project rooted at main, and with it every checkout
// group of that project currently published.
func planProject(gg []state.Group, x *Index, main, to string) (*RenamePlan, error) {
	plan := &RenamePlan{Main: main, Project: to, Renames: map[string]string{}}
	if current := x.At(main); current != nil {
		plan.ConfigPath = current.Path
	}
	if x.ProjectName(main) == to {
		return plan, nil
	}
	for _, published := range gg {
		co, ok := publishedCheckout(published, x, main)
		if !ok {
			continue
		}
		next := to
		if co.Linked() {
			next = to + "@" + co.Worktree
		}
		plan.Renames[published.Name] = next
	}
	return plan, conflicts(gg, plan)
}

// publishedCheckout is the checkout of one published group, when that group is
// a checkout of main's project under its current name.
func publishedCheckout(group state.Group, x *Index, main string) (Checkout, bool) {
	if group.RootDir == nil {
		return Checkout{}, false
	}
	co, ok := Locate(*group.RootDir)
	if !ok || co.Main != main || group.Name != x.CheckoutName(co) {
		return Checkout{}, false
	}
	return co, true
}

// conflicts refuses a rename that would give a group a name some group outside
// the rename already has. The project name itself is checked even when the
// main checkout is not published right now, because it will be.
func conflicts(gg []state.Group, plan *RenamePlan) error {
	taken := make(map[string]bool, len(gg))
	for _, group := range gg {
		if _, moving := plan.Renames[group.Name]; !moving {
			taken[group.Name] = true
		}
	}
	next := slices.AppendSeq([]string{plan.Project}, maps.Values(plan.Renames))
	slices.Sort(next)
	for _, name := range next {
		if taken[name] {
			return &RenameError{Kind: RenameConflict,
				Msg: "a group named " + name + " already exists", Hint: "pick another name"}
		}
	}
	return nil
}
