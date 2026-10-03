package display

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/ports"
)

// Text helpers for the human renderings: widths that ignore colour codes, and
// paths under the home directory written as ~/….

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func visibleLen(s string) int {
	return len(ansiRegex.ReplaceAllString(s, ""))
}

func padRight(s string, width int) string {
	vl := visibleLen(s)
	if vl >= width {
		return s
	}
	return s + strings.Repeat(" ", width-vl)
}

// wideRunes are the East Asian Wide and Fullwidth ranges, as a terminal counts
// them: one rune, two cells. Anything else is one cell — a good enough rule for
// the labels and names this package pads.
func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F, // Hangul Jamo
		r >= 0x2E80 && r <= 0x303E, // CJK Radicals .. CJK punctuation
		r >= 0x3041 && r <= 0x33FF, // Hiragana .. CJK Compatibility
		r >= 0x3400 && r <= 0x4DBF, // CJK Extension A
		r >= 0x4E00 && r <= 0x9FFF, // CJK Unified
		r >= 0xA000 && r <= 0xA4CF, // Yi
		r >= 0xAC00 && r <= 0xD7A3, // Hangul syllables
		r >= 0xF900 && r <= 0xFAFF, // CJK Compatibility Ideographs
		r >= 0xFE30 && r <= 0xFE4F, // CJK Compatibility Forms
		r >= 0xFF00 && r <= 0xFF60, // Fullwidth Forms
		r >= 0xFFE0 && r <= 0xFFE6:
		return 2
	}
	return 1
}

// DisplayWidth is how many terminal cells s occupies, ignoring ANSI codes and
// counting wide runes as two. Plain visibleLen counts runes, which is why a
// column holding both "failed" and a CJK word drifts.
func DisplayWidth(s string) int {
	s = ansiRegex.ReplaceAllString(s, "")
	w := 0
	for _, r := range s {
		w += runeWidth(r)
	}
	return w
}

// PadDisplay pads to a display-width column. Padded strings may already carry
// ANSI codes.
func PadDisplay(s string, width int) string {
	if vw := DisplayWidth(s); vw < width {
		return s + strings.Repeat(" ", width-vw)
	}
	return s
}

func colorStatus(status string) string {
	switch status {
	case "running":
		return Green(status)
	case "partial":
		return Yellow(status)
	case "stopped":
		return Dim(status)
	default:
		return status
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

func shortenHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" || path == home {
		return path
	}
	if rel, err := filepath.Rel(home, path); err == nil &&
		rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "~" + string(filepath.Separator) + rel
	}
	return path
}

func colorName(p ports.ListeningPort, name string) string {
	switch p.Type {
	case ports.PortTypeDocker:
		return Magenta(name)
	default:
		return Green(name)
	}
}
