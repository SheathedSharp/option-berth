package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/spf13/cobra"
)

var (
	downForceFlag    bool
	downJSONFlag     bool
	downWorktreeFlag string
	downIdentityFlag string
)

// downCmd is the project lifecycle stop action: it stops a project and gives
// back the ports option-berth picked for it.
var downCmd = &cobra.Command{
	Use:     "down [project]",
	Short:   "Stop a project's services and release its claimed ports",
	GroupID: commandGroupOperations,
	Long: "Stop every service of the project in the nearest oberth.yaml, or of the\n" +
		"named project: every port it listens on, and every service option-berth started\n" +
		"for it that holds no port. The ports option-berth claimed for its `port: auto`\n" +
		"services are released.\n\n" +
		"`--worktree` names a checkout by its root path and `--identity` by the\n" +
		"`wt_...` token from `oberth status --json` or `oberth worktrees`. Either one\n" +
		"still reaches the checkout's services after its directory was deleted or\n" +
		"moved, and releases the reservations recorded for that identity. Neither can\n" +
		"be combined with a project name or config path.",
	Args: cobra.MaximumNArgs(1),
	RunE: downRun,
}

func init() {
	downCmd.Flags().BoolVarP(&downForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	downCmd.Flags().BoolVar(&downJSONFlag, "json", false, "Output as JSON")
	downCmd.Flags().StringVar(&downWorktreeFlag, "worktree", "",
		"Stop the checkout at this root path, even if its directory is gone")
	downCmd.Flags().StringVar(&downIdentityFlag, "identity", "",
		"Stop the checkout with this wt_ identity, even if its directory is gone")
	rootCmd.AddCommand(downCmd)
}

func downRun(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	worktree := strings.TrimSpace(downWorktreeFlag)
	identity := strings.TrimSpace(downIdentityFlag)
	if worktree != "" && identity != "" {
		return usageError{fmt.Errorf("--worktree and --identity cannot be combined")}
	}
	if (worktree != "" || identity != "") && len(args) == 1 {
		return usageError{fmt.Errorf("a project argument cannot be combined with --worktree or --identity")}
	}

	params := rpc.GroupsKillParams{HostParams: hostParams(), Force: downForceFlag, Release: true}
	var local *groups.Config
	switch {
	case worktree != "":
		// The daemon resolves paths against its own cwd; make the caller's
		// answer independent of where it was typed.
		abs, err := filepath.Abs(worktree)
		if err != nil {
			return usageError{fmt.Errorf("resolving --worktree %q: %w", worktree, err)}
		}
		params.WorktreePath = &abs
	case identity != "":
		params.Identity = &identity
	case len(args) == 1:
		params.Name = strings.TrimSpace(args[0])
	default:
		wd, err := os.Getwd()
		if err != nil {
			return err
		}
		cfg, err := nearestConfig(wd)
		if err != nil {
			return err
		}
		path := cfg.Path
		params.ConfigPath = &path
		local = cfg
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if err := requireStopConfigSupport(c, local); err != nil {
		return err
	}

	snapshot, err := hostSnapshot(cmd.Context(), c)
	if err != nil {
		return cliError(err)
	}

	var env rpc.KillEnvelope
	if err := c.Call(cmd.Context(), "groups.kill", params, &env); err != nil {
		return cliError(err)
	}
	if downJSONFlag {
		return printJSON(env)
	}

	var reportErr error
	if len(env.Results) == 0 {
		fmt.Println("Nothing was running.")
	} else {
		reportErr = reportKill(os.Stdout, env.Results, snapshot, false, false)
	}
	if env.Released > 0 {
		fmt.Println(display.Dim(fmt.Sprintf("released %d claimed %s", env.Released, pluralWord(env.Released, "port"))))
	}
	return reportErr
}
