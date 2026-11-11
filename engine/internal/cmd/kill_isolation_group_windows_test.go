//go:build windows

package cmd

import "fmt"

// killHeldProcessGroupDefault has no Windows equivalent: the owned-run fixtures
// that create a process group are unix-only, so a group identity must fail
// closed rather than fall back to a bare pid.
func killHeldProcessGroupDefault(id isolatedProcessIdentity) error {
	return fmt.Errorf("%w: process-group cleanup is not supported on windows", errUnscopedKillCleanup)
}
