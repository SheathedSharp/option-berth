package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/killer"
)

// guardDaemonKill is the seam every daemon executor round passes before its
// RPC. Production leaves it permissive: the client was dialled by dialDaemon
// for the daemon the user asked for, so there is no wider scope to check. The
// test binary replaces it with a guard that admits only the client instance a
// test-owned fixture registered together with an explicit Scan, so a daemon
// round whose fixture was forgotten refuses before its kill RPC instead of
// acting through an arbitrary or shared daemon. The command has already made
// its read-only snapshot call by then; it is only the signalling round that is
// gated. It takes no target, pid or selector: naming a subject is not
// ownership.
var guardDaemonKill = func(*client.Client) error { return nil }

// killThroughDaemon runs `oberth kill` as `ports.kill` / `groups.kill`.
//
// Target selection stays here, against the port table the daemon serves, so
// every message the user can see — the invalid-selector errors, "no listening
// port belongs to group X", the confirmation list, the result lines and the
// --json list — is produced by the same code as the direct path. Only the
// signalling moves: the daemon kills, then rescans and records the port going
// down, which is what the CLI's own killer could never do.
func killThroughDaemon(ctx context.Context, c *client.Client, args []string, bindIP string) error {
	snapshot, err := daemonList(ctx, c)
	if err != nil {
		return cliError(err)
	}
	targets, confirm, err := killTargets(args, snapshot, bindIP)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		return reportKill(os.Stdout, nil, snapshot, killJSONFlag, killDryRunFlag)
	}

	opts := killOptions()
	return killRequest{
		exec:     killInDaemon(c),
		targets:  targets,
		snapshot: snapshot,
		opts:     opts,
		confirm:  confirm && !killYesFlag && !killDryRunFlag,
		asJSON:   killJSONFlag,
	}.execute(ctx)
}

// killInDaemon is the executor that asks the daemon to signal and then
// rescan. The method choice is made once per round from the same selection
// the direct path would use.
func killInDaemon(c *client.Client) killExecutor {
	return func(ctx context.Context, targets []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		if err := guardDaemonKill(c); err != nil {
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

// killMethodFor picks the RPC. `-g` goes to groups.kill so the daemon that
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

// killSelectors turns the targets killTargets resolved into wire selectors. A
// selector sets exactly one of port and pid, which is what the targets already
// are: nothing here re-selects.
func killSelectors(targets []killer.Target) []rpc.Selector {
	out := make([]rpc.Selector, 0, len(targets))
	for _, t := range targets {
		var sel rpc.Selector
		switch {
		case t.RunID != "":
			id := t.RunID
			sel.RunID = &id
		case t.PID > 0:
			pid := t.PID
			sel.PID = &pid
		default:
			port := t.Port
			sel.Port = &port
			if t.BindAddress != "" {
				bind := t.BindAddress
				sel.BindAddress = &bind
			}
		}
		out = append(out, sel)
	}
	return out
}

// graceMs is --grace on the wire. Zero means "the daemon's default", which is
// the same default the killer applies locally.
func graceMs(opts killer.Options) int {
	if opts.Grace <= 0 {
		return 0
	}
	return int(opts.Grace.Milliseconds())
}

// killSession is `oberth kill --session <id>`: the session form of `-g`,
// answered by `sessions.kill` for the same reason `-g` is answered by
// `groups.kill` — the daemon owns the membership, so it does the selecting.
// There is no direct-scan path: a session only exists in the daemon.
func killSession(ctx context.Context) error {
	id, err := currentSession(killSessionFlag)
	if err != nil {
		return err
	}
	c, err := sessionsDaemon(ctx)
	if err != nil {
		return err
	}
	defer c.Close()

	snapshot, err := daemonList(ctx, c)
	if err != nil {
		return cliError(err)
	}

	return killRequest{
		exec:     killSessionInDaemon(c, id),
		snapshot: snapshot,
		opts:     killOptions(),
		confirm:  !killYesFlag && !killDryRunFlag,
		asJSON:   killJSONFlag,
		emptyPlan: func() {
			fmt.Printf("Session %s has nothing running.\n", id)
		},
	}.execute(ctx)
}

// killSessionInDaemon is the executor for the hidden session selector. It
// ignores the resolved targets: a session's membership exists only inside the
// daemon, so the daemon does the selecting.
func killSessionInDaemon(c *client.Client, id string) killExecutor {
	return func(ctx context.Context, _ []killer.Target, opts killer.Options, dryRun bool) ([]killer.Result, error) {
		if err := guardDaemonKill(c); err != nil {
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
