package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/state"
	"github.com/spf13/cobra"
)

var (
	eventsOnce         bool
	eventsWorktrees    []string
	eventsRepositories []string
	eventsWorkspace    string
)

// eventsSnapshotRecord and eventsChangedRecord flatten the protocol payload
// into the CLI's NDJSON line. An agent can decode each line by its top-level
// type without maintaining a second envelope model.
type eventsSnapshotRecord struct{ state.StreamSnapshot }
type eventsChangedRecord struct{ state.StateChanged }

var eventsCmd = &cobra.Command{
	Use:     "events",
	Short:   "Subscribe to selected worktree state changes",
	GroupID: commandGroupSupport,
	Long: "Read a filtered machine-local state stream. The first NDJSON record is a snapshot; later records are state.changed summaries.\n\n" +
		"Selectors are additive. `--worktree` takes a checkout root or worktree ID, `--repository` takes a repository ID, and `--workspace` joins an explicit ephemeral integration relation.\n" +
		"A workspace is registered by a subscription that also supplies a worktree or repository; later subscribers may use that workspace name alone.\n" +
		"After a transport drop or queue overflow, `oberth events` reconnects with its last sequence and asks the daemon to replay the missing changes.",
	Args: cobra.NoArgs,
	RunE: eventsRun,
}

func init() {
	eventsCmd.Flags().BoolVar(&eventsOnce, "once", false, "Print the selected snapshot and exit")
	eventsCmd.Flags().StringSliceVar(&eventsWorktrees, "worktree", nil, "Include a checkout root (repeatable or comma separated)")
	eventsCmd.Flags().StringSliceVar(&eventsRepositories, "repository", nil, "Include a repository ID (repeatable or comma separated)")
	eventsCmd.Flags().StringVar(&eventsWorkspace, "workspace", "", "Join an explicit integration workspace")
	rootCmd.AddCommand(eventsCmd)
}

func eventsRun(cmd *cobra.Command, _ []string) error {
	scope := state.Scope{
		Worktrees:    append([]string(nil), eventsWorktrees...),
		Repositories: append([]string(nil), eventsRepositories...),
		Workspace:    eventsWorkspace,
	}
	scope = normalizeEventsScope(scope)
	if scope.Empty() {
		return usageError{errors.New("events requires --worktree, --repository, or --workspace")}
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)

	if eventsOnce {
		c, err := connectForWrite(cmd.Context())
		if err != nil {
			return err
		}
		defer c.Close()
		if err := requireScopedState(c); err != nil {
			return err
		}
		var snap state.StreamSnapshot
		if err := c.Call(cmd.Context(), "state.snapshot", rpc.StateSnapshotParams{Scope: &scope}, &snap); err != nil {
			return cliError(err)
		}
		return enc.Encode(eventsSnapshotRecord{StreamSnapshot: snap})
	}

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Keep the last sequence the process actually emitted. If the socket drops
	// or its bounded delivery queue overflows, the next subscription asks the
	// daemon to replay from this cursor instead of taking an uncheckable leap to
	// a fresh state. The daemon identity disambiguates a sequence reset after a
	// restart.
	var lastSeq uint64
	var daemonIdentity string
	haveSubscription := false
	haveSnapshot := false
	workspaceScoped := strings.TrimSpace(scope.Workspace) != ""
	for {
		if err := waitForEventReconnect(ctx, haveSubscription); err != nil {
			return err
		}
		c, err := connectForWrite(ctx)
		if err != nil {
			if !haveSubscription {
				return err
			}
			if ctx.Err() != nil {
				return context.Canceled
			}
			continue
		}
		if err := requireScopedState(c); err != nil {
			c.Close()
			return err
		}
		identity := fmt.Sprintf("%d/%s", c.Hello().PID, c.Hello().StartedAt)
		sub, err := c.Subscribe(ctx, client.SubscribeOptions{
			Events:   true,
			AfterSeq: lastSeq,
			Scope:    &scope,
		})
		if err != nil {
			c.Close()
			if ctx.Err() != nil {
				return context.Canceled
			}
			// An invalid workspace relation is a real subscription error, not a
			// dropped transport. The caller must rejoin it with a selector. A
			// transport error after a successful subscription can be replayed.
			var rpcErr *rpc.Error
			if !haveSubscription || errors.As(err, &rpcErr) {
				return cliError(err)
			}
			continue
		}

		if !haveSnapshot || workspaceScoped || sub.StreamSnapshot.Seq != lastSeq || identity != daemonIdentity {
			if err := enc.Encode(eventsSnapshotRecord{StreamSnapshot: sub.StreamSnapshot}); err != nil {
				c.Close()
				return err
			}
		}
		lastSeq = sub.StreamSnapshot.Seq
		daemonIdentity = identity
		haveSubscription = true
		haveSnapshot = true

		changes := sub.Changes
		daemonStopping := false
		for changes != nil {
			select {
			case change, ok := <-changes:
				if !ok {
					changes = nil
					continue
				}
				if sub.Dropped() {
					changes = nil
					continue
				}
				if slices.Contains(change.Changed, "daemon_stopping") {
					daemonStopping = true
				}
				if change.Seq > lastSeq {
					lastSeq = change.Seq
				}
				if err := enc.Encode(eventsChangedRecord{StateChanged: change}); err != nil {
					c.Close()
					return err
				}
			case <-ctx.Done():
				c.Close()
				return context.Canceled
			}
		}
		c.Close()
		if daemonStopping {
			return nil
		}
		// The transport dropped or the subscription queue overflowed. Loop back
		// with lastSeq; a brief delay avoids a tight reconnect loop while a
		// daemon is being replaced.
	}
}

func waitForEventReconnect(ctx context.Context, resumed bool) error {
	if !resumed {
		return nil
	}
	timer := time.NewTimer(250 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return context.Canceled
	case <-timer.C:
		return nil
	}
}

func requireScopedState(c *client.Client) error {
	for _, capability := range c.Hello().Capabilities {
		if capability == rpc.CapabilityStateScope {
			return nil
		}
	}
	version := c.Hello().DaemonVersion
	if version == "" {
		version = "an older version"
	}
	return fmt.Errorf("the running daemon (%s) does not support scoped state events\nhint: restart it with `oberth daemon restart`", version)
}

func normalizeEventsScope(scope state.Scope) state.Scope {
	worktrees := make([]string, 0, len(scope.Worktrees))
	for _, raw := range scope.Worktrees {
		root := strings.TrimSpace(raw)
		if root == "" {
			continue
		}
		if state.IsWorktreeID(root) {
			worktrees = append(worktrees, root)
			continue
		}
		if absolute, err := filepath.Abs(root); err == nil {
			worktrees = append(worktrees, absolute)
		} else {
			worktrees = append(worktrees, root)
		}
	}
	scope.Worktrees = worktrees
	return scope
}
