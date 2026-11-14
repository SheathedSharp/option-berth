package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
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
	flags := downCmd.Flags()
	flags.BoolVarP(&downForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	flags.BoolVar(&downJSONFlag, "json", false, "Output as JSON")
	flags.StringVar(&downWorktreeFlag, "worktree", "",
		"Stop the checkout at this root path, even if its directory is gone")
	flags.StringVar(&downIdentityFlag, "identity", "",
		"Stop the checkout with this wt_ identity, even if its directory is gone")
	rootCmd.AddCommand(downCmd)
}

func downRun(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	params, local, err := downParams(args)
	if err != nil {
		return err
	}

	c, err := connectForWrite(cmd.Context())
	if err != nil {
		return err
	}
	defer c.Close()

	if err := requireStopConfigSupport(c, local); err != nil {
		return err
	}

	rows, err := daemonList(cmd.Context(), c)
	if err != nil {
		return cliError(err)
	}

	var envelope rpc.KillEnvelope
	if err := c.Call(cmd.Context(), "groups.kill", params, &envelope); err != nil {
		return cliError(err)
	}
	if downJSONFlag {
		// The envelope is the explanation, the status is the gate. Printing the
		// document and exiting 0 made a failed stop indistinguishable from a
		// clean one to a caller that reads only the status, whose next step is
		// to delete a worktree whose service is still running. The text path
		// already answers non-zero through reportKill; this branch answers
		// through the daemon's own verdict, as `restart --json` does for the
		// same envelope.
		if err := printJSON(envelope); err != nil {
			return err
		}
		if !envelope.OK {
			return errSilent
		}
		return nil
	}
	return reportDown(envelope, rows)
}

// reportDown renders what groups.kill did: the per-process lines for whatever
// was listening, and then the sentence about the reservations it gave back.
// The released count is the daemon's, so it is printed whether or not anything
// was listening.
func reportDown(envelope rpc.KillEnvelope, rows []ports.ListeningPort) error {
	var renderErr error
	if len(envelope.Results) == 0 {
		fmt.Println("Nothing was running.")
	} else {
		renderErr = reportKill(os.Stdout, envelope.Results, rows, false, false)
	}
	if envelope.Released > 0 {
		fmt.Println(display.Dim(fmt.Sprintf("released %d claimed %s", envelope.Released, pluralWord(envelope.Released, "port"))))
	}
	return renderErr
}

// downParams chooses what to stop before any daemon call: one explicit
// checkout (path or identity), one named project, or the manifest at or above
// the working directory. The selectors are mutually exclusive, and a
// filesystem path is made absolute here because the daemon resolves it
// against its own working directory, not the shell's.
func downParams(args []string) (rpc.GroupsKillParams, *groups.Config, error) {
	params := rpc.GroupsKillParams{Force: downForceFlag, Release: true}
	worktree := strings.TrimSpace(downWorktreeFlag)
	identity := strings.TrimSpace(downIdentityFlag)
	if err := exclusiveDownSelector(worktree, identity, args); err != nil {
		return params, nil, err
	}

	switch {
	case worktree != "":
		abs, err := filepath.Abs(worktree)
		if err != nil {
			return params, nil, usageError{fmt.Errorf("resolving --worktree %q: %w", worktree, err)}
		}
		params.WorktreePath = &abs
	case identity != "":
		params.Identity = &identity
	case len(args) == 1:
		params.Name = strings.TrimSpace(args[0])
	default:
		return downParamsFromManifest(params)
	}
	return params, nil, nil
}

// exclusiveDownSelector refuses the combinations that name more than one
// subject: stopping two checkouts at once would be two stops, not one, and
// neither form can be combined with a project argument.
func exclusiveDownSelector(worktree, identity string, args []string) error {
	if worktree != "" && identity != "" {
		return usageError{fmt.Errorf("--worktree and --identity cannot be combined")}
	}
	if (worktree != "" || identity != "") && len(args) == 1 {
		return usageError{fmt.Errorf("a project argument cannot be combined with --worktree or --identity")}
	}
	return nil
}

// downParamsFromManifest is the form with no selector at all: the manifest at
// or above the working directory is the project, and its parsed config is
// returned so the stop can be checked against the daemon's capabilities.
func downParamsFromManifest(params rpc.GroupsKillParams) (rpc.GroupsKillParams, *groups.Config, error) {
	wd, err := os.Getwd()
	if err != nil {
		return params, nil, err
	}
	cfg, err := nearestConfig(wd)
	if err != nil {
		return params, nil, err
	}
	path := cfg.Path
	params.ConfigPath = &path
	return params, cfg, nil
}
