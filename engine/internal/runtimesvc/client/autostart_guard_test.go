package client

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
)

// Autostart is the one place that launches a process the caller did not name,
// so both of its refusals are pinned here.

func TestAutostartRespectsTheEnvironmentSwitch(t *testing.T) {
	t.Setenv(NoAutostartEnv, "1")
	err := Autostart(context.Background(), filepath.Join(t.TempDir(), "option-berth"), "")
	if err == nil {
		t.Fatal("Autostart ran while disabled")
	}
	if !strings.Contains(err.Error(), NoAutostartEnv) {
		t.Errorf("error = %v, want it to name %s", err, NoAutostartEnv)
	}
	if !wrapsNotRunning(err) {
		t.Errorf("error = %v, want it to wrap ErrNotRunning", err)
	}
}

// Even with autostart allowed, a Go test binary is never spawned as the
// daemon: `serve --detach` would re-run the suite in the background.
func TestAutostartRefusesATestBinary(t *testing.T) {
	t.Setenv(NoAutostartEnv, "")
	// It exits non-zero, so the opt-in case fails at the spawn instead of
	// waiting out the socket timeout.
	testBinary := filepath.Join(t.TempDir(), "guardfake.test")
	if err := os.WriteFile(testBinary, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	err := Autostart(context.Background(), testBinary, "")
	if err == nil {
		t.Fatal("Autostart spawned a Go test binary as the daemon")
	}
	if !strings.Contains(err.Error(), "test binary") ||
		!strings.Contains(err.Error(), runtimesvc.AllowTestDaemonEnv) {
		t.Errorf("error = %v, want the refusal and the opt-in named", err)
	}

	t.Setenv(runtimesvc.AllowTestDaemonEnv, "1")
	err = Autostart(context.Background(), testBinary, filepath.Join(t.TempDir(), "d.sock"))
	if err != nil && strings.Contains(err.Error(), "test binary") {
		t.Errorf("the opt-in did not lift the guard: %v", err)
	}
}

func wrapsNotRunning(err error) bool {
	return strings.Contains(err.Error(), ErrNotRunning.Error())
}
