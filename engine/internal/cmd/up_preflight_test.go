package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestUpRejectsWaitOptionsBeforeResolvingProject(t *testing.T) {
	for _, tc := range []struct {
		name     string
		wait     bool
		timeout  time.Duration
		explicit bool
	}{
		{"zero", true, 0, true}, {"negative", true, -time.Second, true},
		{"timeout_without_wait", false, time.Second, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldWait, oldTimeout := upWait, upWaitTimeout
			t.Cleanup(func() { upWait, upWaitTimeout = oldWait, oldTimeout })
			home := t.TempDir()
			state := filepath.Join(home, "unused-state")
			t.Setenv("BERTH_HOME", state)
			t.Chdir(home)
			upWait, upWaitTimeout = tc.wait, tc.timeout
			command := &cobra.Command{}
			command.Flags().Duration("wait-timeout", 30*time.Second, "")
			if tc.explicit {
				if err := command.Flags().Set("wait-timeout", tc.timeout.String()); err != nil {
					t.Fatal(err)
				}
			}
			err := upRun(command, nil)
			var usage usageError
			if !errors.As(err, &usage) || !strings.Contains(err.Error(), "--wait") {
				t.Fatalf("expected usage error before project/RPC access, got %v", err)
			}
			if _, err := os.Stat(state); !os.IsNotExist(err) {
				t.Fatalf("invalid options touched runtime state: %v", err)
			}
		})
	}
}
