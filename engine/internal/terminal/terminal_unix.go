//go:build darwin || linux

package terminal

// Driving the terminal without a second dependency. `golang.org/x/term` would
// do this, but the engine already depends on golang.org/x/sys for every other
// platform call, and this is a handful of lines of the same: two flags off for
// raw mode, one flag off for a hidden prompt. Adding a dependency for that
// would be a second way to do the same thing.

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

const platformSupportsRaw = true

// stdinIsTerminal and stderrIsTerminal answer the two ends of the conversation.
// A prompt drawn into a pipe is a prompt nobody can answer.
//
// The test is a termios read, not a character-device bit: /dev/null is a
// character device too, and a prompt written there would wait forever for a
// person who is not there.
func stdinIsTerminal() bool  { return isTerminal(os.Stdin) }
func stderrIsTerminal() bool { return isTerminal(os.Stderr) }

func isTerminal(file *os.File) bool {
	_, err := unix.IoctlGetTermios(int(file.Fd()), ioctlReadTermios)
	return err == nil
}

// terminalWidth is the drawable column count, or zero when the terminal will
// not say. A zero width means the picker keeps its rows whole rather than
// guessing and cutting a word.
func terminalWidth() int {
	size, err := unix.IoctlGetWinsize(int(os.Stderr.Fd()), unix.TIOCGWINSZ)
	if err != nil || size.Col == 0 {
		return 0
	}
	return int(size.Col)
}

// makeRaw turns the terminal into a one-key-at-a-time reader. Canonical mode
// and echo are off so a key reaches the picker immediately; ISIG is off too, so
// Ctrl-C arrives as a byte the picker can turn into a clean cancel instead of a
// signal that leaves the terminal raw.
func makeRaw(fd int) (func(), error) {
	original, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, fmt.Errorf("read terminal state: %w", err)
	}
	raw := *original
	raw.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG
	raw.Iflag &^= unix.ICRNL
	raw.Cc[unix.VMIN] = 1
	raw.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &raw); err != nil {
		return nil, fmt.Errorf("enter raw mode: %w", err)
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, original) }, nil
}

// echoOff makes one keypress at a time reach the caller with nothing drawn.
// Canonical mode is off as well as echo: in canonical mode Ctrl-C would sit in
// the line buffer until Enter, so a person who changes their mind at the key
// prompt would have no way out. ISIG is off for the same reason — a signal
// would kill the process before the deferred restore put the terminal back.
func echoOff(fd int) (func(), error) {
	original, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return nil, fmt.Errorf("read terminal state: %w", err)
	}
	quiet := *original
	quiet.Lflag &^= unix.ECHO | unix.ICANON | unix.ISIG
	quiet.Iflag &^= unix.ICRNL
	quiet.Cc[unix.VMIN] = 1
	quiet.Cc[unix.VTIME] = 0
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &quiet); err != nil {
		return nil, fmt.Errorf("disable terminal echo: %w", err)
	}
	return func() { _ = unix.IoctlSetTermios(fd, ioctlWriteTermios, original) }, nil
}
