package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/buildinfo"
	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/ports"
	"github.com/sheathedsharp/option-berth/engine/internal/versioncheck"
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

// logoLines is the logo: the mark with the wordmark, tagline and any extra
// identity lines beside it. The mark is taller than the text, so its last rows
// stand alone.
func logoLines(extra ...string) []string {
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	text := make([]string, 0, 1+len(taglineLines)+len(extra))
	text = append(text, display.Bold(wordmark))
	text = append(text, dimmed(taglineLines)...)
	text = append(text, extra...)
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
func logoBlock(extra ...string) string {
	lines := logoLines(extra...)
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

// agentReads is the reading pattern `oberth --help` hands a coding agent, and
// the reason it is on the help screen at all: an agent pays for every byte it
// reads, so the cheapest shape of each answer is part of the interface, not a
// tip. These are the three reads a session starts with — the runtime facts, the
// operation that waits for real readiness, and the one line that explains a
// failure. The last row has no description: it is already as long as the
// column allows, and its flags say what it does.
var agentReads = []struct{ cmd, what string }{
	{"oberth status --json --brief", "runtime facts only; narrow with --only <service>"},
	{"oberth up --wait --json", "start and wait until really ready"},
	{"oberth logs <service> --once --json --run current --first-error", ""},
}

// agentHelp is the block `oberth --help` ends with. The width is the widest
// command that carries a description, so the descriptions line up and the long
// row stands alone without trailing spaces.
func agentHelp() string {
	width := 0
	for _, r := range agentReads {
		if r.what != "" && len(r.cmd) > width {
			width = len(r.cmd)
		}
	}
	var b strings.Builder
	b.WriteString("For agents:\n")
	for _, r := range agentReads {
		if r.what == "" {
			b.WriteString("  " + r.cmd + "\n")
			continue
		}
		b.WriteString("  " + display.PadDisplay(r.cmd, width) + "  " + r.what + "\n")
	}
	return b.String()
}

// installAgentHelp appends the agent reading block to the root help screen and
// leaves every other command's help exactly as cobra renders it. The block is a
// reading pattern, not a second command list: the grouped layers above it stay
// the only place commands are enumerated, and a subcommand's own --help is
// still the manual for that command.
func installAgentHelp() {
	base := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(c *cobra.Command, args []string) {
		if c != rootCmd {
			base(c, args)
			return
		}
		out := c.OutOrStdout()
		var rendered bytes.Buffer
		c.SetOut(&rendered)
		base(c, args)
		c.SetOut(out)
		fmt.Fprint(out, withAgentHelp(rendered.String()))
	})
}

// withAgentHelp splices the block in above cobra's trailing pointer to
// `[command] --help`, so the screen still ends by sending the reader to the
// manual. The trailing line is located rather than recomposed: the rest of the
// screen is cobra's, and only that one line must stay last.
func withAgentHelp(text string) string {
	const pointer = "\nUse \""
	if i := strings.LastIndex(text, pointer); i >= 0 {
		return text[:i] + "\n" + agentHelp() + text[i:]
	}
	return text + "\n" + agentHelp()
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
		printWelcome(cmd.OutOrStdout(), welcomeVersion())
		return nil
	},
}

// welcomeVersion is the version line the signpost can show. It only reaches the
// release feed when a terminal is attached, the user has not opted out, and
// this build is a release; an unknown answer stays unknown.
func welcomeVersion() versioncheck.State {
	current := buildinfo.VersionValue()
	state := versioncheck.State{Current: current}
	if !versioncheck.IsRelease(current) || !versioncheck.Enabled() {
		return state
	}
	checked, _ := versioncheck.Lookup(context.Background(), current, false, versioncheck.WelcomeTimeout)
	return checked
}

// printWelcome renders the bare-`oberth` screen: the logo with its one line of
// what this is, the commands a session starts with, and the way out to
// everything else. It is a signpost, not the manual — the grouped command list
// stays on `oberth --help` (gh's split between first screen and help).
func printWelcome(w io.Writer, version versioncheck.State) {
	fmt.Fprintln(w, logoBlock(versionLine(version)))
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
	if version.UpdateAvailable() {
		printUpgrade(w, version.Latest)
	}
}

// versionLine is the build identity under the tagline: what this binary is,
// and — when a check has run — whether it is the latest release. An unknown
// answer prints only the version; "not checked" never reads as "latest".
func versionLine(state versioncheck.State) string {
	current := state.Current
	if current == "" {
		current = buildinfo.VersionValue()
	}
	line := display.Bold(current)
	switch state.Relation() {
	case versioncheck.Same:
		line += " " + display.Green("(latest)")
	case versioncheck.Behind:
		line += " " + display.Yellow("(latest version "+state.Latest+")")
	}
	return line
}

// printUpgrade is the one action the signpost offers when this build is behind:
// the install command for this platform, and the daemon restart that makes the
// new binary the one that runs next.
func printUpgrade(w io.Writer, latest string) {
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  "+display.Yellow("Upgrade to "+latest+":"))
	fmt.Fprintf(w, "    %s\n", display.Bold(upgradeCommand(runtime.GOOS)))
	fmt.Fprintf(w, "    %s  %s\n", display.Bold("oberth daemon stop"), display.Dim("# pick up the new build"))
}

// upgradeCommand is the published install entry point for goos. It is the same
// command docs/install.md gives, so a reader can compare the two.
func upgradeCommand(goos string) string {
	if goos == "windows" {
		return "irm https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.ps1 | iex"
	}
	return "curl -fsSL https://raw.githubusercontent.com/SheathedSharp/option-berth/main/install.sh | sh"
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
	installAgentHelp()
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
