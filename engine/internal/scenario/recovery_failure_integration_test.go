//go:build integration

package scenario

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Exercise the real CLI's registered recovery hook, not only a synthetic
// failing callback. Corruption must remain inspectable and never become a
// successfully running empty daemon. Once fixed, the same endpoint can start.
func TestRecoveryFailurePreventsDaemonReadiness(t *testing.T) {
	e := newEnv(t)
	stateDir := filepath.Join(e.home, ".option-berth")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(stateDir, "runs.json")
	original := []byte(`{"runs":`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := e.command("", "serve")
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(output.String(), "importing runs.json") {
			t.Fatalf("recovery failure not returned by serve: %v, %s", err, output.String())
		}
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-done
		t.Fatalf("failed recovery left daemon running: %s", output.String())
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, original) {
		t.Fatalf("serve discarded recovery evidence: %q, %v", after, err)
	}
	if _, err := os.Stat(e.socket); !os.IsNotExist(err) {
		t.Fatalf("failed startup left socket: %v", err)
	}
	// Explicit repair of the generated fixture only, not silent production
	// recovery. A second process demonstrates lock/socket cleanup succeeded.
	if err := os.WriteFile(path, []byte(`{"runs":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	e.serve()
	if _, err := e.run("", "daemon", "stop", "--json"); err != nil {
		t.Fatalf("corrected fixture did not restart: %v", err)
	}
}
