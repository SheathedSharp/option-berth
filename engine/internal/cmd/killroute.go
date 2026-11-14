package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/sheathedsharp/option-berth/engine/internal/killer"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// guardRuntimeServiceKill is the seam every runtime service executor round passes before its
// RPC. Production leaves it permissive: the client was dialled by dialRuntimeService
// for the runtime service the user asked for, so there is no wider scope to check. The
// test binary replaces it with a guard that admits only the client instance a
// test-owned fixture registered together with an explicit Scan, so a runtime service
// round whose fixture was forgotten refuses before its kill RPC instead of
// acting through an arbitrary or shared runtime.service. The command has already made
// its read-only snapshot call by then; it is only the signalling round that is
// gated. It takes no target, pid or selector: naming a subject is not
// ownership.
var guardRuntimeServiceKill = func(*client.Client) error { return nil }

// killThroughRuntimeService runs `oberth kill` as `ports.kill` / `groups.kill`.
//
// Selection runs here, against the table the runtime service serves, so the sentences a
// user sees — invalid selector, "no listening port belongs to group X", the
// confirmation list, result lines and the --json list — come from the same code
// as the direct path. Only signalling moves: the runtime service kills, rescans and
// records the port going down, which the CLI's own killer could not do.
func killThroughRuntimeService(ctx context.Context, conn *client.Client, args []string, bindIP string) error {
	rows, err := runtimeServiceList(ctx, conn)
	if err != nil {
		return cliError(err)
	}
	targets, confirm, err := killTargets(args, rows, bindIP)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return reportKill(os.Stdout, nil, rows, killJSONFlag, killDryRunFlag)
	}

	opts := killOptions()
	return killRequest{
		exec:     killInRuntimeService(conn),
		targets:  targets,
		snapshot: rows,
		opts:     opts,
		confirm:  confirm && !killYesFlag && !killDryRunFlag,
		asJSON:   killJSONFlag,
	}.execute(ctx)
}

// killInRuntimeService is the executor that asks the runtime service to signal and then
// rescan. The method choice is made once per round from the same selection
// the direct path would use.
func killInRuntimeService(c *client.Client) killExecutor {
	return func(ctx context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		if err := guardRuntimeServiceKill(c); err != nil {
			return nil, err
		}
		switch killMethodFor(killGroupFlag, opts) {
		case "groups.kill":
			var env rpc.KillEnvelope
			err := c.Call(ctx, "groups.kill", rpc.GroupsKillParams{
				Name:    killGroupFlag,
				Force:   opts.Force,
				GraceMs: graceMs(opts),
				DryRun:  dryRun,
			}, &env)
			return env.Results, err
		default:
			var env rpc.KillEnvelope
			err := c.Call(ctx, "ports.kill", rpc.PortsKillParams{
				Targets:  killSelectors(targets),
				Tree:     opts.Tree,
				Force:    opts.Force,
				GraceMs:  graceMs(opts),
				Escalate: opts.Escalate,
				DryRun:   dryRun,
			}, &env)
			return env.Results, err
		}
	}
}

// killMethodFor picks the RPC. `-g` goes to groups.kill so the runtime service that
// owns the snapshot resolves the group itself; the flags groups.kill has no
// field for (--tree, --no-escalate) send the group's members to ports.kill,
// which supports them and acts on exactly the targets killTargets already
// selected.
func killMethodFor(group string, opts killer.Options) string {
	if group != "" && !opts.Tree && opts.Escalate == nil {
		return "groups.kill"
	}
	return "ports.kill"
}

// killSelectors turns resolved targets into wire selectors. A selector carries
// exactly one of port and pid — the targets are already that shape, so nothing
// is re-selected here.
func killSelectors(resolved []killer.Target) []rpc.Selector {
	selectors := make([]rpc.Selector, 0, len(resolved))
	for _, target := range resolved {
		var selector rpc.Selector
		switch {
		case target.RunID != "":
			runID := target.RunID
			selector.RunID = &runID
		case target.PID > 0:
			pid := target.PID
			selector.PID = &pid
		default:
			port := target.Port
			selector.Port = &port
			if target.BindAddress != "" {
				bind := target.BindAddress
				selector.BindAddress = &bind
			}
		}
		selectors = append(selectors, selector)
	}
	return selectors
}

// graceMs is --grace on the wire. Zero means "the runtime service's default", the same
// default the killer applies locally.
func graceMs(opts killer.Options) int {
	if opts.Grace > 0 {
		return int(opts.Grace.Milliseconds())
	}
	return 0
}

// killSession is `oberth kill --session <id>`, the session form of `-g`. It is
// answered by `sessions.kill` for the same reason `-g` is answered by
// `groups.kill`: the runtime service owns the membership and therefore selects. No
// direct-scan path exists, because a session only exists in the runtime.service.
func killSession(ctx context.Context) error {
	sessionID, err := currentSession(killSessionFlag)
	if err != nil {
		return err
	}
	conn, err := sessionsRuntimeService(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	snapshot, err := runtimeServiceList(ctx, conn)
	if err != nil {
		return cliError(err)
	}

	return killRequest{
		exec:     killSessionInRuntimeService(conn, sessionID),
		snapshot: snapshot,
		opts:     killOptions(),
		confirm:  !killYesFlag && !killDryRunFlag,
		asJSON:   killJSONFlag,
		emptyPlan: func() {
			fmt.Printf("Session %s has nothing running.\n", sessionID)
		},
	}.execute(ctx)
}

// killSessionInRuntimeService is the executor for the hidden session selector. It
// ignores the resolved targets: a session's membership exists only inside the
// runtime service, so the runtime service does the selecting.
func killSessionInRuntimeService(c *client.Client, id string) killExecutor {
	return func(ctx context.Context, _ []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		if err := guardRuntimeServiceKill(c); err != nil {
			return nil, err
		}
		var env rpc.KillEnvelope
		err := c.Call(ctx, "sessions.kill", rpc.SessionsKillParams{
			ID:     id,
			Tree:   opts.Tree,
			Force:  opts.Force,
			DryRun: dryRun,
		}, &env)
		return env.Results, err
	}
}
