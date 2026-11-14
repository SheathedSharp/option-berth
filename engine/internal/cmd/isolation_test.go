package cmd

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
	"github.com/sheathedsharp/option-berth/engine/internal/testenv"
	"github.com/spf13/cobra"
)

// This is the package that leaked. Its tests drive the real commands, so every
// path the CLI resolves must land in the test's own temp directory, and the
// check goes through the production resolvers rather than a restatement.
func TestEverythingTheCLIResolvesIsIsolated(t *testing.T) {
	resolved := []string{
		runtimesvc.SocketPath(),
		runtimesvc.LockPath(),
		runtimesvc.LogPath(),
		runtimesvc.ConfigDir(),
		config.Path(),
		store.Path(),
	}
	testenv.RequireIsolated(t, resolved...)

	// The live install is what these tests spent an afternoon writing to, so it
	// is named outright instead of being left implied by the temp check.
	if realHome := testenv.RealHome(); realHome != "" {
		live := filepath.Join(realHome, ".option-berth")
		if under(config.Path(), live) {
			t.Fatalf("config.Path() = %s: the tests are pointed at the live install", config.Path())
		}
		if under(store.Path(), live) {
			t.Fatalf("store.Path() = %s: the tests would write to the live database", store.Path())
		}
	}
}

// under reports whether path is dir itself or lives below it.
func under(path, dir string) bool {
	rest, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	return !strings.HasPrefix(rest, "..")
}

// The autostart path is what forked `cmd.test runtime service run --detach`. Two independent
// guards stop it now, and both are checked here: either one alone would have
// been enough to prevent the 2,150 leaked processes.
func TestAutostartIsRefusedInTests(t *testing.T) {
	// Guard one: the process-wide ban TestMain installs.
	firstErr := client.Autostart(context.Background(), "", runtimesvc.SocketPath())
	if firstErr == nil {
		t.Fatal("Autostart succeeded in a test binary")
	}
	if !strings.Contains(firstErr.Error(), client.NoAutostartEnv) {
		t.Errorf("error = %v, want it to name %s", firstErr, client.NoAutostartEnv)
	}

	// Guard two: even with the ban lifted, the executable is a test binary.
	testenv.AllowAutostart(t)
	secondErr := client.Autostart(context.Background(), "", runtimesvc.SocketPath())
	if secondErr == nil {
		t.Fatal("Autostart succeeded with a Go test binary as the runtime service")
	}
	if !strings.Contains(secondErr.Error(), "test binary") {
		t.Errorf("error = %v, want it to say the binary is a test binary", secondErr)
	}
}

// The production seam on the other side: `oberth runtime service run` inside a test binary
// refuses instead of re-running the suite in the background.
func TestServeRefusesToRunAsATestBinary(t *testing.T) {
	command := &cobra.Command{}
	command.SetContext(context.Background())

	runErr := runtimeServiceRun(command, nil)
	if runErr == nil {
		t.Fatal("serve started inside a test binary")
	}
	if !strings.Contains(runErr.Error(), runtimesvc.AllowTestRuntimeServiceEnv) {
		t.Errorf("error = %v, want it to name the opt-out %s", runErr, runtimesvc.AllowTestRuntimeServiceEnv)
	}

	t.Setenv(runtimesvc.AllowTestRuntimeServiceEnv, "1")
	if err := refuseTestBinaryRuntimeService(); err != nil {
		t.Errorf("the explicit opt-in did not lift the guard: %v", err)
	}
}

// Nothing here starts a runtime service of its own, so nothing may be left running.
// TestMain checks that for the whole binary; this lands the failure on a named
// test when the fault is this package's.
func TestNoRuntimeServiceSurvivesThisPackage(t *testing.T) {
	testenv.RequireNoLeakedRuntimeServices(t)
}
