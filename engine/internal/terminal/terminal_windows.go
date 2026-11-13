//go:build windows

package terminal

// The engine's Windows surface is a native build, a CLI smoke test and a
// cross-build, not full interactive acceptance. A prompt that cannot restore
// the console is worse than no prompt, so this platform refuses and every
// caller falls back to its flags.

const platformSupportsRaw = false

func stdinIsTerminal() bool  { return false }
func stderrIsTerminal() bool { return false }
func terminalWidth() int     { return 0 }

func makeRaw(int) (func(), error) { return nil, ErrNotInteractive }
func echoOff(int) (func(), error) { return nil, ErrNotInteractive }
