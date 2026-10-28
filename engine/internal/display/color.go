package display

import (
	"os"
	"strconv"
	"strings"
)

// ANSI escape codes
const (
	reset     = "\033[0m"
	bold      = "\033[1m"
	underline = "\033[4m"
	dim       = "\033[2m"

	red     = "\033[31m"
	green   = "\033[32m"
	yellow  = "\033[33m"
	blue    = "\033[34m"
	magenta = "\033[35m"
	cyan    = "\033[36m"
	white   = "\033[37m"
)

// NoColor tracks whether color output is disabled.
var NoColor bool

func init() {
	// Respect NO_COLOR convention (https://no-color.org)
	if _, ok := os.LookupEnv("NO_COLOR"); ok {
		NoColor = true
		return
	}

	// Detect if stdout is a pipe/not a terminal
	fi, err := os.Stdout.Stat()
	if err == nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		NoColor = true
		return
	}

	// Check TERM
	term := os.Getenv("TERM")
	if term == "dumb" || term == "" {
		NoColor = true
	}
}

func colorize(s string, codes ...string) string {
	if NoColor {
		return s
	}
	return strings.Join(codes, "") + s + reset
}

// The brand palette, from the client's BerthGeometry.swift. The mark is two
// shapes in two colours: the rails in ink, and the capsule — the run that has a
// berth of its own — in terracotta. Each has a light and a dark form, so a dark
// terminal gets paper rails and the lightened terracotta, exactly as the
// client's dark lockup does.
//
// None of it is a status colour. Status stays green/yellow/red/dim, and the
// brand README is explicit that terracotta never means "healthy".
const (
	inkRGB        = "#23262B"
	paperRGB      = "#F8F6F2"
	accentRGB     = "#A8543E"
	darkAccentRGB = "#DB9378"

	// Nearest xterm-256 slots to the four above, by sRGB distance.
	inkSlot, paperSlot         = 235, 255
	accentSlot, darkAccentSlot = 131, 174
)

// brandColour picks the SGR parameters for one brand colour from what the
// terminal advertises. basic is what an eight-colour terminal gets, and may be
// empty, meaning "leave this alone" — the right answer for the rail, whose
// ink/paper is best approximated by the terminal's own foreground.
//
// Kept a pure function of the environment so every branch is testable without
// a terminal.
func brandColour(light, dark string, lightSlot, darkSlot int, basic, colorterm, term, colorfgbg string) string {
	rgb, slot := light, lightSlot
	if darkBackground(colorfgbg) {
		rgb, slot = dark, darkSlot
	}
	switch {
	case isTruecolor(colorterm):
		return "\033[38;2;" + sgrRGB(rgb) + "m"
	case strings.Contains(term, "256color"):
		return "\033[38;5;" + strconv.Itoa(slot) + "m"
	default:
		return basic
	}
}

// markColours resolves both of the mark's colours from what the terminal
// advertises, so the pair can be pinned as a unit.
//
// The two eight-colour fallbacks differ on purpose. The rail's ink/paper is
// best approximated by the terminal's own foreground, so it is left alone. The
// accent gets yellow: the basic eight have no terracotta, and xterm's yellow
// (#AA5500) is the nearest of them to #A8543E — nearer than the red, which in
// this CLI already means "failed".
func markColours(colorterm, term, colorfgbg string) (string, string) {
	return brandColour(inkRGB, paperRGB, inkSlot, paperSlot, "", colorterm, term, colorfgbg),
		brandColour(accentRGB, darkAccentRGB, accentSlot, darkAccentSlot, yellow, colorterm, term, colorfgbg)
}

// The mark's two colours, resolved once at startup.
var ink, accent = markColours(os.Getenv("COLORTERM"), os.Getenv("TERM"), os.Getenv("COLORFGBG"))

func isTruecolor(colorterm string) bool {
	colorterm = strings.ToLower(strings.TrimSpace(colorterm))
	return colorterm == "truecolor" || colorterm == "24bit"
}

// darkBackground reads COLORFGBG, the "fg;bg" hint some terminals export, and
// reports whether its background is one of the eight dark slots. Terminals that
// do not export it (Terminal.app and iTerm2 among them) get the light forms:
// asking would mean an OSC round-trip on every run, and guessing dark would put
// a washed-out mark on a light background.
func darkBackground(colorfgbg string) bool {
	fields := strings.Split(colorfgbg, ";")
	if len(fields) < 2 {
		return false
	}
	index, err := strconv.Atoi(strings.TrimSpace(fields[len(fields)-1]))
	return err == nil && index >= 0 && index < 8
}

func sgrRGB(hex string) string {
	channel := func(from, to int) string {
		value, err := strconv.ParseUint(hex[from:to], 16, 8)
		if err != nil {
			return "0"
		}
		return strconv.FormatUint(value, 10)
	}
	return channel(1, 3) + ";" + channel(3, 5) + ";" + channel(5, 7)
}

// brand applies an already-resolved sequence. An empty sequence means this
// terminal has no colour for it, and the text is left as it is.
func brand(s, sequence string) string {
	if NoColor || sequence == "" {
		return s
	}
	return sequence + s + reset
}

func Bold(s string) string      { return colorize(s, bold) }
func Dim(s string) string       { return colorize(s, dim) }
func Ink(s string) string       { return brand(s, ink) }
func Accent(s string) string    { return brand(s, accent) }
func Red(s string) string       { return colorize(s, red) }
func Green(s string) string     { return colorize(s, green) }
func Yellow(s string) string    { return colorize(s, yellow) }
func Blue(s string) string      { return colorize(s, blue) }
func Magenta(s string) string   { return colorize(s, magenta) }
func Cyan(s string) string      { return colorize(s, cyan) }
func BoldCyan(s string) string  { return colorize(s, bold, cyan) }
func Underline(s string) string { return colorize(s, underline, blue) }
