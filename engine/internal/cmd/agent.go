package cmd

import (
	"fmt"
	"io"

	"github.com/sheathedsharp/option-berth/internal/agentlaunch"
	"github.com/spf13/cobra"
)

func newAgentCommand() *cobra.Command {
	command := &cobra.Command{Use: "agent", Short: "Open an existing coding agent in an explicitly selected worktree", GroupID: commandGroupSupport}
	var listJSON bool
	list := &cobra.Command{Use: "list", Args: cobra.NoArgs, Short: "List supported agents without running them", RunE: func(cmd *cobra.Command, _ []string) error {
		providers := agentlaunch.Providers(nil)
		if listJSON {
			return printJSON(struct {
				Providers []agentlaunch.Provider `json:"providers"`
			}{providers})
		}
		for _, p := range providers {
			state := "not installed"
			if p.Installed {
				state = "available"
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%-10s %-18s %s\n", p.ID, p.Name, state)
		}
		return nil
	}}
	list.Flags().BoolVar(&listJSON, "json", false, "Output JSON")
	command.AddCommand(list)
	for _, verb := range []string{"plan", "run"} {
		verb := verb
		var worktree, mode, prompt string
		var promptStdin, jsonOutput bool
		sub := &cobra.Command{Use: verb + " <provider>", Args: cobra.ExactArgs(1), Short: "Build an exact native argv; run executes it with inherited terminal input", RunE: func(cmd *cobra.Command, args []string) error {
			// Reject plan-only flags before consuming input or resolving a provider.
			if verb == "run" && jsonOutput {
				return usageError{fmt.Errorf("--json is for agent plan/list; run preserves the agent's native output")}
			}
			if verb == "run" && promptStdin {
				return usageError{fmt.Errorf("--prompt-stdin is plan-only; native run must retain terminal stdin")}
			}
			if promptStdin && cmd.Flags().Changed("prompt") {
				return usageError{fmt.Errorf("use either --prompt or --prompt-stdin")}
			}
			if promptStdin {
				raw, err := io.ReadAll(io.LimitReader(cmd.InOrStdin(), 64*1024+1))
				if err != nil {
					return err
				}
				if len(raw) > 64*1024 {
					return usageError{fmt.Errorf("agent prompt exceeds 64 KiB")}
				}
				prompt = string(raw)
			}
			plan, err := agentlaunch.Build(agentlaunch.Options{Provider: args[0], Worktree: worktree, Mode: mode, Prompt: prompt}, nil)
			if err != nil {
				return usageError{err}
			}
			if verb == "plan" {
				if jsonOutput {
					return printJSON(plan)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s mode in %s\nExecutable: %s\nArguments are not printed; use --json to inspect locally.\n", plan.Provider, plan.Mode, plan.Worktree, plan.Executable)
				return nil
			}
			return executeAgent(plan)
		}}
		sub.Flags().StringVar(&worktree, "worktree", "", "Explicit selected checkout directory")
		sub.Flags().StringVar(&mode, "mode", "native", "native interactive UI or task (one-shot)")
		sub.Flags().StringVar(&prompt, "prompt", "", "Initial prompt; not interpreted as shell text")
		sub.Flags().BoolVar(&promptStdin, "prompt-stdin", false, "Read plan prompt from stdin (not shell history)")
		sub.Flags().BoolVar(&jsonOutput, "json", false, "Print the exact launch plan as JSON (may contain prompt text)")
		command.AddCommand(sub)
	}
	return command
}

func init() { rootCmd.AddCommand(newAgentCommand()) }
