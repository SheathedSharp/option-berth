package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/display"
	"github.com/sheathedsharp/option-berth/internal/git"
	"github.com/sheathedsharp/option-berth/internal/store"
	"github.com/sheathedsharp/option-berth/internal/workplace"
	"github.com/spf13/cobra"
)

// The workplace record: what work has been done in this worktree, and which
// workplace new work lands in.
//
// The command is the thin half. It resolves the worktree, asks the daemon, and
// prints; every rule about what a workplace is, what a selector matches and
// what a block means lives in internal/workplace, which is also what the
// daemon serves. Reads fall back to the database directly when no daemon is
// running, because "which workplace is this and what is in it" is a question
// the record on disk can answer on its own.

// The command family's flags. They are package variables rather than locals
// because the command tree is built once: a value one invocation sets would
// otherwise still be there for the next one in the same process, which is a
// test's problem rather than a user's only because a user's invocations are
// separate processes.
var (
	workplaceWorktreeFlag string
	workplaceJSONFlag     bool
	// The record command's fields, one per flag.
	workplaceRecordBlock      rpc.WorkplaceBlockInput
	workplaceRecordOutputFile string
	workplaceRecordStartedAt  string
	workplaceRecordEndedAt    string
	workplaceRecordExitCode   int
)

func newWorkplaceCommand() *cobra.Command {
	command := &cobra.Command{
		Use:     "workplace",
		Short:   "The record of work done in this worktree",
		GroupID: commandGroupHandoff,
		Long: "A workplace is a named scope under one worktree that holds the work done in it: the\n" +
			"shell commands the engine ran and the agent tasks it bridged. Work is recorded as\n" +
			"it happens — `oberth start` and `oberth agent stream` both write into the worktree's\n" +
			"active workplace — and a front-end that watched work the engine did not start can\n" +
			"report it with `oberth workplace record`.\n\n" +
			"The record is facts, not a session: recalling a workplace reads back what was done,\n" +
			"with the run and the log each block belongs to. It never restarts anything, and a\n" +
			"block with no observed end stays `running` rather than being assumed to have\n" +
			"succeeded.",
	}
	command.PersistentFlags().StringVar(&workplaceWorktreeFlag, "worktree", "",
		"The worktree to work in; defaults to the working directory")
	command.AddCommand(
		newWorkplaceListCommand(),
		newWorkplaceNewCommand(),
		newWorkplaceUseCommand(),
		newWorkplaceShowCommand(),
		newWorkplaceDeleteCommand(),
		newWorkplaceRecordCommand(),
	)
	return command
}

func init() { rootCmd.AddCommand(newWorkplaceCommand()) }

// workplaceRoot is the worktree a workplace command is about: --worktree, or
// the working directory.
func workplaceRoot() (string, error) {
	dir := strings.TrimSpace(workplaceWorktreeFlag)
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("resolving the working directory: %w", err)
		}
		dir = wd
	}
	return resolveWorktree(dir)
}

// resolveWorktree names the checkout a directory sits in, in the one spelling
// the workplace tables key on.
//
// A checkout is what a workplace belongs to, so git decides first; a directory
// that holds an oberth.yaml without being a checkout is still a project, and
// its manifest directory is the root it is recorded under.
func resolveWorktree(dir string) (string, error) {
	root := git.Root(dir)
	if root == "" {
		if _, r := projectAt(dir); r != "" {
			root = r
		}
	}
	if root == "" {
		return "", failHint("not_found",
			fmt.Sprintf("no git checkout at or above %s", shortPath(dir)),
			"run this from a worktree, or name one with --worktree <path>")
	}
	clean, err := store.CleanRoot(root)
	if err != nil {
		return "", fail("invalid_params", "%v", err)
	}
	return clean, nil
}

// workplaceError renders a failure from internal/workplace the way every other
// command renders one: the same code and the same hint the daemon would send.
func workplaceError(err error) error {
	if err == nil {
		return nil
	}
	return failHint(rpc.CodeName(workplace.Code(err)), err.Error(), workplace.Hint(err))
}

