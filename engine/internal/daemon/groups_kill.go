package daemon

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/sheathedsharp/option-berth/internal/killer"
	"github.com/sheathedsharp/option-berth/internal/state"
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
	name, cfg, err := killGroupName(req.Runtime, p)
	if err != nil {
		return nil, err
	}
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
		portRelease, err = captureServicePortRelease(ctx, req.Runtime, cfg)
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
		if !p.Release || cfg == nil {
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

// killGroupName is the group a groups.kill call is about: the name it sent, or
// the group the daemon publishes a config's services under. The config comes
// back too when the call named one.
func killGroupName(rt *Runtime, p rpc.GroupsKillParams) (string, *groups.Config, error) {
	if name := strings.TrimSpace(p.Name); name != "" {
		return name, nil, nil
	}
	if p.ConfigPath != nil && strings.TrimSpace(*p.ConfigPath) != "" {
		path := strings.TrimSpace(*p.ConfigPath)
		cfg, ok := rt.Scanner.ConfigAt(path)
		if !ok {
			if err := rt.Scanner.LoadConfig(path); err != nil {
				return "", nil, configError(path, err)
			}
			if cfg, ok = rt.Scanner.ConfigAt(path); !ok {
				return "", nil, rpc.NewError(rpc.CodeNotFound, "no usable "+groups.ConfigName+" at "+path, "")
			}
		}
		return rt.Scanner.GroupOf(cfg), cfg, nil
	}
	return "", nil, rpc.NewError(rpc.CodeInvalidParams, "name or config_path is required",
		`send {"name": "my-app"} or {"config_path": "/repo/oberth.yaml"}`)
}
