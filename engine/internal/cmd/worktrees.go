package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"github.com/spf13/cobra"
)

var (
	worktreesJSONFlag bool
	worktreesGoneFlag bool
)

// worktreesCmd is the read-only half of the worktree lifecycle: which
// `${worktree}` identities this daemon has seen, and which of their checkouts
// are gone. It never deletes a database, a data directory or a ledger row;
// removing data stays a person's decision.
var worktreesCmd = &cobra.Command{
	Use:     "worktrees",
	Short:   "Checkout identities this machine has seen, including gone checkouts",
	GroupID: commandGroupFacts,
	Long: "List every `${worktree}` identity the daemon has recorded, with the checkout\n" +
		"root the token was derived from. A row outlives its directory: after a worktree\n" +
		"is deleted or moved, the identity still names the database or data directory\n" +
		"that was built from it (`app_${worktree}`, `.data/${worktree}`).\n\n" +
		"`--gone` keeps only identities whose checkout no longer exists at the recorded\n" +
		"path. The command is read-only: it does not remove the ledger row, the data, or\n" +
		"a running service. `oberth down --identity <wt_...>` stops what is still\n" +
		"running for one of them.",
	Args: cobra.NoArgs,
	RunE: worktreesRun,
}

func init() {
	worktreesCmd.Flags().BoolVar(&worktreesJSONFlag, "json", false, "Output as JSON")
	worktreesCmd.Flags().BoolVar(&worktreesGoneFlag, "gone", false,
		"Only identities whose checkout no longer exists")
	rootCmd.AddCommand(worktreesCmd)
}

// worktreeIdentityRow is one line of the public document. Existence is a
// filesystem fact read now, not a stored flag: a checkout that comes back at
// the same path has the same identity again.
type worktreeIdentityRow struct {
	Identity string `json:"identity"`
	Root     string `json:"root"`
	Group    string `json:"group,omitempty"`
	Exists   bool   `json:"exists"`
	LastSeen string `json:"last_seen,omitempty"`
	// RunningServices is how many declared services are running for this
	// identity now. It is absent when no daemon answered: the ledger does not
	// store runtime facts, and zero would claim a measurement the command did
	// not make.
	RunningServices *int `json:"running_services,omitempty"`
}

type worktreesDocument struct {
	Worktrees []worktreeIdentityRow `json:"worktrees"`
}

func worktreesRun(cmd *cobra.Command, _ []string) error {
	identities, err := knownWorktreeIdentities()
	if err != nil {
		return err
	}
	running := runningServicesByIdentity(cmd.Context())
	rows := make([]worktreeIdentityRow, 0, len(identities))
	for _, identity := range identities {
		exists := checkoutExists(identity.Root)
		if worktreesGoneFlag && exists {
			continue
		}
		row := worktreeIdentityRow{
			Identity: identity.Identity,
			Root:     identity.Root,
			Group:    identity.Group,
			Exists:   exists,
		}
		if count, ok := running[identity.Identity]; ok {
			row.RunningServices = &count
		}
		if !identity.LastSeen.IsZero() {
			row.LastSeen = identity.LastSeen.UTC().Format(time.RFC3339)
		}
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Group != rows[j].Group {
			return rows[i].Group < rows[j].Group
		}
		return rows[i].Identity < rows[j].Identity
	})

	if worktreesJSONFlag {
		return printJSON(worktreesDocument{Worktrees: rows})
	}
	printWorktrees(rows)
	return nil
}

// runningServicesByIdentity asks the daemon for the running count per
// `${worktree}` identity. The ledger is the authority on identity; this is a
// read-time join against group facts. No daemon means no entry, which the JSON
// omits, rather than a zero the command did not observe.
func runningServicesByIdentity(ctx context.Context) map[string]int {
	if noDaemonFlag {
		return nil
	}
	c, err := dialDaemon(ctx)
	if err != nil {
		return nil
	}
	defer c.Close()
	var listed rpc.GroupsListResult
	if err := c.Call(ctx, "groups.list", rpc.HostParams{Host: hostParams().Host}, &listed); err != nil {
		return nil
	}
	out := make(map[string]int, len(listed.Groups))
	for _, group := range listed.Groups {
		if group.Identity == "" {
			continue
		}
		out[group.Identity] += group.RunningServices
	}
	return out
}

// knownWorktreeIdentities reads the ledger without starting or asking the
// daemon. A database that does not exist yet, or one from before the ledger
// existed, has no identities rather than an error: there is nothing to clean up
// and nothing to report.
func knownWorktreeIdentities() ([]store.WorktreeIdentity, error) {
	st, err := store.OpenReadOnly("")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer st.Close()
	identities, err := st.WorktreeIdentities()
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return nil, nil
	}
	return identities, err
}

// checkoutExists is the only definition of "gone" this command uses: the
// recorded root is no longer a directory at that path.
func checkoutExists(root string) bool {
	if root == "" {
		return false
	}
	info, err := os.Stat(root)
	return err == nil && info.IsDir()
}

func printWorktrees(rows []worktreeIdentityRow) {
	if len(rows) == 0 {
		if worktreesGoneFlag {
			fmt.Println(display.Dim("No worktree identities have a missing checkout."))
			return
		}
		fmt.Println(display.Dim("No worktree identities recorded yet; the daemon records one when it sees a manifest."))
		return
	}
	for _, row := range rows {
		group := row.Group
		if group == "" {
			group = display.Dim("no group")
		}
		state := display.Dim("present")
		if !row.Exists {
			state = display.Yellow("checkout gone")
		}
		running := ""
		if row.RunningServices != nil {
			running = fmt.Sprintf("  %d running", *row.RunningServices)
		}
		fmt.Printf("  %s  %s  %s  %s%s\n", row.Identity, group, row.Root, state, running)
	}
}
