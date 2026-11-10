package cmd

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/spf13/cobra"
)

func TestDownRefusesCombinedWorktreeSelectors(t *testing.T) {
	prevWorktree, prevIdentity := downWorktreeFlag, downIdentityFlag
	t.Cleanup(func() { downWorktreeFlag, downIdentityFlag = prevWorktree, prevIdentity })

	downWorktreeFlag, downIdentityFlag = "/repo/wt", "wt_0123456789ab"
	err := downRun(&cobra.Command{}, nil)
	var usage usageError
	if !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}

	downWorktreeFlag, downIdentityFlag = "/repo/wt", ""
	err = downRun(&cobra.Command{}, []string{"demo"})
	if !errors.As(err, &usage) {
		t.Fatalf("project + --worktree err = %v, want a usage error", err)
	}
}

// TestDownParamsChoosesOneTarget pins target selection without a daemon: the
// four forms produce exactly one selector each, and a relative worktree path
// is resolved before it crosses the socket.
func TestDownParamsChoosesOneTarget(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		worktree string
		identity string
		check    func(t *testing.T, params rpc.GroupsKillParams)
	}{
		{
			name: "a named project",
			args: []string{"demo"},
			check: func(t *testing.T, params rpc.GroupsKillParams) {
				if params.Name != "demo" || params.ConfigPath != nil {
					t.Errorf("params = %+v, want the named project only", params)
				}
			},
		},
		{
			name:     "a worktree path",
			worktree: "relative/checkout",
			check: func(t *testing.T, params rpc.GroupsKillParams) {
				if params.WorktreePath == nil || !filepath.IsAbs(*params.WorktreePath) {
					t.Errorf("WorktreePath = %v, want an absolute path", params.WorktreePath)
				}
				if params.Identity != nil || params.Name != "" {
					t.Errorf("params = %+v, want the worktree selector only", params)
				}
			},
		},
		{
			name:     "an identity",
			identity: "wt_0123456789ab",
			check: func(t *testing.T, params rpc.GroupsKillParams) {
				if params.Identity == nil || *params.Identity != "wt_0123456789ab" {
					t.Errorf("params = %+v, want the identity", params)
				}
				if params.WorktreePath != nil || params.Name != "" {
					t.Errorf("params = %+v, want the identity selector only", params)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			prevWorktree, prevIdentity := downWorktreeFlag, downIdentityFlag
			t.Cleanup(func() { downWorktreeFlag, downIdentityFlag = prevWorktree, prevIdentity })
			downWorktreeFlag, downIdentityFlag = tc.worktree, tc.identity

			params, local, err := downParams(tc.args)
			if err != nil {
				t.Fatalf("downParams: %v", err)
			}
			if local != nil {
				t.Errorf("local config = %v, want none for an explicit target", local)
			}
			if params.Force != downForceFlag || !params.Release {
				t.Errorf("params = %+v, want the flags and release carried through", params)
			}
			if params.WorktreePath == nil && params.Identity == nil && params.Name == "" && params.ConfigPath == nil {
				t.Fatalf("no selector was chosen: %+v", params)
			}
			tc.check(t, params)
		})
	}
}

// withDownFlags installs one flag combination for the duration of a test.
func withDownFlags(t *testing.T, set func()) {
	t.Helper()
	oldForce, oldJSON := downForceFlag, downJSONFlag
	oldWorktree, oldIdentity := downWorktreeFlag, downIdentityFlag
	t.Cleanup(func() {
		downForceFlag, downJSONFlag = oldForce, oldJSON
		downWorktreeFlag, downIdentityFlag = oldWorktree, oldIdentity
	})
	downForceFlag, downJSONFlag, downWorktreeFlag, downIdentityFlag = false, false, "", ""
	set()
}

// TestDownParamsFallsBackToTheNearestManifest: with no selector at all the
// project is the manifest at or above the working directory, and that file is
// what the daemon is asked to stop.
func TestDownParamsFallsBackToTheNearestManifest(t *testing.T) {
	dir := demoProject(t)
	withDownFlags(t, func() {})

	params, cfg, err := downParams(nil)
	if err != nil {
		t.Fatalf("downParams: %v", err)
	}
	if cfg == nil || cfg.Path != filepath.Join(dir, "oberth.yaml") {
		t.Fatalf("config = %v, want the manifest at the working directory", cfg)
	}
	if params.ConfigPath == nil || *params.ConfigPath != cfg.Path {
		t.Fatalf("params = %+v, want the manifest path as the selector", params)
	}
	if !params.Release {
		t.Error("down must release the claimed ports")
	}
}

// TestDownParamsWithoutAManifestFails: outside any project there is nothing to
// name, and the error says so instead of stopping an empty group.
func TestDownParamsWithoutAManifestFails(t *testing.T) {
	t.Chdir(t.TempDir())
	withDownFlags(t, func() {})

	params, cfg, err := downParams(nil)
	if err == nil {
		t.Fatalf("downParams = %+v, %v; want the missing-manifest error", params, cfg)
	}
	if cfg != nil {
		t.Errorf("config = %v, want none", cfg)
	}
}

// TestDownStopsTheNearestProjectThroughTheDaemon drives the whole command
// against a real daemon: the manifest is chosen here, the stop happens there,
// and the answer is the documented "nothing was running" sentence.
func TestDownStopsTheNearestProjectThroughTheDaemon(t *testing.T) {
	demoProject(t)
	resetRouting(t)
	startDaemonWhereTheCommandLooks(t)
	withDownFlags(t, func() {})

	var err error
	out := captureStdout(t, func() { err = downRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("down: %v", err)
	}
	if !strings.Contains(out, "Nothing was running.") {
		t.Fatalf("stdout = %q, want the empty-stop sentence", out)
	}
}

// TestDownJSONAnswersWithTheEnvelope: --json prints the daemon's own answer, so
// a caller reads the same results and released count the text path renders.
func TestDownJSONAnswersWithTheEnvelope(t *testing.T) {
	demoProject(t)
	resetRouting(t)
	startDaemonWhereTheCommandLooks(t)
	withDownFlags(t, func() { downJSONFlag = true })

	var err error
	out := captureStdout(t, func() { err = downRun(testCommand(), nil) })
	if err != nil {
		t.Fatalf("down --json: %v", err)
	}
	var env rpc.KillEnvelope
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("stdout is not one JSON document: %v\n%s", err, out)
	}
	if len(env.Results) != 0 {
		t.Fatalf("results = %+v, want none for a project with nothing running", env.Results)
	}
}

// TestDownReportsAnUnknownProject: a name the daemon cannot resolve is its
// error, carried through with the wire's code.
func TestDownReportsAnUnknownProject(t *testing.T) {
	resetRouting(t)
	startDaemonWhereTheCommandLooks(t)
	withDownFlags(t, func() {})

	err := downRun(testCommand(), []string{"no-such-project"})
	if err == nil {
		t.Fatal("down of an unknown project returned nil")
	}
	if got := exitCodeFor(err); got != exitFail {
		t.Fatalf("exit code = %d, want %d", got, exitFail)
	}
}

// TestDownWithoutADaemonFails: stopping is a daemon operation; with none
// reachable the command reports the connection failure instead of pretending
// the project is down.
func TestDownWithoutADaemonFails(t *testing.T) {
	demoProject(t)
	resetRouting(t)
	noDaemonReachable(t)
	withDownFlags(t, func() {})

	if err := downRun(testCommand(), nil); err == nil {
		t.Fatal("down without a daemon returned nil")
	}
}