// withRecord runs fn against the daemon when one is listening, and against the
// database directly when none is. Only a daemon that cannot be reached falls
// back: one that answered with an error answered the question, and asking the
// file instead would turn a refusal into a second opinion.
func withRecord[T any](ctx context.Context, method string, params any, fn func(*store.Store) (T, error)) (T, error) {
	var zero T
	if !noDaemonFlag {
		if c, err := dialDaemon(ctx); err == nil {
			defer c.Close()
			var out T
			if err := c.Call(ctx, method, params, &out); err != nil {
				return zero, cliError(err)
			}
			return out, nil
		}
	}
	st, err := store.OpenReadOnly(store.Path())
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return zero, failHint("daemon_unavailable",
				fmt.Sprintf("no daemon is listening and the record cannot be read directly: %v", err),
				"start one with `oberth serve --detach`")
		}
		// No database file at all: nothing has ever been recorded on this
		// machine, which is the same answer as an empty record. A database that
		// is there but unreadable is a failure, and is reported as one.
		st = nil
	}
	if st != nil {
		defer func() { _ = st.Close() }()
	}
	out, err := fn(st)
	if err != nil {
		return zero, workplaceError(err)
	}
	return out, nil
}

// withDaemonWrite runs a write through the daemon, starting one if needed.
// A write has nowhere else to go: the daemon is the single writer of the
// record, and a second writer is how two clients lose each other's work.
func withDaemonWrite[T any](ctx context.Context, method string, params any) (T, error) {
	var zero T
	c, err := connectForWrite(ctx)
	if err != nil {
		return zero, err
	}
	defer c.Close()
	var out T
	if err := c.Call(ctx, method, params, &out); err != nil {
		return zero, cliError(err)
	}
	return out, nil
}

// ------------------------------------------------------------------ list ---

func newWorkplaceListCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "list",
		Short: "This worktree's workplaces, oldest first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			res, err := withRecord(cmd.Context(), "workplace.list",
				rpc.WorkplaceParams{Worktree: root},
				func(st *store.Store) (rpc.WorkplaceListResult, error) {
					return workplace.ListResult(st, root)
				})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			printWorkplaceList(cmd.OutOrStdout(), res)
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	return command
}

func printWorkplaceList(w io.Writer, res rpc.WorkplaceListResult) {
	if len(res.Workplaces) == 0 {
		fmt.Fprintf(w, "%s\n", display.Dim("no workplace yet in "+shortPath(res.Worktree)))
		fmt.Fprintf(w, "%s\n", display.Dim("`oberth workplace new <name>` starts one"))
		return
	}
	fmt.Fprintf(w, "%s %s\n", display.Bold(shortPath(res.Worktree)), display.Dim(blocksCount(len(res.Workplaces), "workplace")))
	width := 0
	for _, wp := range res.Workplaces {
		if n := display.DisplayWidth(wp.Name); n > width {
			width = n
		}
	}
	for _, wp := range res.Workplaces {
		mark := display.Dim("○")
		if wp.Active {
			mark = display.Green("●")
		}
		fmt.Fprintf(w, "  %s %s  %s\n", mark,
			display.PadDisplay(wp.Name, width),
			display.Dim(blocksCount(wp.Blocks, "block")))
	}
}

// ------------------------------------------------------------------- new ---

func newWorkplaceNewCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "new [name]",
		Short: "Start a workplace and record into it",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			res, err := withDaemonWrite[rpc.WorkplaceNewResult](cmd.Context(), "workplace.new",
				rpc.WorkplaceNewParams{Worktree: root, Name: name})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "created %s %s — new work is recorded here\n",
				display.Bold(res.Workplace.Name), display.Dim(res.Workplace.ID))
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	return command
}

// ------------------------------------------------------------------- use ---

func newWorkplaceUseCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "use <name|id>",
		Short: "Record new work into an existing workplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			res, err := withDaemonWrite[rpc.WorkplaceUseResult](cmd.Context(), "workplace.use",
				rpc.WorkplaceUseParams{Worktree: root, Selector: args[0]})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "now recording into %s %s (%s)\n",
				display.Bold(res.Workplace.Name), display.Dim(res.Workplace.ID),
				blocksCount(res.Workplace.Blocks, "block"))
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	return command
}

// ------------------------------------------------------------------ show ---

