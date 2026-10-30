package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// handleGroupsKill stops a group's listening ports. With release it is
// `oberth down`: it also stops the runs option-berth started in the group that hold no
// port, and gives back the claims the group's `port: auto` services hold.
func handleGroupsKill(ctx context.Context, req *Request) (any, error) {
	var p rpc.GroupsKillParams
	if err := req.Bind(&p); err != nil {
		return nil, err
	}
	if p.Reason != "" && p.Reason != "stopped" && p.Reason != "ready_timeout" {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "unsupported stop reason "+p.Reason,
			"use stopped or ready_timeout")
	}
	target, err := killGroupTarget(req.Runtime, p)
	if err != nil {
		return nil, err
	}
	name, cfg := target.group, target.cfg
	// The group's lifecycle lock: a stop cannot interleave with a start's
	// spawns, and two stops take turns. The snapshot below is read under the
	// lock, so a start that just finished is already part of it.
	release, err := req.Runtime.AcquireGroup(ctx, name)
	if err != nil {
		return nil, err
	}
	defer release()
	only, err := serviceFilter(req.Runtime, name, cfg, p.Only)
	if err != nil {
		return nil, err
	}
	scoped := len(only) > 0

	var portRelease *servicePortRelease
	if p.Release && !p.DryRun && !scoped {
		if cfg == nil {
			cfg = configForGroup(req.Runtime, name)
		}
		if cfg != nil {
			portRelease, err = captureServicePortRelease(ctx, req.Runtime, cfg)
		} else if target.claimProject != "" {
			// The manifest is gone with the checkout, so services cannot be
			// enumerated. The identity ledger still names the claim key whose
			// reservations belong to this worktree.
			portRelease, err = captureWorktreePortRelease(ctx, req.Runtime,
				target.claimProject, target.claimWorktree)
		}
		if err != nil {
			return nil, storeError("capturing port reservations before stop", err)
		}
	}

	snap, err := killSnapshot(ctx, req)
	if err != nil {
		return nil, err
	}

	targets := groupTargetsFor(snap, name, only)
	var pidTargets []killer.Target
	var runSignal func(int)
	if p.Release {
		// `down` is the declarative stop: every run option-berth started in the
		// group goes, ports first and then whatever holds none. Nothing in the
		// manifest is exempt — a service the machine owns is not a service of
		// this project at all (decision 0010), so it never reaches this list.
		if cfg == nil {
			cfg = configForGroup(req.Runtime, name)
		}
		pidTargets, runSignal, err = captureSilentGroupStop(req.Runtime, snap, name, only, p.Reason, p.DryRun)
		if err != nil {
			return nil, rpc.NewError(rpc.CodeInternal, err.Error(), "inspect status before retrying down")
		}
	}
	if len(targets) == 0 && len(pidTargets) == 0 {
		if scoped {
			return killEnvelope(nil), nil
		}
		if !p.Release || (cfg == nil && portRelease == nil && !target.explicit) {
			return nil, killRPCError(&killer.CodedError{
				Code:   killer.CodeNotFound,
				Detail: "no listening port belongs to group " + name,
				Hint:   "run `oberth status` to inspect this worktree",
			})
		}
		// No signal was attempted. The fresh scan is still subject to the
		// same raw-registry and observed-reservation guards as the stop path.
		return finishGroupStop(ctx, req, name, portRelease, snap, nil, nil)
	}

	opts := killer.Options{
		Force:  p.Force,
		Grace:  time.Duration(p.GraceMs) * time.Millisecond,
		DryRun: p.DryRun,
		Ports:  killerRows(snap),
	}
	var rows []state.KillResult
	if len(targets) > 0 {
		opts.OnSignal = stopSignalObserver(req.Runtime, snap, targets, opts.DryRun, p.Reason)
		rows = killer.KillPorts(ctx, targets, opts)
	}
	if p.Release && !p.DryRun {
		// Reuse the identities and receipt captured before the port pass.
		// Never fetch a replacement generation by PID between the two passes.
		if len(pidTargets) > 0 {
			tree := opts
			tree.Tree = true
			tree.OnSignal = runSignal
			rows = append(rows, killer.KillPorts(ctx, pidTargets, tree)...)
		}
	}
	after, scanErr := afterKill(req, opts.DryRun)
	return finishGroupStop(ctx, req, name, portRelease, after, scanErr, rows)
}

