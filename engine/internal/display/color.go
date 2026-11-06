package display

import (
	"os"
	"strconv"
	"strings"
)

// Style is the SGR parameters that turn terminal attributes on: the numbers
// between ESC[ and m. Painting is the package's only output operation, so a
// caller cannot emit an attribute it never closed.
type Style []int

// Paint wraps s in this style. Colour that is off, or a style that names
// nothing, leaves the text exactly as it was.
func (st Style) Paint(s string) string {
	if NoColor || len(st) == 0 {
		return s
	}
	params := make([]string, len(st))
	for i, code := range st {
		params[i] = strconv.Itoa(code)
	}
	return "\033[" + strings.Join(params, ";") + "m" + s + "\033[0m"
}

// painter is Paint with the style already chosen: it is what each exported name
// below is.
func (st Style) painter() func(string) string {
	return func(s string) string { return st.Paint(s) }
}

// The named styles this CLI uses. Status keeps to green/yellow/red/dim; the
// brand pair (Ink, Accent) is separate and never means "healthy".
var (
	Bold      = Style{1}.painter()
	Dim       = Style{2}.painter()
	Underline = Style{4, 34}.painter()
	Red       = Style{31}.painter()
	Green     = Style{32}.painter()
	Yellow    = Style{33}.painter()
	Cyan      = Style{36}.painter()
)

// NoColor turns every style off for the whole process. It is set once at
// startup from the environment and from what stdout is.
var NoColor bool

// ColorEnabled decides whether this process paints at all. It is a pure
// function of its inputs, so every rule is testable without a terminal.
//
// NO_COLOR wins whenever it is set, whatever its value (no-color.org); a stdout
// that is not a character device gets no colour because a pipe is going to a
// file or another program; and TERM=dumb or an unset TERM means the terminal
// cannot render it.
func ColorEnabled(hasNoColorEnv bool, stdoutIsTerminal bool, term string) bool {
	switch {
	case hasNoColorEnv:
		return false
	case !stdoutIsTerminal:
		return false
	case term == "dumb" || term == "":
		return false
	}
	return true
}

func init() {
	_, hasNoColor := os.LookupEnv("NO_COLOR")
	stdoutIsTerminal := false
	if fi, err := os.Stdout.Stat(); err == nil {
		stdoutIsTerminal = fi.Mode()&os.ModeCharDevice != 0
	}
	NoColor = !ColorEnabled(hasNoColor, stdoutIsTerminal, os.Getenv("TERM"))
}

// The brand palette, from the client's BerthGeometry.swift. The mark is two
// shapes in two colours: the rails in ink, and the capsule — the run that has a
// berth of its own — in terracotta. Each has a light and a dark form, so a dark
// terminal gets paper rails and the lightened terracotta, exactly as the
// client's dark lockup does.
const (
	inkRGB        = "#23262B"
	paperRGB      = "#F8F6F2"
	accentRGB     = "#A8543E"
	darkAccentRGB = "#DB9378"

	// Nearest xterm-256 slots to the four above, by sRGB distance.
	inkSlot, paperSlot         = 235, 255
	accentSlot, darkAccentSlot = 131, 174
)

// The mark's two sequences, resolved once at startup.
var ink, accent = markSequences(os.Getenv("COLORTERM"), os.Getenv("TERM"), os.Getenv("COLORFGBG"))

// Ink paints in the mark's rail colour, or leaves the text alone when this
// terminal has no colour near it.
func Ink(s string) string { return brandPaint(s, ink) }

// Accent paints in the mark's capsule colour.
func Accent(s string) string { return brandPaint(s, accent) }

func brandPaint(s, sequence string) string {
	if NoColor || sequence == "" {
		return s
	}
	return sequence + s + "\033[0m"
}

// markSequences resolves both of the mark's colours from what the terminal
// advertises, so the pair can be pinned as a unit.
//
// The two eight-colour fallbacks differ on purpose. The rail's ink/paper is
// best approximated by the terminal's own foreground, so it is left alone. The
// accent gets yellow: the basic eight have no terracotta, and xterm's yellow
// (#AA5500) is the nearest of them to #A8543E — nearer than the red, which in
// this CLI already means "failed".
func markSequences(colorterm, term, colorfgbg string) (string, string) {
	return brandSequence(inkRGB, paperRGB, inkSlot, paperSlot, "", colorterm, term, colorfgbg),
		brandSequence(accentRGB, darkAccentRGB, accentSlot, darkAccentSlot, "\033[33m", colorterm, term, colorfgbg)
}

// brandSequence picks the SGR parameters for one brand colour from what the
// terminal advertises. basic is what an eight-colour terminal gets, and may be
// empty, meaning "leave this alone".
func brandSequence(light, dark string, lightSlot, darkSlot int, basic, colorterm, term, colorfgbg string) string {
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
