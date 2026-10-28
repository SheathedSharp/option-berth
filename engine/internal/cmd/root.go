package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

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

// The first screen opens with the brand: the mark, then the wordmark beside it.
// The mark is generated into mark_gen.go from the same 24x24 outline the client
// draws and the brand SVGs export, so it cannot drift from the logo. The
// wordmark is an outline font a terminal cannot reproduce, so it is set in the
// terminal's own type instead of being redrawn in blocks.
const wordmark = "option-berth"

// taglineLines is the one-line positioning statement, wrapped so it sits beside
// the mark instead of running past it.
var taglineLines = []string{
	"Read a worktree's manifest and runtime,",
	"then start or stop its services.",
}

// logoLines is the logo: the mark with the wordmark and tagline beside it. The
// mark is taller than the text, so its last rows stand alone.
func logoLines() []string {
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	text := append([]string{display.Bold(wordmark)}, dimmed(taglineLines)...)
	width := 0
	for _, row := range rail {
		if w := display.DisplayWidth(row); w > width {
			width = w
		}
	}
	lines := make([]string, len(rail))
	for i, row := range rail {
		// The two layers are padded to the same width so a cell's shape is
		// read from the same column in both. Only the rows that carry text get
		// padded on the screen — padding the rest would leave trailing spaces.
		tokenRow := ""
		if i < len(token) {
			tokenRow = token[i]
		}
		if i < len(text) {
			lines[i] = paintMarkRow(display.PadDisplay(row, width),
				display.PadDisplay(tokenRow, width)) + "   " + text[i]
			continue
		}
		lines[i] = strings.TrimRight(paintMarkRow(row, tokenRow), " ")
	}
	return lines
}

// markLayer says which of the mark's two shapes owns a cell. The generator
// emits one layer per shape, each with a space wherever the other owns the
// cell, so a cell never belongs to both.
type markLayer int

const (
	layerNone markLayer = iota
	layerRail
	layerToken
)

func (l markLayer) paint(s string) string {
	switch l {
	case layerRail:
		return display.Ink(s)
	case layerToken:
		return display.Accent(s)
	default:
		return s
	}
}

func cellAt(rail, token []rune, i int) (rune, markLayer) {
	if i < len(rail) && rail[i] != ' ' {
		return rail[i], layerRail
	}
	if i < len(token) && token[i] != ' ' {
		return token[i], layerToken
	}
	return ' ', layerNone
}

// paintMarkRow draws one row, grouping a run of cells that belong to the same
// shape under a single colour sequence rather than one escape per cell.
func paintMarkRow(rail, token string) string {
	r, t := []rune(rail), []rune(token)
	var out strings.Builder
	for i := 0; i < len(r); {
		_, layer := cellAt(r, t, i)
		j := i + 1
		for j < len(r) {
			if _, next := cellAt(r, t, j); next != layer {
				break
			}
			j++
		}
		source := r
		if layer == layerToken {
			source = t
		}
		out.WriteString(layer.paint(string(source[i:j])))
		i = j
	}
	return out.String()
}

func dimmed(lines []string) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = display.Dim(line)
	}
	return out
}

// logoBlock is the logo as one string, on the screen's two-space margin.
// `oberth --help` opens with it too, so both screens start the same way.
func logoBlock() string {
	lines := logoLines()
	for i := range lines {
		lines[i] = "  " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// quickStart is the first screen's whole point: the commands a work session
// actually runs, so the screen answers "what do I type" before "what exists".
// Everything else stays one --help away.
var quickStart = []struct{ cmd, what string }{
	{"oberth status", "What this worktree declares, what is running, what changed"},
	{"oberth up", "Start this worktree's services"},
	{"oberth down", "Stop them and give the ports back"},
}

var rootCmd = &cobra.Command{
	Use:   "oberth",
	Short: "Manage services for the current worktree",
	Long:  logoBlock(),
	RunE: func(cmd *cobra.Command, args []string) error {
		if versionFlag {
			// Keep the shell-facing version probe deliberately small. Detailed
			// build identity remains available through `version --json`.
			fmt.Printf("option-berth %s\n", buildinfo.VersionValue())
			return nil
		}
		printWelcome(cmd.OutOrStdout())
		return nil
	},
}

// printWelcome renders the bare-`oberth` screen: the logo with its one line of
// what this is, the commands a session starts with, and the way out to
// everything else. It is a signpost, not the manual — the grouped command list
// stays on `oberth --help` (gh's split between first screen and help).
func printWelcome(w io.Writer) {
	fmt.Fprintln(w, logoBlock())
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Start here:")
	width := 0
	for _, q := range quickStart {
		if n := len(q.cmd); n > width {
			width = n
		}
	}
	for _, q := range quickStart {
		fmt.Fprintf(w, "    %s  %s\n", display.Bold(display.PadDisplay(q.cmd, width)), display.Dim(q.what))
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  Learn more:")
	for _, q := range []struct{ cmd, what string }{
		{"oberth --help", "Every command, grouped by what it is for"},
		{"oberth doctor", "Check this installation"},
	} {
		fmt.Fprintf(w, "    %s  %s\n", display.Bold(display.PadDisplay(q.cmd, width)), display.Dim(q.what))
	}
}

// The help is grouped by what the reader is trying to do, not by which part of
// the engine does it: an agent looking at `oberth --help` is asking "what is it
// doing", "make it run", "what does the code say", "hand work over or take it
// back", or "fix the installation" — never "which module is this". The layers
// are the ones docs/product.md and docs/cli.md name, and the contract test pins
// every command to one of them.
const (
	commandGroupFacts      = "facts"
	commandGroupOperations = "operations"
	commandGroupCode       = "code"
	commandGroupHandoff    = "handoff"
	commandGroupInfra      = "infra"
)

func init() {
	rootCmd.AddGroup(
		&cobra.Group{ID: commandGroupFacts, Title: "Facts:"},
		&cobra.Group{ID: commandGroupOperations, Title: "Operations:"},
		&cobra.Group{ID: commandGroupCode, Title: "Code and declaration:"},
		&cobra.Group{ID: commandGroupHandoff, Title: "Handoff:"},
		&cobra.Group{ID: commandGroupInfra, Title: "Runtime and setup:"},
	)
	// cobra's built-in help command would otherwise land in a lone
	// "Additional Commands" section, which breaks the layer grouping the
	// contract test pins. It is setup tooling, so it belongs with the rest of
	// Runtime and setup.
	rootCmd.SetHelpCommandGroupID(commandGroupInfra)
	rootCmd.SetCompletionCommandGroupID(commandGroupInfra)
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