func newWorkplaceShowCommand() *cobra.Command {
	var limit int
	command := &cobra.Command{
		Use:   "show [name|id]",
		Short: "Read back what was done in a workplace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			if limit < 0 {
				return usageError{fmt.Errorf("--limit cannot be negative")}
			}
			selector := ""
			if len(args) == 1 {
				selector = args[0]
			}
			res, err := withRecord(cmd.Context(), "workplace.show",
				rpc.WorkplaceShowParams{Worktree: root, Selector: selector, Limit: limit},
				func(st *store.Store) (rpc.WorkplaceShowResult, error) {
					return workplace.ShowResult(st, root, selector, limit)
				})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			printWorkplaceShow(cmd.OutOrStdout(), res)
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	command.Flags().IntVar(&limit, "limit", 0, "Show at most this many of the newest blocks")
	return command
}

func printWorkplaceShow(w io.Writer, res rpc.WorkplaceShowResult) {
	head := fmt.Sprintf("%s · %s · %s", res.Workplace.Name, shortPath(res.Workplace.Worktree),
		blocksCount(res.Workplace.Blocks, "block"))
	fmt.Fprintf(w, "%s\n", display.Bold(head))
	if len(res.Blocks) == 0 {
		fmt.Fprintf(w, "  %s\n", display.Dim("nothing recorded yet"))
		return
	}
	if res.Truncated {
		fmt.Fprintf(w, "  %s\n", display.Dim(fmt.Sprintf("showing the newest %d of %d blocks",
			len(res.Blocks), res.Workplace.Blocks)))
	}
	width := 0
	for _, b := range res.Blocks {
		if n := display.DisplayWidth(oneLine(b.Input)); n > width {
			width = n
		}
	}
	if width > 60 {
		width = 60
	}
	for _, b := range res.Blocks {
		fmt.Fprintf(w, "  %3d  %-5s  %-11s  %s  %s\n", b.Seq, b.Kind, colorBlockStatus(b.Status),
			display.PadDisplay(truncateDisplay(oneLine(b.Input), width), width),
			display.Dim(blockDetail(b)))
	}
}

// blockDetail is the evidence a block points at: where its output went, how it
// ended, or which agent ran it. A block with none of those says nothing rather
// than inventing a status.
func blockDetail(b rpc.WorkplaceBlock) string {
	switch {
	case b.LogPath != "":
		return "log " + shortPath(b.LogPath)
	case b.ExitCode != nil:
		return fmt.Sprintf("exit %d", *b.ExitCode)
	case b.Provider != "":
		return b.Provider
	default:
		return ""
	}
}

func colorBlockStatus(status string) string {
	// The column is padded before colouring: an escape sequence inside
	// PadDisplay would count as display width and break the alignment.
	switch status {
	case string(workplace.StatusRunning):
		return display.Yellow(display.PadDisplay(status, 11))
	case string(workplace.StatusCompleted):
		return display.Green(display.PadDisplay(status, 11))
	case string(workplace.StatusFailed):
		return display.Red(display.PadDisplay(status, 11))
	default:
		return display.Dim(display.PadDisplay(status, 11))
	}
}

// ---------------------------------------------------------------- delete ---

func newWorkplaceDeleteCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "delete <name|id>",
		Short: "Delete a workplace and everything recorded in it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			res, err := withDaemonWrite[rpc.WorkplaceDeleteResult](cmd.Context(), "workplace.delete",
				rpc.WorkplaceDeleteParams{Worktree: root, Selector: args[0]})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			// The name the person used is the one they recognise; the id it
			// resolved to is in the JSON.
			if res.Active != nil {
				fmt.Fprintf(cmd.OutOrStdout(), "deleted %s — now recording into %s\n",
					display.Bold(args[0]), display.Bold(res.Active.Name))
			} else {
				fmt.Fprintf(cmd.OutOrStdout(), "deleted %s — this worktree has no workplace left\n",
					display.Bold(args[0]))
			}
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	return command
}

// ----------------------------------------------------------------- record ---

