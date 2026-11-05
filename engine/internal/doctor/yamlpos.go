package doctor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// go-yaml reports a syntax error as "yaml: line N: <what>" and nothing more:
// no column, and N is where the parser gave up rather than where the mistake
// is. `list: [broken` is reported at line 1 whichever line opened the bracket,
// so a user with a long config is told the file is broken and left to find the
// spot.
//
// The doctor finds it instead. A flow collection still open at the end of the
// document is the mistake, and its opening bracket gives both the line and the
// column worth pointing at; anything else falls back to the parser's line.
// Either way the caller gets a position it can print with a caret under it,
// which is what makes `config_parses` actionable.

var linePattern = regexp.MustCompile(`line (\d+):`)

// Position is a 1-based line and column in a source file.
type Position struct {
	// Zero means the position is unknown.
	Line int
	Col  int
}

// parsePosition locates a YAML failure in src; a zero Line means the error
// carried no usable position.
func parsePosition(src string, err error) Position {
	if line, column, found := unclosedFlow(src); found {
		return Position{Line: line, Col: column}
	}
	if err == nil {
		return Position{}
	}
	match := linePattern.FindStringSubmatch(err.Error())
	if match == nil {
		return Position{}
	}
	line, convErr := strconv.Atoi(match[1])
	if convErr != nil {
		return Position{}
	}
	return Position{Line: line, Col: 1}
}

// yamlMessage is a go-yaml error without its "yaml:" prefix and without the
// "line N:" it repeats: the position is printed separately and more precisely.
func yamlMessage(err error) string {
	message := strings.TrimPrefix(err.Error(), "yaml: ")
	return strings.TrimSpace(linePattern.ReplaceAllString(message, ""))
}

// unclosedFlow returns the position of the innermost `[` or `{` never closed,
// skipping comments and quoted scalars.
func unclosedFlow(src string) (int, int, bool) {
	type opener struct {
		line, column int
		symbol       byte
	}
	var open []opener

	line, column := 1, 1
	var singleQuoted, doubleQuoted, commented bool
	for at := 0; at < len(src); at++ {
		ch := src[at]
		switch {
		case ch == '\n':
			line, column = line+1, 0 // column is advanced at the bottom
			commented, singleQuoted, doubleQuoted = false, false, false
		case commented:
		case singleQuoted:
			if ch == '\'' {
				if at+1 < len(src) && src[at+1] == '\'' {
					at, column = at+1, column+1 // an escaped quote, still inside
					break
				}
				singleQuoted = false
			}
		case doubleQuoted:
			if ch == '\\' && at+1 < len(src) {
				at, column = at+1, column+1
				break
			}
			if ch == '"' {
				doubleQuoted = false
			}
		case ch == '#' && (at == 0 || src[at-1] == ' ' || src[at-1] == '\t' || src[at-1] == '\n'):
			commented = true
		case ch == '\'':
			singleQuoted = true
		case ch == '"':
			doubleQuoted = true
		case ch == '[' || ch == '{':
			open = append(open, opener{line: line, column: column, symbol: ch})
		case ch == ']' || ch == '}':
			expected := byte('[')
			if ch == '}' {
				expected = '{'
			}
			if n := len(open); n > 0 && open[n-1].symbol == expected {
				open = open[:n-1]
			}
		}
		column++
	}
	if len(open) == 0 {
		return 0, 0, false
	}
	innermost := open[len(open)-1]
	return innermost.line, innermost.column, true
}

// caret renders the offending line with a marker under the column, so a failing
// config_parses detail reads like a compiler error.
func caret(source string, at Position) string {
	if at.Line <= 0 {
		return ""
	}
	rows := strings.Split(source, "\n")
	if at.Line > len(rows) {
		return ""
	}
	line := strings.TrimRight(rows[at.Line-1], "\r")
	if at.Col <= 0 || at.Col > len(line)+1 {
		return fmt.Sprintf("  %s", line)
	}
	// Tabs are copied through so the marker lands under the right glyph in a
	// terminal that renders them the same way.
	indent := make([]byte, 0, at.Col-1)
	for i := 0; i < at.Col-1; i++ {
		if line[i] == '\t' {
			indent = append(indent, '\t')
			continue
		}
		indent = append(indent, ' ')
	}
	return fmt.Sprintf("  %s\n  %s^", line, indent)
}
