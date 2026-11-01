package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

// rootHelp renders `oberth --help` the way cobra would, with color off so the
// assertions compare words rather than escape sequences.
func rootHelp(t *testing.T) string {
	t.Helper()
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	t.Cleanup(func() { rootCmd.SetOut(nil) })
	rootCmd.HelpFunc()(rootCmd, nil)
	return buf.String()
}

// The agent block is the point of the change: `oberth --help` has to teach the
// cheap reading pattern, not just list commands. The three reads are pinned by
// their flags because a reader copies the line as written.
func TestRootHelpShowsHowAnAgentReads(t *testing.T) {
	out := rootHelp(t)

	for _, want := range []string{
		"For agents:",
		"oberth status --json --brief",
		"runtime facts only",
		"--only <service>",
		"oberth up --wait --json",
		"oberth logs <service> --once --json --run current --first-error",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("oberth --help is missing %q:\n%s", want, out)
		}
	}

	// The screen still ends by sending the reader to the manual: the block goes
	// above cobra's pointer, not after it.
	pointer := strings.Index(out, `Use "oberth [command] --help"`)
	block := strings.Index(out, "For agents:")
	if pointer < 0 || block < 0 || block > pointer {
		t.Fatalf("the agent block is not above the closing pointer:\n%s", out)
	}
	if strings.Count(out, "For agents:") != 1 {
		t.Errorf("the agent block appears more than once:\n%s", out)
	}
}

// Only the root screen carries the block. A subcommand's own --help stays the
// manual for that command: an agent that asked about `up` is not asking for a
// general reading pattern.
func TestSubcommandHelpStaysTheManualForThatCommand(t *testing.T) {
	for _, command := range rootCmd.Commands() {
		if command.Name() == "help" {
			continue
		}
		var buf bytes.Buffer
		rootCmd.SetOut(&buf)
		rootCmd.HelpFunc()(command, nil)
		if strings.Contains(buf.String(), "For agents:") {
			t.Errorf("%s --help carries the root agent block:\n%s", command.Name(), buf.String())
		}
	}
	rootCmd.SetOut(nil)
}

// The recommendation is only honest if the flags it names exist. Parse each
// row of agentReads and resolve it against the real command tree, so renaming a
// flag breaks this test instead of leaving the help teaching a command that
// fails.
func TestAgentHelpRecommendsFlagsThatExist(t *testing.T) {
	for _, row := range agentReads {
		fields := strings.Fields(row.cmd)
		if len(fields) < 2 {
			t.Fatalf("agentReads row %q names no command", row.cmd)
		}
		name := fields[1]
		var target = rootCmd
		for _, command := range rootCmd.Commands() {
			if command.Name() == name {
				target = command
				break
			}
		}
		if target == rootCmd {
			t.Errorf("agentReads recommends unknown command %q", name)
			continue
		}
		for _, field := range fields[2:] {
			if !strings.HasPrefix(field, "--") {
				continue // a positional placeholder such as <service>
			}
			flag := strings.TrimPrefix(strings.SplitN(field, "=", 2)[0], "--")
			found := target.Flags().Lookup(flag)
			if found == nil {
				t.Errorf("%s --%s does not exist (recommended by %q)", name, flag, row.cmd)
				continue
			}
			if found.Hidden {
				t.Errorf("%s --%s is hidden but recommended by %q", name, flag, row.cmd)
			}
		}
	}
}

// The rows line up: every description starts at the same column, and the row
// too long to carry one keeps no trailing padding.
func TestAgentHelpColumnsLineUp(t *testing.T) {
	block := agentHelp()
	lines := strings.Split(strings.TrimSuffix(block, "\n"), "\n")
	if lines[0] != "For agents:" || len(lines) != len(agentReads)+1 {
		t.Fatalf("block = %q, want a header and %d rows", block, len(agentReads))
	}

	// The command column is the widest command that carries a description; the
	// description sits two spaces past it on every row that has one.
	width := 0
	for _, row := range agentReads {
		if row.what != "" && len(row.cmd) > width {
			width = len(row.cmd)
		}
	}
	for i, row := range agentReads {
		line := lines[i+1]
		if row.what == "" {
			if line != "  "+row.cmd {
				t.Errorf("row %d = %q, want the bare command %q", i, line, row.cmd)
			}
			continue
		}
		want := "  " + row.cmd + strings.Repeat(" ", width-len(row.cmd)) + "  " + row.what
		if line != want {
			t.Errorf("row %d = %q, want %q", i, line, want)
		}
	}
}
