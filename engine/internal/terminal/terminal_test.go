package terminal

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

func noColor(t *testing.T) {
	t.Helper()
	restore := display.NoColor
	display.NoColor = true
	t.Cleanup(func() { display.NoColor = restore })
}

// The frame is the whole visible surface of the picker. It is pinned with
// colour off: the words and the marker carry the meaning, colour only repeats
// it.
func TestFrameShowsTheChoicesAndTheSelectedOne(t *testing.T) {
	noColor(t)
	choices := []Choice{
		{Value: "openrouter", Label: "OpenRouter", Detail: "one key, many decision models"},
		{Value: "typesafe", Label: "TypeSafe", Detail: "Jev directly from TypeSafe"},
		{Value: "", Label: "Custom endpoint", Detail: "any decisions endpoint"},
	}
	lines := frame("Provider", choices, 1, true, 0)
	want := []string{
		"? Provider",
		"    OpenRouter       one key, many decision models",
		"  ❯ TypeSafe         Jev directly from TypeSafe",
		"    Custom endpoint  any decisions endpoint",
		"  " + hintLine,
	}
	if len(lines) != len(want) {
		t.Fatalf("frame has %d lines, want %d:\n%s", len(lines), len(want), strings.Join(lines, "\n"))
	}
	for i := range want {
		if lines[i] != want[i] {
			t.Errorf("line %d = %q, want %q", i, lines[i], want[i])
		}
	}
}

// A frame without the hint is what stays on screen after the answer: the same
// question and rows, nothing that still asks for a key.
func TestFrameCanDropTheHint(t *testing.T) {
	noColor(t)
	choices := []Choice{{Value: "a", Label: "A"}}
	lines := frame("Question", choices, 0, false, 0)
	if len(lines) != 2 {
		t.Fatalf("frame = %q, want the question and one row", lines)
	}
	if lines[0] != "? Question" || lines[1] != "  ❯ A" {
		t.Fatalf("frame = %q", lines)
	}
}

func TestReadKeyDecodesArrowsAndVimKeys(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  key
	}{
		{"arrow up", "\x1b[A", keyUp},
		{"arrow down", "\x1b[B", keyDown},
		{"application arrow up", "\x1bOA", keyUp},
		{"vim up", "k", keyUp},
		{"vim down", "j", keyDown},
		{"enter", "\r", keyEnter},
		{"newline", "\n", keyEnter},
		{"q", "q", keyCancel},
		{"ctrl-c", "\x03", keyCancel},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readKey(bytes.NewReader([]byte(tc.input)))
			if err != nil {
				t.Fatalf("readKey: %v", err)
			}
			if got != tc.want {
				t.Fatalf("readKey(%q) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestSelectMovesWithTheArrowKeysAndAnswersOnEnter(t *testing.T) {
	noColor(t)
	choices := []Choice{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}, {Value: "c", Label: "C"}}
	// down, down, up (k), enter: 0 -> 1 -> 2 -> 1.
	var out bytes.Buffer
	index, err := selectFrom(bytes.NewReader([]byte("\x1b[B\x1b[Bk\r")), &out, "Pick", choices, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("index = %d, want 1", index)
	}
	if !strings.Contains(out.String(), "❯ B") {
		t.Fatalf("the final frame does not show the answer:\n%s", out.String())
	}
}

// The list wraps at both ends: pressing up on the first row lands on the last,
// which is what a short list is expected to do.
func TestSelectWrapsAround(t *testing.T) {
	noColor(t)
	choices := []Choice{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}
	index, err := selectFrom(strings.NewReader("\x1b[A\r"), io.Discard, "Pick", choices, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("index = %d, want the last row", index)
	}
}

func TestSelectCancelsOnQ(t *testing.T) {
	noColor(t)
	choices := []Choice{{Value: "a", Label: "A"}}
	_, err := selectFrom(strings.NewReader("q"), io.Discard, "Pick", choices, 0, 0)
	if err != ErrCancelled {
		t.Fatalf("err = %v, want ErrCancelled", err)
	}
}

func TestSelectClampsTheInitialRow(t *testing.T) {
	noColor(t)
	choices := []Choice{{Value: "a", Label: "A"}, {Value: "b", Label: "B"}}
	index, err := selectFrom(strings.NewReader("\r"), io.Discard, "Pick", choices, 99, 0)
	if err != nil {
		t.Fatal(err)
	}
	if index != 1 {
		t.Fatalf("index = %d, want the last row", index)
	}
}

func TestSelectRefusesAnEmptyList(t *testing.T) {
	if _, err := Select(io.Discard, "Pick", nil, 0); err == nil {
		t.Fatal("an empty list was accepted")
	}
}

// A narrow terminal must not wrap a row: a wrapped line makes the redraw move
// up the wrong number of rows. Every line is clipped to the width and marks
// what it cut.
func TestFrameClipsRowsToTheTerminalWidth(t *testing.T) {
	noColor(t)
	choices := []Choice{{
		Value:  "a",
		Label:  "A label long enough that it cannot fit in a narrow frame",
		Detail: "a detail that also has to be cut",
	}}
	lines := frame("A question that is itself too long for the frame", choices, 0, true, 40)
	for _, line := range lines {
		if got := display.DisplayWidth(line); got > 40 {
			t.Errorf("line %q is %d cells, want at most 40", line, got)
		}
	}
	if !strings.Contains(strings.Join(lines, "\n"), "…") {
		t.Fatalf("clipped frame carries no ellipsis:\n%s", strings.Join(lines, "\n"))
	}
}

// Every prompt has to refuse rather than guess when there is no terminal: a
// command that reaches this path is expected to fall back to its flags, and a
// prompt that blocks a pipe would hang it forever.
func TestPromptsRefuseWithoutATerminal(t *testing.T) {
	if Interactive() {
		t.Skip("this test run has a terminal; the refusal path is unreachable")
	}
	if _, err := Select(io.Discard, "Pick", []Choice{{Value: "a", Label: "A"}}, 0); err != ErrNotInteractive {
		t.Fatalf("Select error = %v, want ErrNotInteractive", err)
	}
	if _, err := Line(io.Discard, "Endpoint", ""); err != ErrNotInteractive {
		t.Fatalf("Line error = %v, want ErrNotInteractive", err)
	}
	if _, err := ReadSecret(io.Discard, "API key"); err != ErrNotInteractive {
		t.Fatalf("ReadSecret error = %v, want ErrNotInteractive", err)
	}
}
