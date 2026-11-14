package runtimesvc

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/scanner"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// `groups.rename` renames a project and, along with it, every checkout of that
// project: the main checkout's group and each linked worktree's
// `<project>@<wt>`. The name stays where the project already keeps it — the
// `name:` of the main checkout's `oberth.yaml` — or, when a project has no
// file, in the runtime service's store, keyed by the main checkout's root.
func init() {
	registerRenameHandler()
}

func registerRenameHandler() {
	RegisterHandler("groups.rename", handleGroupsRename)
}

var (
	groupRenameMu    sync.RWMutex
	groupRenameHooks []func(renames map[string]string)
)

// OnGroupRename registers a function told, old name → new, about every group a
// `groups.rename` renamed. State that recorded a group name at creation time
// follows the rename through this hook — a run's group does, from
// internal/runtimesvc/runsreg — without this package importing that owner.
func OnGroupRename(f func(renames map[string]string)) {
	if f == nil {
		return
	}
	groupRenameMu.Lock()
	groupRenameHooks = append(groupRenameHooks, f)
	groupRenameMu.Unlock()
}

func notifyGroupRename(renames map[string]string) {
	if len(renames) == 0 {
		return
	}
	for _, f := range groupRenameHooksSnapshot() {
		f(renames)
	}
}

func groupRenameHooksSnapshot() []func(map[string]string) {
	groupRenameMu.RLock()
	defer groupRenameMu.RUnlock()
	return append([]func(map[string]string){}, groupRenameHooks...)
}

func handleGroupsRename(_ context.Context, req *Request) (any, error) {
	var p rpc.GroupsRenameParams
	err := req.Bind(&p)
	if err != nil {
		return nil, err
	}
	to, from := strings.TrimSpace(p.To), strings.TrimSpace(p.Name)
	if from == "" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "name is required",
			`send {"name": "<project or checkout group>", "to": "<new project name>"}`)
	}
	if err = groups.ValidProjectName(to); err != nil {
		return nil, rpc.NewError(rpc.CodeInvalidParams, err.Error(),
			"a project name is one word with no @, / or whitespace; a worktree's group adds @<worktree> to it")
	}

	rt := req.HostContext
	snapshot, err := rt.Scanner.Snapshot(scanner.Include{})
	if err != nil {
		return nil, scanFailure(err)
	}
	local := make([]state.Group, 0, len(snapshot.Groups))
	for i := range snapshot.Groups {
		if state.IsLocalhost(snapshot.Groups[i].Host) {
			local = append(local, snapshot.Groups[i])
		}
	}
	plan, err := rt.Scanner.PlanGroupRename(local, from, to)
	if err != nil {
		return nil, renameError(err)
	}
	if len(plan.Renames) == 0 {
		// The project already carries that name: nothing to write, nothing to
		// publish.
		return rpc.GroupsRenameResult{
			MutationResult: rpc.MutationResult{OK: true, Affected: []string{}},
			Name:           plan.Project,
		}, nil
	}

	if plan.ConfigPath == "" {
		db := rt.Store
		if db == nil {
			return nil, errNoStore()
		}
		if err = db.SetGroupAlias(plan.Main, to); err != nil {
			return nil, storeError("saving the project name", err)
		}
	} else {
		renamed, err := groups.SetConfigName(plan.ConfigPath, to)
		if err != nil {
			return nil, configError(plan.ConfigPath, err)
		}
		if err = rt.Scanner.LoadConfig(renamed.Path); err != nil {
			rt.Logger.Warn("reloading a config after renaming its project", "path", renamed.Path, "error", err)
		}
		// From here on the file names the project. An alias stored before that
		// file existed would still outrank it, so it is dropped.
		if st := rt.Store; st != nil && plan.Main != "" {
			if err := st.ClearGroupAlias(plan.Main); err != nil {
				rt.Logger.Warn("clearing a project alias", "root", plan.Main, "error", err)
			}
		}
	}

	notifyGroupRename(plan.Renames)
	rt.Logger.Info("project renamed", "from", from, "to", to, "groups", len(plan.Renames))
	// Publishing happens before the reply, as for every other write: the delta
	// removing the old names and adding the new ones is queued ahead of this
	// response.
	republish(rt)

	names := make([]string, 0, len(plan.Renames))
	for _, renamed := range plan.Renames {
		names = append(names, renamed)
	}
	sort.Strings(names)
	return rpc.GroupsRenameResult{
		Name:           plan.Project,
		MutationResult: rpc.MutationResult{OK: true, Affected: names},
	}, nil
}

// renameError maps a refused rename onto the contract's error registry: the
// details (message and hint) belong to the groups package, only the code is
// decided here.
func renameError(err error) error {
	var refusal *groups.RenameError
	if !errors.As(err, &refusal) {
		return rpc.NewError(rpc.CodeInternal, err.Error(), "check `oberth runtime service log`")
	}
	return renameKindError(refusal)
}

func renameKindError(refusal *groups.RenameError) error {
	switch refusal.Kind {
	case groups.RenameConflict:
		return rpc.NewError(rpc.CodeConflict, refusal.Msg, refusal.Hint)
	case groups.RenameNotFound:
		return rpc.NewError(rpc.CodeNotFound, refusal.Msg, refusal.Hint)
	}
	return rpc.NewError(rpc.CodeInvalidParams, refusal.Msg, refusal.Hint)
}
