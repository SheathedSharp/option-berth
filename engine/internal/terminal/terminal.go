// Package terminal drives the two person-facing interactions a setup step
// needs: choosing one row from a short list and typing one line (or a secret).
//
// It is deliberately small. Every command that uses it must also work without
// it — a pipe, an agent and a Windows build never see the interactive path —
// so the callers keep their flag form and this package refuses plainly instead
// of guessing. Prompts go to the writer the caller passes (standard error in
// practice), because stdout belongs to the command's own answer.
package terminal

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/sheathedsharp/option-berth/engine/internal/display"
)

// Choice is one row of a single-choice list. Value is what the caller acts on;
// Label and Detail are what the person reads.
type Choice struct {
	Value  string
	Label  string
	Detail string
}

var (
	// ErrCancelled means the person stopped the question (q, Esc or Ctrl-C).
	// Callers treat it as "nothing was changed", not as a failure to report.
	ErrCancelled = errors.New("terminal: cancelled")
	// ErrNotInteractive means there is no terminal to drive, or this platform
	// does not implement raw mode. Callers fall back to their flags.
	ErrNotInteractive = errors.New("terminal: standard input or output is not an interactive terminal")
)

// hintLine is the one line that teaches the keys, repeated on every redraw.
const hintLine = "↑/↓ move · enter select · q cancel"

// Interactive reports whether a prompt can be drawn: both ends of the
// conversation are a terminal, the terminal is not "dumb", and this platform
// implements raw mode.
func Interactive() bool {
	if !platformSupportsRaw || !stdinIsTerminal() || !stderrIsTerminal() {
		return false
	}
	return !strings.EqualFold(os.Getenv("TERM"), "dumb")
}

// Select asks one question and returns the chosen index. initial is the row
// that starts highlighted; an out-of-range value is clamped. On a list the
// person cancelled, the error is ErrCancelled.
func Select(w io.Writer, question string, choices []Choice, initial int) (int, error) {
	if len(choices) == 0 {
		return 0, errors.New("terminal: a selection needs at least one choice")
	}
	if !Interactive() {
		return 0, ErrNotInteractive
	}
	restore, err := makeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return 0, err
	}
	defer restore()
	return selectFrom(oneByteReader{os.Stdin}, w, question, choices, initial, terminalWidth())
}

// oneByteReader reads exactly one byte at a time. A buffered reader would read
// ahead and swallow keys typed at the next prompt; in raw mode there is nothing
// to amortise, because each keypress is its own read.
type oneByteReader struct{ r io.Reader }

func (b oneByteReader) ReadByte() (byte, error) {
	var one [1]byte
	if _, err := io.ReadFull(b.r, one[:]); err != nil {
		return 0, err
	}
	return one[0], nil
}

// selectFrom is Select with the terminal replaced by a byte reader and a
// writer, so the loop can be driven by a test without a real terminal. width
// is the drawable columns; zero means "unknown" and drops no text, because a
// wrong guess is worse than a wrapped line.
func selectFrom(in io.ByteReader, out io.Writer, question string, choices []Choice, initial, width int) (int, error) {
	if len(choices) == 0 {
		return 0, errors.New("terminal: a selection needs at least one choice")
	}
	cursor := clampCursor(initial, len(choices))
	previous := writeFrame(out, question, choices, cursor, true, 0, width)
	for {
		pressed, err := readKey(in)
		if err != nil {
			return 0, ErrCancelled
		}
		switch pressed {
		case keyCancel:
			return 0, ErrCancelled
		case keyEnter:
			writeFrame(out, question, choices, cursor, false, previous, width)
			// The finished frame is one line shorter than the live one; the
			// blank line it leaves keeps the next prompt on its own line.
			fmt.Fprint(out, "\r\033[K\n")
			return cursor, nil
		case keyUp:
			cursor = (cursor - 1 + len(choices)) % len(choices)
		case keyDown:
			cursor = (cursor + 1) % len(choices)
		default:
			continue
		}
		previous = writeFrame(out, question, choices, cursor, true, previous, width)
	}
}

func clampCursor(cursor, length int) int {
	switch {
	case cursor < 0:
		return 0
	case cursor >= length:
		return length - 1
	default:
		return cursor
	}
}

// writeFrame redraws the question and the rows in place and returns how many
// lines the frame occupies. previous is how many lines the last frame used, so
// the cursor can be returned to the top before the new one is painted.
func writeFrame(w io.Writer, question string, choices []Choice, cursor int, hint bool, previous, width int) int {
	if previous > 0 {
		fmt.Fprintf(w, "\033[%dA", previous)
	}
	lines := frame(question, choices, cursor, hint, width)
	for _, line := range lines {
		// The clear is per line: a shorter label must not leave the tail of a
		// longer one behind.
		fmt.Fprint(w, "\r\033[K", line, "\n")
	}
	if hint {
		return len(lines)
	}
	// Dropping the hint leaves one stale line under the frame.
	fmt.Fprint(w, "\r\033[K")
	return len(lines) + 1
}

// Row layout: two-space margin, the cursor cell and a space, the label padded
// to one column, two spaces, then the detail. Keeping those constants named
// here is what lets the frame clip to the terminal width without a wrapped line
// throwing the redraw off by one.
const (
	rowPrefix = 4 // "  " + cursor cell + " "
	rowGap    = 2 // between the label column and its detail
)

