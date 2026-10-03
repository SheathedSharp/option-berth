package draft

import "strings"

// firstLine returns the first non-empty line in another program's output.
// It keeps a useful sentence when a structured run fails.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
