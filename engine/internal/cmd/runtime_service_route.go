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

// noRuntimeServiceFlag is the persistent --no-runtime-service flag. It pins every read command
// to the direct scan path and drops the fallback note with it.
var noRuntimeServiceFlag bool

// fallbackNoteOnce keeps the "runtime service unavailable" note to one line per
// invocation, no matter how many times a command reaches for the runtime service (spec,
// "Error handling").
var fallbackNoteOnce sync.Once

func noteFallback() {
	fallbackNoteOnce.Do(func() {
		fmt.Fprintln(os.Stderr, display.Dim("note: runtime service unavailable, using direct scan"))
	})
}

// fallbackCauseOnce is noteFallback for a runtime service that answered with an error.
// The cause is printed, not swallowed: "the runtime service could not scan" and "there
// is no runtime service" are different facts.
var fallbackCauseOnce sync.Once

func noteFallbackBecause(err error) {
	fallbackCauseOnce.Do(func() {
		_, msg, _ := describe(err)
		fmt.Fprintf(os.Stderr, "%s\n", display.Dim("note: runtime service failed ("+msg+"), using direct scan"))
	})
}

// dialRuntimeService is the seam the CLI tests replace. It connects to a runtime service that is
// already listening and never starts one: every command routed through here
// works without a runtime service — `status --no-runtime-service` and the `git` commands are the
// clear cases — so spawning a background process behind the user's back would
// buy nothing they did not ask for. `oberth runtime service run` and the commands that
// genuinely need the runtime service are what start it, and those autostart with
// `oberth runtime service run --detach`
var dialRuntimeService = func(ctx context.Context) (*client.Client, error) {
	return client.Dial(ctx, client.ClientInfo{
		Name:    "cli",
		Version: buildinfo.Version,
	})
}

// dialOrNil returns a connected client, or nil when the caller should fall
// back to a direct scan. --no-runtime-service skips the dial entirely; a failed dial is
// silent here because whether it deserves a note depends on what the caller
// does next.
func dialOrNil(ctx context.Context) *client.Client {
	if noRuntimeServiceFlag {
		return nil
	}
	c, err := dialRuntimeService(ctx)
	if err != nil {
		return nil
	}
	return c
}

// runtimeServiceClient returns a connected client, or nil when the caller should fall
// back to a direct scan. A nil result with --no-runtime-service is silent; a runtime service that
// could not be reached prints the one-line note first.
func runtimeServiceClient(ctx context.Context) *client.Client {
	c := dialOrNil(ctx)
	if c == nil && !noRuntimeServiceFlag {
		noteFallback()
	}
	return c
}

// runtimeServiceUp reports whether a runtime service is listening, without the note
// `runtimeServiceClient` prints on the way past.
//
// It is for a command that has nothing to fall back to: "runtime service unavailable,
// using direct scan" is a useful sentence for `list`, and a false one for a
// caller about to say that the runtime service is required.
func runtimeServiceUp(ctx context.Context) bool {
	c := dialOrNil(ctx)
	if c == nil {
		return false
	}
	c.Close()
	return true
}

// runtimeServiceList reads the worktree-scoped live snapshot through the runtime service and
// converts its evidence rows back to the scanner shape used by kill and logs.
// The old whole-machine ports.list endpoint is intentionally gone.
//
// Every command that reads it acts on one machine: this one. `--host` and the
// registered-remote-host machinery it used to reach are gone
// (docs/history/scope.md, 2026-09-22). The product starts the projects on the
// machine it runs on. The wire carries no host field either: a field every
// handler ignored was indistinguishable from one that worked, so it was
// withdrawn rather than kept and filled with "local".
func runtimeServiceList(ctx context.Context, c *client.Client) ([]ports.ListeningPort, error) {
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
