package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/groups"
	"github.com/spf13/cobra"
)

var (
	downForceFlag bool
	downJSONFlag  bool
)

// downCmd is the project lifecycle stop action: it stops a project and gives
// back the ports option-berth picked for it.
var downCmd = &cobra.Command{
	Use:     "down [project]",
	Short:   "Stop a project's services and release its claimed ports",
	GroupID: commandGroupCore,
	Long: "Stop every service of the project in the nearest oberth.yaml, or of the\n" +
		"named project: every port it listens on, and every service option-berth started\n" +
		"for it that holds no port. The ports option-berth claimed for its `port: auto`\n" +
		"services are released.",
	Args: cobra.MaximumNArgs(1),
	RunE: downRun,
}

func init() {
	downCmd.Flags().BoolVarP(&downForceFlag, "force", "f", false, "Send SIGKILL instead of SIGTERM")
	downCmd.Flags().BoolVar(&downJSONFlag, "json", false, "Output as JSON")
	rootCmd.AddCommand(downCmd)
}

func downRun(cmd *cobra.Command, args []string) error {
	cmd.SilenceUsage = true
	params := rpc.GroupsKillParams{HostParams: hostParams(), Force: downForceFlag, Release: true}
	var local *groups.Config
	if len(args) == 1 {
		params.Name = strings.TrimSpace(args[0])
	} else {
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

	if err := requireConfigSupport(c, local); err != nil {
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
