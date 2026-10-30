package cmd

import (
	"errors"
	"testing"

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
