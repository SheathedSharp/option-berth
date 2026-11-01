//go:build windows

package cmd

import "errors"

// Hidden terminal input on Windows is not implemented: the engine's Windows
// surface is a native build, a CLI smoke test and a cross-build, not full
// service acceptance. Refusing with the two supported paths beats a prompt that
// silently echoes the key.
func readSecretFromTerminal() (string, error) {
	return "", errors.New("interactive key entry is not supported on Windows; use --key-stdin or --key-env")
}
