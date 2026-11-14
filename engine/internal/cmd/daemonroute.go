package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// noDaemonFlag is the persistent --no-daemon flag. It pins every read command
// to the direct scan path and drops the fallback note with it.
var noDaemonFlag bool

// fallbackNoteOnce keeps the "daemon unavailable" note to one line per
// invocation, no matter how many times a command reaches for the daemon (spec,
// "Error handling").
var fallbackNoteOnce sync.Once

func noteFallback() {
	fallbackNoteOnce.Do(func() {
		fmt.Fprintln(os.Stderr, display.Dim("note: daemon unavailable, using direct scan"))
	})
}

// fallbackCauseOnce is noteFallback for a daemon that answered with an error.
// The cause is printed, not swallowed: "the daemon could not scan" and "there
// is no daemon" are different facts.
var fallbackCauseOnce sync.Once

func noteFallbackBecause(err error) {
	fallbackCauseOnce.Do(func() {
		_, msg, _ := describe(err)
		fmt.Fprintf(os.Stderr, "%s\n", display.Dim("note: daemon failed ("+msg+"), using direct scan"))
	})
}

// dialDaemon is the seam the CLI tests replace. It connects to a daemon that is
// already listening and never starts one: every command routed through here
// works without a daemon — `status --no-daemon` and the `git` commands are the
// clear cases — so spawning a background process behind the user's back would
// buy nothing they did not ask for. `oberth serve` and the commands that
// genuinely need the daemon are what start it, and those autostart with
// `oberth serve --detach`
var dialDaemon = func(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{
		Name:    "cli",
		Version: buildinfo.Version,
	})
}

// dialOrNil returns a connected client, or nil when the caller should fall
// back to a direct scan. --no-daemon skips the dial entirely; a failed dial is
// silent here because whether it deserves a note depends on what the caller
// does next.
func dialOrNil(ctx context.Context) *client.Client {
	if noDaemonFlag {
		return nil
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		return nil
	}
	return c
}

// daemonClient returns a connected client, or nil when the caller should fall
// back to a direct scan. A nil result with --no-daemon is silent; a daemon that
// could not be reached prints the one-line note first.
func daemonClient(ctx context.Context) *client.Client {
	c := dialOrNil(ctx)
	if c == nil && !noDaemonFlag {
		noteFallback()
	}
	return c
}

// daemonUp reports whether a daemon is listening, without the note
// `daemonClient` prints on the way past.
//
// It is for a command that has nothing to fall back to: "daemon unavailable,
// using direct scan" is a useful sentence for `list`, and a false one for a
// caller about to say that the daemon is required.
func daemonUp(ctx context.Context) bool {
	c := dialOrNil(ctx)
	if c == nil {
		return false
	}
	c.Close()
	return true
}

// daemonList reads the worktree-scoped live snapshot through the daemon and
// converts its evidence rows back to the scanner shape used by kill and logs.
// The old whole-machine ports.list endpoint is intentionally gone.
//
// Every command that reads it acts on one machine: this one. `--host` and the
// registered-remote-host machinery it used to reach are gone
// (docs/history/scope.md, 2026-09-22). The product starts the projects on the
// machine it runs on. The wire carries no host field either: a field every
// handler ignored was indistinguishable from one that worked, so it was
// withdrawn rather than kept and filled with "local".
func daemonList(ctx context.Context, c *client.Client) ([]ports.ListeningPort, error) {
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		return nil, err
	}
	return state.ToListeningAll(snap.Ports), nil
}

// listInclude maps the CLI's --stats / --health onto the wire's include list.
func listInclude(stats, health bool) rpc.Include {
	include := rpc.Include{}
	if stats {
		include = append(include, "stats")
	}
	if health {
		include = append(include, "health")
	}
	return include
}

// strPtrOrNil is the helper the params builders share: an empty string means
// "no filter", which the wire spells as an absent field.
func strPtrOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
