package cmd

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/docker"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/spf13/cobra"
)

// completionLong is the command's long help, one entry per output line so the
// per-shell recipes stay readable as a list rather than a block of prose.
var completionLong = strings.Join([]string{
	"Generate shell completion scripts for option-berth.",
	"",
	"To load completions:",
	"",
	"Bash:",
	"  $ source <(oberth completion bash)",
	"",
	"  # To load completions for each session, execute once:",
	"  # Linux:",
	"  $ oberth completion bash > /etc/bash_completion.d/option-berth",
	"  # macOS:",
	"  $ oberth completion bash > $(brew --prefix)/etc/bash_completion.d/option-berth",
	"",
	"Zsh:",
	"  # If shell completion is not already enabled in your environment,",
	"  # you will need to enable it. You can execute the following once:",
	"  $ echo \"autoload -U compinit; compinit\" >> ~/.zshrc",
	"",
	"  # To load completions for each session, execute once:",
	"  $ oberth completion zsh > \"${fpath[1]}/_oberth\"",
	"",
	"  # You will need to start a new shell for this setup to take effect.",
	"",
	"Fish:",
	"  $ oberth completion fish | source",
	"",
	"  # To load completions for each session, execute once:",
	"  $ oberth completion fish > ~/.config/fish/completions/oberth.fish",
}, "\n") + "\n"

var completionCmd = &cobra.Command{
	Use:                   "completion [bash|zsh|fish]",
	Short:                 "Generate shell completion scripts",
	GroupID:               commandGroupInfra,
	Long:                  completionLong,
	DisableFlagsInUseLine: true,
	ValidArgs:             []string{"bash", "zsh", "fish"},
	Args:                  cobra.MatchAll(cobra.ExactArgs(1), cobra.OnlyValidArgs),
	RunE:                  runCompletionShell,
}

// runCompletionShell writes the completion script the named shell reads.
func runCompletionShell(cmd *cobra.Command, args []string) error {
	root, out := cmd.Root(), cmd.OutOrStdout()
	switch args[0] {
	case "bash":
		return root.GenBashCompletion(out)
	case "zsh":
		return root.GenZshCompletion(out)
	case "fish":
		return root.GenFishCompletion(out, true)
	}
	return fmt.Errorf("unsupported shell: %s", args[0])
}

func init() {
	rootCmd.AddCommand(completionCmd)
}

// scanCompletionPorts is the listener scan completion asks for. Both halves of
// its answer are the contract: the rows to name, and the failure that must not
// be offered as "there is nothing to complete".
var scanCompletionPorts = func() ([]ports.ListeningPort, error) {
	pp, err := ports.Scan()
	if err != nil {
		return nil, err
	}
	docker.EnrichPorts(pp)
	ports.Enrich(pp)
	// Completion renders names without building the group collection; stamp
	// run ownership so a `oberth start` service completes as its service name.
	groups.StampRuns(pp)
	return pp, nil
}

// completePort offers candidate <port> arguments for the commands that take
// one: a listener scan enriched with Docker and process detail, rendered as
// "port\tdescription".
func completePort(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}

	pp, err := scanCompletionPorts()
	if err != nil {
		return nil, cobra.ShellCompDirectiveError
	}

	return portCompletions(pp), cobra.ShellCompDirectiveNoFileComp
}

// portCompletions is the candidate list: a port and the name a reader knows it
// by, separated by a tab so the shell shows the name but inserts the number.
func portCompletions(rows []ports.ListeningPort) []string {
	completions := make([]string, 0, len(rows))
	for _, row := range rows {
		completions = append(completions, fmt.Sprintf("%s\t%s", strconv.Itoa(row.Port), row.DisplayName()))
	}
	return completions
}
