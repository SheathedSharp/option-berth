package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// noDaemonFlag is the persistent --no-daemon flag: it forces the direct scan
// path for every read command, without the fallback note.
var noDaemonFlag bool

// fallbackNoteOnce keeps the "daemon unavailable" note to one line per
// invocation, however many times a command reaches for the daemon (spec,
// "Error handling").
var fallbackNoteOnce sync.Once

func noteFallback() {
	fallbackNoteOnce.Do(func() {
		fmt.Fprintln(os.Stderr, "note: daemon unavailable, using direct scan")
	})
}

// dialDaemon is the seam the CLI tests replace. It connects to a daemon that is
// already listening and never starts one: every command routed through here
// works without a daemon (spec, "CLI surface": `list`, `info`, `next`, `wait`,
// `logs`, `graph` and `watch` all say "needs daemon: no"), so spawning a
// background process behind the user's back would buy nothing they did not ask
// for. `oberth serve`, the desktop app, and the commands that genuinely need the
// daemon are what start it, and those autostart with `oberth serve --detach`
// (contract §7).
var dialDaemon = func(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{
		Name:    "cli",
		Version: buildinfo.Version,
	})
}

// daemonClient returns a connected client, or nil when the caller should fall
// back to a direct scan. A nil result with --no-daemon is silent; anything else
// prints the one-line note first.
func daemonClient(ctx context.Context) *client.Client {
	if noDaemonFlag {
		return nil
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		noteFallback()
		return nil
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
	if noDaemonFlag {
		return false
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// daemonList reads the worktree-scoped live snapshot through the daemon and
// converts its evidence rows back to the scanner shape used by kill and logs.
// The old whole-machine ports.list endpoint is intentionally gone.
func daemonList(ctx context.Context, c *client.Client) ([]ports.ListeningPort, error) {
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		return nil, err
	}
	return state.ToListeningAll(snap.Ports), nil
}

// listInclude maps the CLI's --stats / --health onto the wire's include.
func listInclude(stats, health bool) rpc.Include {
	inc := rpc.Include{}
	if stats {
		inc = append(inc, "stats")
	}
	if health {
		inc = append(inc, "health")
	}
	return inc
}

// strPtrOrNil is the helper the params builders share: an empty string means
// "no filter", which the wire spells as an absent field.
func strPtrOrNil(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