// frame is the picker as plain lines: the question, one row per choice with the
// cursor marked, and — while it is still asking — the key hint. Labels are
// padded to one column so the details read as a list rather than as ragged
// prose. A non-zero width clips each row to that many columns; the terminal
// would otherwise wrap a long detail and leave the cursor bookkeeping wrong.
func frame(question string, choices []Choice, cursor int, hint bool, width int) []string {
	known := width > 0
	budget := width
	if known {
		// Leave the last column empty. Writing the final cell arms the
		// terminal's pending wrap, and the newline after the row would then
		// move two rows instead of one.
		budget = width - 1
	}
	labelColumn := 0
	for _, choice := range choices {
		if n := display.DisplayWidth(choice.Label); n > labelColumn {
			labelColumn = n
		}
	}
	if known {
		if room := budget - rowPrefix - rowGap; labelColumn > room {
			labelColumn = room
		}
		if labelColumn < 1 {
			labelColumn = 1
		}
	}
	lines := make([]string, 0, len(choices)+2)
	lines = append(lines, "? "+clipText(question, budget-2))
	for i, choice := range choices {
		marker := "  "
		label := choice.Label
		detail := choice.Detail
		if known {
			label = clipText(label, labelColumn)
			if detailRoom := budget - rowPrefix - labelColumn - rowGap; detailRoom > 0 {
				detail = clipText(detail, detailRoom)
			} else {
				detail = ""
			}
		}
		if i == cursor {
			marker = display.Accent("❯") + " "
			label = display.Bold(label)
		}
		row := "  " + marker + display.PadDisplay(label, labelColumn)
		if detail != "" {
			row += "  " + display.Dim(detail)
		}
		lines = append(lines, strings.TrimRight(row, " "))
	}
	if hint {
		lines = append(lines, "  "+display.Dim(clipText(hintLine, budget-2)))
	}
	return lines
}

// clipText shortens text to at most width terminal cells, ending in an ellipsis
// when something was cut. A non-positive width means "no clipping": callers
// that do not know the terminal pass zero on purpose, and a row with no room
// left drops its detail rather than calling this.
func clipText(text string, width int) string {
	if width <= 0 || display.DisplayWidth(text) <= width {
		return text
	}
	var b strings.Builder
	used := 0
	for _, r := range text {
		cells := display.DisplayWidth(string(r))
		if used+cells > width-1 {
			break
		}
		b.WriteRune(r)
		used += cells
	}
	return b.String() + "…"
}

// key is one decoded keypress.
type key int

const (
	keyUnknown key = iota
	keyUp
	keyDown
	keyEnter
	keyCancel
)

// readKey decodes one keypress. The arrow sequences are the ones a terminal
// sends; the vim keys are there because a person who lives in a terminal tries
// them. A lone Escape waits for the next byte and is then read as cancel, which
// is the price of not having a read timeout.
func readKey(in io.ByteReader) (key, error) {
	b, err := in.ReadByte()
	if err != nil {
		return keyCancel, err
	}
	switch b {
	case 0x03, 'q', 'Q':
		return keyCancel, nil
	case '\r', '\n':
		return keyEnter, nil
	case 'k':
		return keyUp, nil
	case 'j':
		return keyDown, nil
	case 0x1b:
		second, readErr := in.ReadByte()
		if readErr != nil {
			return keyCancel, nil
		}
		if second != '[' && second != 'O' {
			return keyCancel, nil
		}
		third, readErr := in.ReadByte()
		if readErr != nil {
			return keyCancel, nil
		}
		switch third {
		case 'A':
			return keyUp, nil
		case 'B':
			return keyDown, nil
		default:
			return keyUnknown, nil
		}
	default:
		return keyUnknown, nil
	}
}

// Line asks for one line of text. An empty answer returns initial, so a caller
// can offer the current value in brackets and let Enter keep it.
func Line(w io.Writer, question, initial string) (string, error) {
	if !Interactive() {
		return "", ErrNotInteractive
	}
	suffix := ""
	if initial != "" {
		suffix = " " + display.Dim("["+initial+"]")
	}
	fmt.Fprintf(w, "? %s%s: ", question, suffix)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return "", ErrCancelled
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return initial, nil
	}
	return line, nil
}

// ReadSecret reads one line with echo disabled. An empty line is returned as
// an empty string: keeping a stored secret is a decision the caller makes, not
// something this package can guess.
func ReadSecret(w io.Writer, label string) (string, error) {
	if !platformSupportsRaw || !stdinIsTerminal() {
		return "", ErrNotInteractive
	}
	fd := int(os.Stdin.Fd())
	restore, err := echoOff(fd)
	if err != nil {
		return "", err
	}
	defer func() {
		restore()
		fmt.Fprintln(w)
	}()

	fmt.Fprintf(w, "? %s: ", label)
	var line []byte
	buffer := make([]byte, 1)
	for {
		n, readErr := os.Stdin.Read(buffer)
		if n == 0 || readErr != nil {
			return "", ErrCancelled
		}
		switch buffer[0] {
		case 0x03:
			// Ctrl-C reaches us as a byte because the prompt turned ISIG off;
			// returning here still runs the deferred echo restore.
			return "", ErrCancelled
		case '\n', '\r':
			return strings.TrimSpace(string(line)), nil
		case 0x7f, 0x08:
			// Backspace and Ctrl-H: nothing was drawn, so only the buffer
			// changes and the screen has nothing to correct.
			if len(line) > 0 {
				line = line[:len(line)-1]
			}
		case 0x15:
			line = line[:0]
		default:
			line = append(line, buffer[0])
			if len(line) > 4096 {
				return "", errors.New("the value is too long")
			}
		}
	}
}