// serviceFilter validates a scoped stop against the project's manifest and
// returns the selected service names. A missing or unknown service is a
// caller error rather than a silent no-op that could make restart misleading.
func serviceFilter(rt *Runtime, name string, cfg *groups.Config, requested []string) (map[string]bool, error) {
	if len(requested) == 0 {
		return nil, nil
	}
	if cfg == nil {
		cfg = configForGroup(rt, name)
	}
	if cfg == nil {
		return nil, rpc.NewError(rpc.CodeNotFound, "no manifest describes group "+name,
			"run `oberth status` to inspect this worktree")
	}
	known := make(map[string]bool, len(cfg.Services))
	for _, svc := range cfg.Services {
		known[svc.Name] = true
	}
	selected := make(map[string]bool, len(requested))
	for _, raw := range requested {
		service := strings.TrimSpace(raw)
		if service == "" {
			return nil, rpc.NewError(rpc.CodeInvalidParams, "--only contains an empty service name",
				"name one or more services from `oberth status`")
		}
		if !known[service] {
			return nil, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("service %q is not declared by group %s", service, name),
				"run `oberth status` to inspect this worktree")
		}
		selected[service] = true
	}
	return selected, nil
}

// runsWithoutPorts removes runs whose process already appears in the port
// targets. Port targets are killed in the first pass; sending the same pid
// through the run-only pass would produce a duplicate result and a second
// signal after the process had already gone away.
func runsWithoutPorts(snap state.Snapshot, name string, pids []int) []int {
	covered := map[int]bool{}
	for _, p := range snap.Ports {
		if !inSnapshotGroup(p, name) {
			continue
		}
		if p.PID > 0 {
			covered[p.PID] = true
		}
		if p.Run != nil && p.Run.RootPID > 0 {
			covered[p.Run.RootPID] = true
		}
	}
	out := make([]int, 0, len(pids))
	for _, pid := range pids {
		if !covered[pid] {
			out = append(out, pid)
		}
	}
	return out
}

func groupPIDs(runs []state.Run, only map[string]bool) []int {
	var out []int
	for _, run := range runs {
		if only[run.Name] && run.RootPID > 0 {
			out = append(out, run.RootPID)
		}
	}
	return out
}

// groupStopTarget is what one groups.kill call resolved to. cfg is nil when the
// manifest is gone; claimProject/claimWorktree still identify the reservations
// that belonged to the deleted checkout.
type groupStopTarget struct {
	group         string
	cfg           *groups.Config
	configPath    string
	claimProject  string
	claimWorktree string
	identity      string
	// explicit is set when the caller named a worktree path or identity. Such
	// a target is a real answer even when nothing is running, so `down` reports
	// an empty result instead of not_found.
	explicit bool
}

// killGroupTarget resolves every accepted way of naming a stop target. Name and
// ConfigPath are name-shaped and keep their historical behaviour. Worktree and
// Identity are the explicit entries for a checkout whose directory may be gone.
func killGroupTarget(rt *Runtime, p rpc.GroupsKillParams) (groupStopTarget, error) {
	name := strings.TrimSpace(p.Name)
	configPath := trimmed(p.ConfigPath)
	worktree := trimmed(p.WorktreePath)
	identity := trimmed(p.Identity)

	explicit := worktree != "" || identity != ""
	if explicit {
		if worktree != "" && identity != "" {
			return groupStopTarget{}, rpc.NewError(rpc.CodeInvalidParams,
				"worktree and identity are mutually exclusive",
				"send one of them; identity is the wt_ token from `oberth status --json` or `oberth worktrees`")
		}
		if name != "" || configPath != "" {
			return groupStopTarget{}, rpc.NewError(rpc.CodeInvalidParams,
				"worktree/identity cannot be combined with name or config_path",
				"name the checkout once: by path, by identity, by group name, or by config path")
		}
		return resolveExplicitStop(rt, worktree, identity)
	}

	group, cfg, err := killGroupName(rt, p)
	if err != nil {
		return groupStopTarget{}, err
	}
	target := groupStopTarget{group: group, cfg: cfg}
	if cfg != nil {
		target.configPath = cfg.Path
		target.claimProject, target.claimWorktree = claims.Identity(cfg.Dir, "", "")
		target.identity, _ = groups.WorktreeIdentityFor(cfg.Dir)
	}
	if err := ensureUniqueGroup(rt, group, target.identity); err != nil {
		return groupStopTarget{}, err
	}
	return target, nil
}