func newWorkplaceRecordCommand() *cobra.Command {
	block := &workplaceRecordBlock
	command := &cobra.Command{
		Use:   "record",
		Short: "Record work the engine did not start",
		Long: "Append one block to this worktree's active workplace.\n\n" +
			"This is for work option-berth cannot observe itself — an interactive shell or a\n" +
			"native agent in a front-end's own terminal. Blocks recorded this way carry\n" +
			"`source: reported`: the front-end's account, not the engine's evidence, which is\n" +
			"why there is no flag to claim otherwise. Work the engine starts (`oberth start`,\n" +
			"`oberth agent stream`) is recorded without asking and is marked `engine`.\n\n" +
			"Reporting the same id twice updates one block rather than adding a second, so the\n" +
			"start and the end of a piece of work can arrive in either order.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			root, err := workplaceRoot()
			if err != nil {
				return err
			}
			if block.Kind == "" {
				return usageError{fmt.Errorf("--kind is required (shell or agent)")}
			}
			// Everything this command writes is a report. Only the engine's own
			// recording paths mark a block as its evidence, and they do it over
			// the RPC rather than through here.
			block.Source = string(workplace.SourceReported)
			if workplaceRecordOutputFile != "" {
				if block.Output != "" {
					return usageError{fmt.Errorf("use either --output or --output-file")}
				}
				data, err := os.ReadFile(workplaceRecordOutputFile)
				if err != nil {
					return fail("not_found", "reading %s: %v", shortPath(workplaceRecordOutputFile), err)
				}
				block.Output = string(data)
			}
			block.StartedAt = workplaceRecordStartedAt
			block.EndedAt = workplaceRecordEndedAt
			if cmd.Flags().Changed("exit-code") {
				code := workplaceRecordExitCode
				block.ExitCode = &code
			}
			res, err := withDaemonWrite[rpc.WorkplaceRecordResult](cmd.Context(), "workplace.record",
				rpc.WorkplaceRecordParams{Worktree: root, Block: *block})
			if err != nil {
				return err
			}
			if workplaceJSONFlag {
				return printJSON(res)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "recorded %s block %d in %s (%s)\n",
				res.Block.Kind, res.Block.Seq, display.Bold(res.Workplace.Name), res.Block.Status)
			return nil
		},
	}
	command.Flags().BoolVar(&workplaceJSONFlag, "json", false, "Output as JSON")
	command.Flags().StringVar(&block.Kind, "kind", "", "What was recorded: shell or agent")
	command.Flags().StringVar(&block.ID, "id", "",
		"Identifier for this piece of work; reuse it to report the same work again")
	command.Flags().StringVar(&block.Input, "input", "", "The command line or the task prompt")
	command.Flags().StringVar(&block.Status, "status", "",
		"running, completed, failed or interrupted; defaults to running")
	command.Flags().StringVar(&block.Provider, "provider", "", "Agent provider, for kind=agent")
	command.Flags().StringVar(&block.RunID, "run-id", "", "The engine run this work became, if any")
	command.Flags().IntVar(&block.PID, "pid", 0, "Process id, if the reporter knows it")
	command.Flags().StringVar(&block.LogPath, "log-path", "", "Where the output was written, if anywhere")
	command.Flags().StringVar(&block.Output, "output", "", "Output text for a reported block")
	command.Flags().StringVar(&workplaceRecordOutputFile, "output-file", "", "Read the output text from this file")
	command.Flags().IntVar(&workplaceRecordExitCode, "exit-code", 0, "Exit code, for work that has ended")
	command.Flags().StringVar(&workplaceRecordStartedAt, "started-at", "", "RFC 3339 UTC; defaults to now")
	command.Flags().StringVar(&workplaceRecordEndedAt, "ended-at", "", "RFC 3339 UTC; defaults to now when the status is terminal")
	return command
}

// ----------------------------------------------------------------- utils ---

func blocksCount(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// oneLine folds a command line or prompt into one line, so a multi-line input
// cannot break the table it is printed in.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// truncateDisplay cuts a string to a display width, marking the cut. It works
// in runes so a truncated CJK prompt does not end in half a character.
func truncateDisplay(s string, width int) string {
	if width <= 0 || display.DisplayWidth(s) <= width {
		return s
	}
	var out []rune
	used := 0
	for _, r := range s {
		w := display.DisplayWidth(string(r))
		if used+w > width-1 {
			break
		}
		out = append(out, r)
		used += w
	}
	return string(out) + "…"
}
