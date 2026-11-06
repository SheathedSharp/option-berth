package display

import (
	"regexp"
	"strings"
)

// Text helpers for the human renderings: a column width that ignores colour
// codes and counts what a terminal actually draws.

var ansiRegex = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// runeCells is how many terminal cells r takes. The East Asian Wide and
// Fullwidth ranges take two; everything else takes one, which is the right rule
// for the labels and names this package pads.
func runeCells(r rune) int {
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
// counting a wide rune as two. Counting runes instead would make a column
// holding both "failed" and a CJK word drift.
func DisplayWidth(s string) int {
	s = ansiRegex.ReplaceAllString(s, "")
	width := 0
	for _, r := range s {
		width += runeCells(r)
	}
	return width
}

// PadDisplay pads to a display-width column, so a string that already carries
// colour codes still lines up.
func PadDisplay(s string, width int) string {
	if drawn := DisplayWidth(s); drawn < width {
		return s + strings.Repeat(" ", width-drawn)
	}
	return s
}
