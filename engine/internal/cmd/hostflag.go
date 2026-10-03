package cmd

import (
	"context"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/ports"
)

// Every command here acts on one machine: this one.
//
// `--host` and the registered-remote-host machinery it used to reach are gone
// (docs/history/scope.md, 2026-09-22). The product starts the projects on the
// machine it runs on; pointing it at another machine's daemon over ssh was the
// upstream project's idea of a fleet, and a fleet is what this is not. What is
// left of that layer is this file: the params every call carries, saying "local".

// hostParams is the wire form of "this machine" for the mutating daemon calls
// that still accept the local selector envelope.
func hostParams() rpc.HostParams { return rpc.HostParams{} }

// hostSnapshot reads the current live evidence a command selects its targets
// from. It is a worktree snapshot, not an all-machine port radar.
func hostSnapshot(ctx context.Context, c *client.Client) ([]ports.ListeningPort, error) {
	return daemonList(ctx, c)
}
