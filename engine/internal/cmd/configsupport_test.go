package cmd

import (
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/groups"
)

// A daemon that predates ready_timeout would ignore the declared budget and
// keep its fixed 30s dependency wait. Starting must say so instead of letting
// the file's promise silently not apply; stopping what already runs does not
// need the budget and stays allowed.
func TestConfigSupportRequiresReadyTimeoutCapability(t *testing.T) {
	declared := &groups.Config{
		Path:     "/tmp/oberth.yaml",
		Services: []groups.Service{{Name: "api", Cmd: "run api", ReadyTimeout: "90s"}},
	}
	plain := &groups.Config{
		Path:     "/tmp/oberth.yaml",
		Services: []groups.Service{{Name: "api", Cmd: "run api"}},
	}
	// Cmd without a ${...} reference, so only the ready_timeout gate is at
	// stake: UsesAssignedPorts stays false for this manifest.
	old := rpc.DaemonHelloResult{
		DaemonVersion: "v1.3.0",
		Capabilities:  []string{rpc.CapabilityAutoPorts, rpc.CapabilityManifestExtras},
	}
	if err := configSupportProblem(declared, old, true); err == nil {
		t.Fatal("a declared ready_timeout was handed to a daemon that ignores it")
	} else if !strings.Contains(err.Error(), "ready_timeout") || !strings.Contains(err.Error(), "daemon restart") {
		t.Fatalf("error = %v, want it to name ready_timeout and the daemon restart", err)
	}
	if err := configSupportProblem(declared, old, false); err != nil {
		t.Fatalf("stopping services does not need ready_timeout support: %v", err)
	}
	if err := configSupportProblem(plain, old, true); err != nil {
		t.Fatalf("a file relying on the 30s default is compatible with an old daemon: %v", err)
	}

	updated := old
	updated.DaemonVersion = "v1.4.0"
	updated.Capabilities = append(append([]string{}, old.Capabilities...), rpc.CapabilityReadyTimeout)
	if err := configSupportProblem(declared, updated, true); err != nil {
		t.Fatalf("a daemon announcing the capability was rejected: %v", err)
	}
}
