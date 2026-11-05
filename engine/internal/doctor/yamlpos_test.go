package doctor

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestParsePositionPointsAtTheUnclosedBracket(t *testing.T) {
	cases := []struct {
		label    string
		input    string
		wantLine int
		wantCol  int
	}{
		{
			label: "flow sequence never closed", input: "items: [open",
			wantLine: 1, wantCol: 8,
		},
		{
			// go-yaml blames line 1 here; the bracket is on line 2.
			label: "unclosed bracket below the reported line",
			input: "first: 1\nsecond: [1,\nthird: 2\n", wantLine: 2, wantCol: 9,
		},
		{
			label: "unclosed mapping", input: "a: {x: 1\n", wantLine: 1, wantCol: 4,
		},
		{
			label: "innermost wins", input: "a: [1, {b: 2\n", wantLine: 1, wantCol: 8,
		},
		{
			label: "bracket inside a quoted scalar is not an opener",
			input: "a: \"[\"\nb: [1\n", wantLine: 2, wantCol: 4,
		},
		{
			label: "bracket in a comment is not an opener",
			input: "a: 1 # [\nb: [1\n", wantLine: 2, wantCol: 4,
		},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			var parsed yaml.Node
			err := yaml.Unmarshal([]byte(tc.input), &parsed)
			if err == nil {
				t.Fatalf("%q parsed cleanly; the fixture is not a syntax error", tc.input)
			}
			at := parsePosition(tc.input, err)
			if at.Line != tc.wantLine || at.Col != tc.wantCol {
				t.Errorf("position = %d:%d, want %d:%d (yaml said %v)",
					at.Line, at.Col, tc.wantLine, tc.wantCol, err)
			}
		})
	}
}

func TestParsePositionFallsBackToTheParsersLine(t *testing.T) {
	const input = "a: *nope\n"
	var parsed yaml.Node
	err := yaml.Unmarshal([]byte(input), &parsed)
	if err == nil {
		t.Fatal("an unknown anchor should not parse")
	}
	// go-yaml reports this one with no line at all, so there is nothing to
	// point at and the check names the file instead.
	if at := parsePosition(input, err); at.Line != 0 {
		t.Errorf("position = %d:%d, want no position", at.Line, at.Col)
	}
}

func TestCaretMarksTheColumn(t *testing.T) {
	got := caret("items: [open", Position{Line: 1, Col: 8})
	want := "  items: [open\n         ^"
	if got != want {
		t.Errorf("caret =\n%s\nwant\n%s", got, want)
	}
}

func TestYamlMessageDropsTheRedundantPrefixes(t *testing.T) {
	var parsed yaml.Node
	err := yaml.Unmarshal([]byte("items: [open"), &parsed)
	got := yamlMessage(err)
	if strings.Contains(got, "yaml:") || strings.Contains(got, "line 1") {
		t.Errorf("message = %q, want no yaml:/line prefix", got)
	}
	if !strings.Contains(got, "','") {
		t.Errorf("message = %q, want the parser's own words", got)
	}
}