// resolveExplicitStop turns a worktree path or `${worktree}` identity into the
// group that carries its services. When the directory still exists the manifest
// is re-read (the most precise target, and the only one that can name every
// `port: auto` service); when it is gone, the identity ledger supplies the group
// and claim key recorded before deletion.
func resolveExplicitStop(rt *Runtime, worktree, identity string) (groupStopTarget, error) {
	if rt.Store == nil {
		return groupStopTarget{}, errNoStore()
	}
	var (
		rec   store.WorktreeIdentity
		found bool
		err   error
	)
	root := worktree
	if identity != "" {
		rec, found, err = rt.Store.WorktreeIdentity(identity)
	} else {
		if root, err = store.CleanRoot(root); err != nil {
			return groupStopTarget{}, rpc.NewError(rpc.CodeInvalidParams,
				"the worktree path is not usable: "+err.Error(), "pass the checkout root")
		}
		rec, found, err = rt.Store.WorktreeIdentityByRoot(root)
		if err == nil && !found {
			rec, found, err = rt.Store.WorktreeIdentity(groups.WorktreeToken(root))
		}
	}
	if err != nil {
		return groupStopTarget{}, storeError("reading the worktree identity", err)
	}

	// A checkout that still exists is read through its manifest. A manifest
	// that can no longer be parsed is not a reason to refuse the stop: the
	// recorded group still names the services that were started from it.
	if found && rec.ConfigPath != "" {
		if info, statErr := os.Stat(rec.ConfigPath); statErr == nil && !info.IsDir() {
			cfg, cfgErr := reloadConfig(rt, rec.ConfigPath)
			if cfgErr != nil {
				if strings.TrimSpace(rec.Group) == "" {
					return groupStopTarget{}, cfgErr
				}
			} else {
				return targetFromConfig(rt, cfg)
			}
		}
	}
	if !found && root != "" {
		if cfg := configAtDir(rt, root); cfg != nil {
			return targetFromConfig(rt, cfg)
		}
	}
	if !found {
		named := identity
		if named == "" {
			named = groups.WorktreeToken(root)
		}
		detail := "no known worktree identity " + named
		if root != "" {
			detail += " (" + root + ")"
		}
		return groupStopTarget{}, rpc.NewError(rpc.CodeNotFound, detail,
			"run `oberth worktrees` to list the identities this daemon has seen")
	}
	if strings.TrimSpace(rec.Group) == "" {
		return groupStopTarget{}, rpc.NewError(rpc.CodeNotFound,
			"worktree identity "+rec.Identity+" has no recorded group at "+rec.Root,
			"the checkout was recorded without a manifest; there is no service group to stop")
	}
	if err := ensureUniqueGroup(rt, rec.Group, rec.Identity); err != nil {
		return groupStopTarget{}, err
	}
	return groupStopTarget{
		group: rec.Group, configPath: rec.ConfigPath,
		claimProject: rec.Project, claimWorktree: rec.Worktree,
		identity: rec.Identity, explicit: true,
	}, nil
}

func targetFromConfig(rt *Runtime, cfg *groups.Config) (groupStopTarget, error) {
	group := rt.Scanner.GroupOf(cfg)
	identity, _ := groups.WorktreeIdentityFor(cfg.Dir)
	if err := ensureUniqueGroup(rt, group, identity); err != nil {
		return groupStopTarget{}, err
	}
	project, worktree := claims.Identity(cfg.Dir, "", "")
	return groupStopTarget{
		group: group, cfg: cfg, configPath: cfg.Path,
		claimProject: project, claimWorktree: worktree,
		identity: identity, explicit: true,
	}, nil
}

