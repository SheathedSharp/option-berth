//go:build !darwin && !linux && !windows

package terminal

// Platforms the release does not build for still have to compile: the picker
// is simply unavailable and every caller falls back to its flags.

const platformSupportsRaw = false

func stdinIsTerminal() bool  { return false }
func stderrIsTerminal() bool { return false }
func terminalWidth() int     { return 0 }

func makeRaw(int) (func(), error) { return nil, ErrNotInteractive }
func echoOff(int) (func(), error) { return nil, ErrNotInteractive }
