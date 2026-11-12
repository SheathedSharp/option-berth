//go:build !windows

package cmd

// Reading a secret from a terminal without echoing it. `golang.org/x/term`
// would do this, but the engine already depends on golang.org/x/sys for every
// other platform call, and this is eight lines of it: turn ECHO off, read the
// line, turn it back on. Adding a dependency for that would be a second way to
// do the same thing.

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

// readSecretFromTerminal prompts and reads one line with echo disabled. It
// refuses when stdin is not a terminal, because reading a key from a pipe while
// telling the person to type would hang forever.
func readSecretFromTerminal() (string, error) {
	fd := int(os.Stdin.Fd())
	// A pipe is not a terminal: prompting would read EOF immediately and a
	// person would see a prompt with no way to answer it.
	if info, statErr := os.Stdin.Stat(); statErr != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", errors.New("standard input is not a terminal; use --key-stdin or --key-env")
	}
	original, err := unix.IoctlGetTermios(fd, ioctlReadTermios)
	if err != nil {
		return "", fmt.Errorf("read terminal state: %w", err)
	}
	quiet := *original
	// ECHONL is cleared too: a trailing newline is fine, but the character the
	// person types must never be drawn on the screen.
	quiet.Lflag &^= unix.ECHO
	quiet.Lflag |= unix.ICANON | unix.ISIG
	if err := unix.IoctlSetTermios(fd, ioctlWriteTermios, &quiet); err != nil {
		return "", fmt.Errorf("disable terminal echo: %w", err)
	}
	// Restore even when the read fails or the process is interrupted.
	defer func() {
		_ = unix.IoctlSetTermios(fd, ioctlWriteTermios, original)
		fmt.Fprintln(os.Stderr)
	}()

	fmt.Fprint(os.Stderr, "API key (input hidden): ")
	var line []byte
	buffer := make([]byte, 1)
	for {
		n, readErr := os.Stdin.Read(buffer)
		if n == 0 || readErr != nil {
			return "", errors.New("no key was entered")
		}
		if buffer[0] == '\n' {
			break
		}
		if buffer[0] == '\r' {
			continue
		}
		line = append(line, buffer[0])
		if len(line) > 4096 {
			return "", errors.New("the key is too long")
		}
	}
	return strings.TrimSpace(string(line)), nil
}
