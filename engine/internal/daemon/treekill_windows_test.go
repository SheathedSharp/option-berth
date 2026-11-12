//go:build integration && windows

package daemon_test

import "os/exec"

// ownProcessGroup has nothing to do on Windows. Spawning a process already
// puts it inside a Job Object, and the Job Object is what carries the kill
// down the tree.
func ownProcessGroup(*exec.Cmd) {}

// killTree kills the command itself; the Job Object disposes of whatever it
// started below it.
func killTree(cmd *exec.Cmd) { _ = cmd.Process.Kill() }
