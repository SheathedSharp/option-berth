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
