package display

import "testing"

// The first screen paints the brand mark in the client's two colours, which
// only the richer terminals can carry exactly. Each capability level gets the
// nearest colour it can actually render, and the choice is a pure function of
// the environment so it can be pinned here rather than eyeballed.
func TestMarkColoursFollowTerminalCapabilities(t *testing.T) {
	cases := []struct {
		name       string
		colorterm  string
		term       string
		colorfgbg  string
		wantInk    string
		wantAccent string
	}{
		{"truecolor dark", "truecolor", "xterm-256color", "15;0",
			"\033[38;2;248;246;242m", "\033[38;2;219;147;120m"},
		{"truecolor light", "24bit", "xterm-256color", "0;15",
			"\033[38;2;35;38;43m", "\033[38;2;168;84;62m"},
		{"256 dark", "", "xterm-256color", "15;0", "\033[38;5;255m", "\033[38;5;174m"},
		{"256 light", "", "xterm-256color", "0;15", "\033[38;5;235m", "\033[38;5;131m"},
		// Eight colours have no terracotta, and no ink or paper either: the
		// rail stays the terminal's own foreground, the capsule gets yellow.
		{"basic", "", "xterm", "15;0", "", "\033[33m"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotInk, gotAccent := markSequences(tc.colorterm, tc.term, tc.colorfgbg)
			if gotInk != tc.wantInk || gotAccent != tc.wantAccent {
				t.Fatalf("markSequences(%q, %q, %q) = (%q, %q), want (%q, %q)",
					tc.colorterm, tc.term, tc.colorfgbg, gotInk, gotAccent, tc.wantInk, tc.wantAccent)
			}
		})
	}
}

// A terminal that never says what its background is gets the light forms:
// guessing dark would put a washed-out mark on a light background, and asking
// would mean an OSC round-trip on every run.
func TestMarkColoursDefaultToTheLightForms(t *testing.T) {
	for _, colorfgbg := range []string{"", "0", "not-a-colour", "0;", "0;default"} {
		gotInk, gotAccent := markSequences("truecolor", "xterm-256color", colorfgbg)
		if want := "\033[38;2;35;38;43m"; gotInk != want {
			t.Errorf("COLORFGBG=%q gave ink %q, want %q", colorfgbg, gotInk, want)
		}
		if want := "\033[38;2;168;84;62m"; gotAccent != want {
			t.Errorf("COLORFGBG=%q gave accent %q, want %q", colorfgbg, gotAccent, want)
		}
	}
}

// Dark is the eight low slots and nothing else: COLORFGBG reports a colour
// index, so 8 and above are the bright half and a light background.
func TestDarkBackgroundReadsTheLastColorfgbgField(t *testing.T) {
	dark := []string{"15;0", "7;1", "0;7", " 15 ; 3 "}
	light := []string{"", "0", "15", "0;8", "0;15", "0;default", "0;-1"}
	for _, value := range dark {
		if !darkBackground(value) {
			t.Errorf("darkBackground(%q) = false, want true", value)
		}
	}
	for _, value := range light {
		if darkBackground(value) {
			t.Errorf("darkBackground(%q) = true, want false", value)
		}
	}
}

// The rail and the accent obey the same NO_COLOR switch as every other colour
// in the package, and an empty sequence — the rail on an eight-colour terminal
// — leaves the text alone rather than wrapping it in a bare reset.
func TestBrandColoursHonourNoColorAndEmptySequences(t *testing.T) {
	restore := NoColor
	NoColor = true
	t.Cleanup(func() { NoColor = restore })

	if got := Ink("mark"); got != "mark" {
		t.Fatalf("Ink with NoColor = %q, want the plain text", got)
	}
	if got := Accent("mark"); got != "mark" {
		t.Fatalf("Accent with NoColor = %q, want the plain text", got)
	}

	NoColor = false
	if got := brandPaint("mark", ""); got != "mark" {
		t.Fatalf("brand with an empty sequence = %q, want the plain text", got)
	}
	if got := brandPaint("mark", "\033[33m"); got != "\033[33mmark\033[0m" {
		t.Fatalf("brand with a sequence = %q, want it wrapped", got)
	}
}

// When a process paints at all is decided in one place, from three
// environment facts: every combination is pinned here instead of being
// discovered in a terminal.
func TestColorEnabledReadsTheEnvironment(t *testing.T) {
	cases := []struct {
		name       string
		noColorEnv bool
		stdoutTTY  bool
		term       string
		want       bool
	}{
		{"an ordinary terminal", false, true, "xterm-256color", true},
		{"NO_COLOR set", true, true, "xterm-256color", false},
		{"stdout redirected", false, false, "xterm-256color", false},
		{"TERM=dumb", false, true, "dumb", false},
		{"no TERM", false, true, "", false},
		// NO_COLOR wins over everything else, whatever its value.
		{"NO_COLOR beats a terminal", true, true, "xterm-256color", false},
		{"NO_COLOR in a pipe", true, false, "xterm", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ColorEnabled(tc.noColorEnv, tc.stdoutTTY, tc.term); got != tc.want {
				t.Fatalf("ColorEnabled(%v, %v, %q) = %v, want %v",
					tc.noColorEnv, tc.stdoutTTY, tc.term, got, tc.want)
			}
		})
	}
}

// Paint wraps in the style's own parameters and closes them; with colour off or
// a style that names nothing, the text comes back exactly as it went in.
func TestPaintWrapsAndCloses(t *testing.T) {
	restore := NoColor
	t.Cleanup(func() { NoColor = restore })

	NoColor = false
	if got, want := Bold("x"), "\033[1mx\033[0m"; got != want {
		t.Errorf("Bold = %q, want %q", got, want)
	}
	if got, want := Underline("x"), "\033[4;34mx\033[0m"; got != want {
		t.Errorf("Underline = %q, want %q (two parameters, one sequence)", got, want)
	}
	if got := (Style{}).Paint("x"); got != "x" {
		t.Errorf("an empty style = %q, want the plain text", got)
	}

	NoColor = true
	if got := Bold("x"); got != "x" {
		t.Errorf("Bold with NoColor = %q, want the plain text", got)
	}
}
