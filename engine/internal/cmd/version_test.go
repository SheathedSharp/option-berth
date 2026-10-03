package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/display"
)

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
	// The manual is elsewhere: no grouped headers, no full flag block.
	for _, gone := range []string{"Worktree lifecycle:", "Inspection and explicit controls:", "Usage:", "Flags:"} {
		if strings.Contains(out, gone) {
			t.Errorf("welcome should not carry the manual's %q:\n%s", gone, out)
		}
	}
}
