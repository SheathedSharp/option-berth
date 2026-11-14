package runtimesvc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/claims"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
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
	if len(p.Only) > 0 && len(p.ExpectRuns) > 0 {
		return nil, rpc.NewError(rpc.CodeInvalidParams, "only and expect_runs are mutually exclusive",
			"expect_runs names the runs to stop; only names services, whatever run each is")
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
	expect, err := expectFilter(req.Runtime, name, cfg, p.ExpectRuns)
	if err != nil {
		return nil, err
	}
	// Either selector makes the stop scoped, which is what keeps the group's
	// `port: auto` claims attached to the services that keep running.
	scoped := len(only) > 0 || len(expect) > 0

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

	var targets []killer.Target
	if len(expect) > 0 {
		targets = expectedGroupTargets(snap, name, expect)
	} else {
		targets = groupTargetsFor(snap, name, only)
	}
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
		pidTargets, runSignal, err = captureSilentGroupStop(req.Runtime, snap, name, only, expect, p.Reason, p.DryRun)
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
		DryRun: p.DryRun,
		Grace:  time.Duration(p.GraceMs) * time.Millisecond,
		Force:  p.Force,
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

// expectFilter validates the run identities a stop is allowed to touch and
// returns service → run id. Both halves are required: a service alone would
// stop a replacement run that another controller started, which is the race
// this selector exists to close. The service is still checked against the
// manifest so a typo is a caller error, not a quiet no-op.
func expectFilter(rt *Runtime, name string, cfg *groups.Config, requested []rpc.RunExpectation) (map[string]string, error) {
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
	expected := make(map[string]string, len(requested))
	for i := range requested {
		service, runID, err := expectedRun(requested[i])
		if err != nil {
			return nil, err
		}
		if !known[service] {
			return nil, rpc.NewError(rpc.CodeNotFound,
				fmt.Sprintf("service %q is not declared by group %s", service, name),
				"run `oberth status` to inspect this worktree")
		}
		if expected[service] != "" {
			return nil, rpc.NewError(rpc.CodeInvalidParams,
				fmt.Sprintf("expect_runs names service %q more than once", service),
				"send one expected run per service")
		}
		expected[service] = runID
	}
	return expected, nil
}

// expectedRun reads one expectation and refuses an incomplete one. A service
// without a run id is the name-only stop this selector replaces, so it is a
// caller error rather than a silent widening.
func expectedRun(raw rpc.RunExpectation) (string, string, error) {
	service := strings.TrimSpace(raw.Service)
	runID := strings.TrimSpace(raw.RunID)
	if service == "" {
		return "", "", rpc.NewError(rpc.CodeInvalidParams, "expect_runs contains an empty service name",
			"name the service and the run id the stop is allowed to stop")
	}
	if runID == "" {
		return "", "", rpc.NewError(rpc.CodeInvalidParams,
			fmt.Sprintf("expect_runs names service %q without a run id", service),
			"a service-only stop can reach a replacement run; name the run the caller started")
	}
	return service, runID, nil
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

func groupPIDs(runs []state.Run, only map[string]bool, expect map[string]string) []int {
	var out []int
	for _, run := range runs {
		if len(only) > 0 && !only[run.Name] {
			continue
		}
		if len(expect) > 0 && expect[run.Name] != run.ID {
			continue
		}
		if run.RootPID > 0 {
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
		target.claimProject, target.claimWorktree = claims.Identity(cfg.Dir)
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
			"run `oberth worktrees` to list the identities this runtime service has seen")
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
	project, worktree := claims.Identity(cfg.Dir)
	return groupStopTarget{
		group: group, cfg: cfg, configPath: cfg.Path,
		claimProject: project, claimWorktree: worktree,
		identity: identity, explicit: true,
	}, nil
}

// ensureUniqueGroup refuses to act on a group name that more than one checkout
// is published under. The runtime service's snapshot merges such checkouts, so a
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

// killGroupName resolves the group a groups.kill call is about: either the name
// it sent, or the group the runtime service publishes a config's services under. When
// the call named a config, that config comes back with it.
func killGroupName(rt *Runtime, p rpc.GroupsKillParams) (string, *groups.Config, error) {
	groupName := strings.TrimSpace(p.Name)
	if groupName != "" {
		return groupName, nil, nil
	}
	configPath := trimmed(p.ConfigPath)
	if configPath != "" {
		cfg, err := loadConfig(rt, configPath)
		if err != nil {
			return "", nil, err
		}
		return rt.Scanner.GroupOf(cfg), cfg, nil
	}
	return "", nil, rpc.NewError(rpc.CodeInvalidParams, "name or config_path is required",
		`send {"name": "my-app"} or {"config_path": "/repo/oberth.yaml"}`)
}
