package cmd

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestVersionJSONCarriesBuildIdentity(t *testing.T) {
	previous := versionJSONFlag
	versionJSONFlag = true
	t.Cleanup(func() { versionJSONFlag = previous })

	out := captureStdout(t, func() {
		if err := versionRun(testCommand(), nil); err != nil {
			t.Fatalf("versionRun: %v", err)
		}
	})

	var got versionDocument
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); err != nil {
		t.Fatalf("version JSON: %v\n%s", err, out)
	}
	if got.Version == "" || got.Platform == "" {
		t.Fatalf("version document = %+v, want version and platform", got)
	}
}

func TestRootShortVersionFlag(t *testing.T) {
	previous := versionFlag
	versionFlag = true
	t.Cleanup(func() { versionFlag = previous })

	out := captureStdout(t, func() {
		if err := rootCmd.RunE(rootCmd, nil); err != nil {
			t.Fatalf("root version: %v", err)
		}
	})
	got := strings.TrimSpace(out)
	if got == "" || strings.Contains(got, "commit") || strings.Contains(got, "built") {
		t.Fatalf("short version output = %q, want only the CLI version", got)
	}
}

// The first screen is where the brand shows up, so the mark has to be the real
// one. markRail and markToken are generated (mark_gen.go) from the client's
// geometry, and a regeneration that came out empty or misshapen would otherwise
// ship silently.
func TestWelcomeShowsTheBrandMarkBesideTheWordmark(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	// paintMarkRow reads a cell's shape from the same column in both layers, so
	// the generator has to keep them the same shape. If it ever stops, the
	// colour would land on the wrong cells rather than fail loudly.
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	if len(rail) != len(token) {
		t.Fatalf("mark layers have %d and %d rows, want the same", len(rail), len(token))
	}
	mark := make([]string, len(rail))
	for i := range rail {
		if r, tk := []rune(rail[i]), []rune(token[i]); len(r) != len(tk) {
			t.Fatalf("mark row %d is %d and %d cells wide, want the same", i, len(r), len(tk))
		}
		mark[i] = strings.TrimRight(paintMarkRow(rail[i], token[i]), " ")
	}
	if len(mark) < 2 || strings.TrimSpace(strings.Join(mark, "")) == "" {
		t.Fatalf("mark = %q, want a multi-row mark", mark)
	}

	var buf bytes.Buffer
	printWelcome(&buf)
	lines := strings.Split(buf.String(), "\n")
	if len(lines) < len(mark) {
		t.Fatalf("welcome has %d rows, want at least the %d mark rows", len(lines), len(mark))
	}
	width := 0
	for _, row := range mark {
		if w := display.DisplayWidth(row); w > width {
			width = w
		}
	}
	for i, row := range mark {
		if !strings.HasPrefix(lines[i], "  "+row) {
			t.Errorf("welcome row %d = %q, want mark row %q on the two-space margin",
				i, lines[i], row)
		}
	}
	// The wordmark and the tagline sit beside the mark, not under it: they
	// start in the column just past the mark's widest row.
	for i, want := range append([]string{wordmark}, taglineLines...) {
		prefix := "  " + display.PadDisplay(mark[i], width) + "   "
		if !strings.HasPrefix(lines[i], prefix) || !strings.HasSuffix(lines[i], want) {
			t.Errorf("welcome row %d = %q, want %q in the column after the mark",
				i, lines[i], want)
		}
	}
}

// The mark is drawn in the brand's two colours — rails in ink/paper, capsule in
// terracotta. Painting it in one colour is what made the capsule read as a
// third blob beside the two rail caps, so which layer owns a cell is the point.
// Whichever colours the terminal ends up with, the glyphs must not move.
func TestMarkLayersPaintEveryCellExactlyOnce(t *testing.T) {
	rail := strings.Split(strings.TrimSuffix(markRail, "\n"), "\n")
	token := strings.Split(strings.TrimSuffix(markToken, "\n"), "\n")
	if len(rail) != len(token) {
		t.Fatalf("mark layers have %d and %d rows, want the same", len(rail), len(token))
	}
	var sawRail, sawToken bool
	for i := range rail {
		r, tk := []rune(rail[i]), []rune(token[i])
		if len(r) != len(tk) {
			t.Fatalf("mark row %d is %d and %d cells wide, want the same", i, len(r), len(tk))
		}
		merged := make([]rune, len(r))
		for j := range r {
			switch {
			case r[j] != ' ' && tk[j] != ' ':
				t.Fatalf("mark row %d cell %d is claimed by both layers", i, j)
			case r[j] != ' ':
				merged[j], sawRail = r[j], true
			case tk[j] != ' ':
				merged[j], sawToken = tk[j], true
			default:
				merged[j] = ' '
			}
		}
		if got := ansiEscape.ReplaceAllString(paintMarkRow(rail[i], token[i]), ""); got != string(merged) {
			t.Errorf("painted row %d = %q, want the mark %q", i, got, string(merged))
		}
	}
	if !sawRail || !sawToken {
		t.Fatalf("mark has rail=%v token=%v, want both shapes present", sawRail, sawToken)
	}
}

// The bare `oberth` screen is a signpost, not the manual: the logo, one line
// of what this is, the commands a session starts with, and the way out to the
// full grouped help.
func TestPrintWelcomeShowsQuickStartNotTheManual(t *testing.T) {
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })

	var buf bytes.Buffer
	printWelcome(&buf)
	out := buf.String()

	for _, want := range []string{
		"oberth status", "oberth up", "oberth down",
		"Start here:", "Learn more:", "oberth --help",
		// the one-line identity
		"manifest and runtime",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("welcome is missing %q:\n%s", want, out)
		}
	}
	// The command column lines up: every description starts at the same cell.
	lines := strings.Split(out, "\n")
	desc := -1
	for _, l := range lines {
		if !strings.HasPrefix(l, "    oberth ") {
			continue
		}
		// The description starts after the padded command and its spaces.
		cmdLen := 4
		for cmdLen < len(l) && l[cmdLen] != ' ' {
			cmdLen++
		}
		for cmdLen < len(l) && l[cmdLen] == ' ' {
			cmdLen++
		}
		if desc == -1 {
			desc = cmdLen
		} else if desc != cmdLen {
			t.Fatalf("description column drifted between rows:\n%s", out)
		}
	}
	// The manual is elsewhere: no grouped headers, no full flag block. The
	// headers named here are the layers docs/cli.md documents, so this test
	// fails if the welcome screen starts carrying the manual's grouping.
	for _, gone := range []string{"Facts:", "Operations:", "Code and declaration:", "Handoff:", "Usage:", "Flags:"} {
		if strings.Contains(out, gone) {
			t.Errorf("welcome should not carry the manual's %q:\n%s", gone, out)
		}
	}
}
