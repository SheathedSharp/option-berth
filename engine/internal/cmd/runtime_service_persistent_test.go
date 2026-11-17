package cmd

import (
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/config"
)

// A unit that a supervisor keeps alive must never idle out, and the flag has to win
// over the file: otherwise `runtime_service.idle_timeout` in config.yaml would quietly
// turn the persistent service back into one that disappears when nobody is looking.
func TestPersistentOverridesTheConfiguredIdleTimeout(t *testing.T) {
	configured := config.RuntimeServiceConfig{IdleTimeout: "30m"}
	for _, testCase := range []struct {
		name       string
		persistent bool
		want       time.Duration
	}{
		{"config decides without the flag", false, 30 * time.Minute},
		{"the unit's flag disables idle shutdown", true, 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if got := runtimeServiceIdleTimeout(configured, testCase.persistent); got != testCase.want {
				t.Fatalf("runtimeServiceIdleTimeout(%q, %v) = %s, want %s",
					configured.IdleTimeout, testCase.persistent, got, testCase.want)
			}
		})
	}
}

// With nothing configured, `--persistent` is still the stronger statement: the
// default 30 minutes must not survive it.
func TestPersistentOverridesTheDefaultIdleTimeout(t *testing.T) {
	if got := runtimeServiceIdleTimeout(config.RuntimeServiceConfig{}, false); got != config.DefaultIdleTimeout {
		t.Fatalf("default idle timeout = %s, want %s", got, config.DefaultIdleTimeout)
	}
	if got := runtimeServiceIdleTimeout(config.RuntimeServiceConfig{}, true); got != 0 {
		t.Fatalf("persistent idle timeout = %s, want 0", got)
	}
}

// The flag is what the service unit writes into its command line, so it has to exist
// on `run` and stay off unless it is passed.
func TestPersistentIsAFlagOfRun(t *testing.T) {
	flag := runtimeServiceRunCmd.Flags().Lookup("persistent")
	if flag == nil {
		t.Fatal("`runtime service run` has no --persistent flag")
	}
	if flag.DefValue != "false" {
		t.Fatalf("--persistent default = %q, want false", flag.DefValue)
	}
}
