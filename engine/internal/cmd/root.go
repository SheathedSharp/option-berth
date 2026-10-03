package cmd

import (
	"errors"
	"fmt"
	"os"

	"github.com/sheathedsharp/option-berth/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/internal/config"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/ports"
	"github.com/spf13/cobra"
)

// loadedConfig holds the parsed config for the duration of a command run.
// Populated by PersistentPreRun; never nil after Execute starts.
var loadedConfig = &config.Config{}

var versionFlag bool

const banner = `
  ██████   ███   ██████   ██   ██████
  ██          ██     ██ ██  ██     ██
  ██     ██   ██ ██  ██ ██  ██     ██
      ██ ██   ██ ██  ██ ██  ██ ████
      ██ ██      ██  ██ ██  ██ ██  ██
  ██████   ███   ██  ██ ██  ██ ██  ██
`

var rootCmd = &cobra.Command{
	Use:   "oberth",
	Short: "Manage services for the current worktree",
	Long:  display.Cyan(banner) + "\n  " + display.Dim("Read a worktree's manifest and runtime, then start or stop its services."),
	RunE: func(cmd *cobra.Command, args []string) error {
		if versionFlag {
			// Keep the shell-facing version probe deliberately small. Detailed
			// build identity remains available through `version --json`.
			fmt.Printf("option-berth %s\n", buildinfo.VersionValue())
			return nil
		}
		return cmd.Help()
	},
}

const (
	commandGroupCore    = "core"
	commandGroupSupport = "support"
	commandGroupInfra   = "infra"
)

func init() {
	rootCmd.AddGroup(
		&cobra.Group{ID: commandGroupCore, Title: "Worktree lifecycle:"},
		&cobra.Group{ID: commandGroupSupport, Title: "Inspection and explicit controls:"},
		&cobra.Group{ID: commandGroupInfra, Title: "Runtime and setup:"},
	)
	rootCmd.PersistentFlags().Bool("no-color", false, "Disable colored output")
	rootCmd.Flags().BoolVarP(&versionFlag, "version", "v", false,
		"Print the version and exit")
	rootCmd.PersistentFlags().BoolVar(&noDaemonFlag, "no-daemon", false,
		"Never talk to the oberth daemon; scan directly instead")

	// Error rendering is ours (docs/cli.md: one shape, and a --json caller gets
	// JSON on stderr). cobra would otherwise print its own "Error: …" followed
	// by the whole usage block.
	rootCmd.SilenceErrors = true
	rootCmd.SilenceUsage = true
	rootCmd.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		// pflag errors have no exported type; wrapping them here is what lets
		// Execute tell "you used the command wrong" (exit 2) from a failure.
		return usageError{err}
	})

	rootCmd.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		if versionFlag {
			return
		}
		// Errors are rendered in Execute, after this command has returned and
		// its flags are out of reach — so the JSON answer is captured here.
		// --json is a per-command flag, never a persistent one.
		if f := cmd.Flags().Lookup("json"); f != nil {
			jsonMode = f.Value.String() == "true"
		}

		cfg, warnings := config.Load()
		for _, w := range warnings {
			fmt.Fprintln(os.Stderr, w)
		}
		loadedConfig = cfg

		// color: false in config disables ANSI; an explicit --no-color flag
		// also wins. We never force color ON (would corrupt piped output).
		if cfg.Color != nil && !*cfg.Color {
			display.NoColor = true
		}
		if nc, _ := cmd.Flags().GetBool("no-color"); nc {
			display.NoColor = true
		}

		ports.RegisterServices(cfg.Services)
	}
}

// errSilent asks for a non-zero exit without a further message: the command
// has already reported the failure itself, as `--json` output does.
var errSilent = errors.New("")

// Execute runs the CLI and exits with the status docs/cli.md documents:
// 0 ok, 1 failure, 2 usage, 130 interrupted.
func Execute() {
	err := rootCmd.Execute()
	if err == nil {
		return
	}
	code := reportError(os.Stderr, err)
	if code == exitOK {
		code = exitFail
	}
	os.Exit(code)
}