// ensureUniqueGroup refuses to act on a group name that more than one checkout
// is published under. The daemon's snapshot merges such checkouts, so a
// by-name stop would reach both; naming one by path or identity cannot make
// group-scoped process selection sharper than the published group.
func ensureUniqueGroup(rt *Runtime, group, self string) error {
	if group == "" {
		return nil
	}
	roots := map[string]string{}
	if self != "" {
		roots[self] = ""
	}
	add := func(identity, root string) {
		if identity != "" {
			if _, ok := roots[identity]; !ok {
				roots[identity] = root
			}
		}
	}
	if rt.Store != nil {
		rows, err := rt.Store.WorktreeIdentityGroup(group)
		if err != nil {
			return storeError("checking which checkout owns group "+group, err)
		}
		for _, row := range rows {
			add(row.Identity, row.Root)
		}
	}
	if rt.Scanner != nil {
		for _, cfg := range rt.Scanner.Configs() {
			if rt.Scanner.GroupOf(cfg) != group {
				continue
			}
			identity, root := groups.WorktreeIdentityFor(cfg.Dir)
			add(identity, root)
		}
	}
	if len(roots) <= 1 {
		return nil
	}
	identities := make([]string, 0, len(roots))
	for identity := range roots {
		identities = append(identities, identity)
	}
	sort.Strings(identities)
	detail := "group " + group + " is shared by " + strings.Join(identities, ", ")
	hint := "stop the processes by port or pid; a group-scoped stop would also reach the other checkout"
	return rpc.NewError(rpc.CodeAmbiguous, detail, hint)
}

func trimmed(v *string) string {
	if v == nil {
		return ""
	}
	return strings.TrimSpace(*v)
}

// loadConfig re-reads one manifest into the index. A file that no longer exists
// or no longer parses is reported with the config error, not as a group miss.
func loadConfig(rt *Runtime, path string) (*groups.Config, error) {
	if rt.Scanner == nil {
		return nil, rpc.NewError(rpc.CodeNotFound, "no scanner is available to read "+path, "")
	}
	cfg, ok := rt.Scanner.ConfigAt(path)
	if !ok {
		if err := rt.Scanner.LoadConfig(path); err != nil {
			return nil, configError(path, err)
		}
		if cfg, ok = rt.Scanner.ConfigAt(path); !ok {
			return nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+path, "")
		}
	}
	return cfg, nil
}

// reloadConfig re-reads one manifest even when the index already holds a parsed
// copy. An explicit stop is worth a fresh read: the services named there decide
// which claims are released.
func reloadConfig(rt *Runtime, path string) (*groups.Config, error) {
	if rt.Scanner == nil {
		return nil, rpc.NewError(rpc.CodeNotFound, "no scanner is available to read "+path, "")
	}
	if err := rt.Scanner.LoadConfig(path); err != nil {
		return nil, configError(path, err)
	}
	cfg, ok := rt.Scanner.ConfigAt(path)
	if !ok {
		return nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+path, "")
	}
	return cfg, nil
}

// configAtDir is loadConfig for the manifest at a checkout root, accepting the
// legacy file name too.
func configAtDir(rt *Runtime, dir string) *groups.Config {
	for _, name := range []string{groups.ConfigName, groups.LegacyConfigName} {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		if cfg, err := loadConfig(rt, path); err == nil {
			return cfg
		}
	}
	return nil
}

// killGroupName is the group a groups.kill call is about: the name it sent, or
// the group the daemon publishes a config's services under. The config comes
// back too when the call named one.
func killGroupName(rt *Runtime, p rpc.GroupsKillParams) (string, *groups.Config, error) {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name, nil, nil
	}
	if path := trimmed(p.ConfigPath); path != "" {
		cfg, err := loadConfig(rt, path)
		if err != nil {
			return "", nil, err
		}
		return rt.Scanner.GroupOf(cfg), cfg, nil
	}
	return "", nil, rpc.NewError(rpc.CodeInvalidParams, "name or config_path is required",
		`send {"name": "my-app"} or {"config_path": "/repo/oberth.yaml"}`)
}
