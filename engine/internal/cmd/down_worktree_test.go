package cmd

import (
	"errors"
	"path/filepath"
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
