package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
)

// `--session current` is shorthand: it resolves from this shell's own
// environment, and says so when there is nothing there to resolve.
func TestCurrentSessionResolvesFromTheEnvironment(t *testing.T) {
	t.Setenv(sessions.EnvSession, "claude-code:run42")
	resolved, err := currentSession("current")
	if err != nil {
		t.Fatalf("currentSession: %v", err)
	}
	if resolved != "run42" {
		t.Errorf("currentSession(current) = %q, want run42", resolved)
	}

	// Anything else is passed through untouched.
	if literal, err := currentSession("some-id"); err != nil || literal != "some-id" {
		t.Errorf("currentSession(some-id) = %q, %v", literal, err)
	}
	if blank, err := currentSession(""); err != nil || blank != "" {
		t.Errorf("currentSession(\"\") = %q, %v", blank, err)
	}
}

func TestCurrentSessionWithoutAnAgentIsAnError(t *testing.T) {
	for _, key := range []string{
		sessions.EnvClaudeCode, sessions.EnvClaudeCodeSessionID,
		sessions.EnvClaudeSessionID, sessions.EnvSession, sessions.EnvSessionID,
		sessions.EnvCodexSandbox, sessions.EnvCodexThreadID,
		sessions.EnvCursorAgent, sessions.EnvCursorAgentSessionID,
	} {
		t.Setenv(key, "")
	}
	if _, err := currentSession("current"); err == nil {
		t.Error("currentSession(current) outside an agent did not fail")
	}
}

func TestSessionsCommandWithoutADaemonSaysSo(t *testing.T) {
	saved := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) { return nil, client.ErrNotRunning }
	t.Cleanup(func() { dialDaemon = saved })

	_, runErr := sessionsDaemon(context.Background())
	if runErr == nil {
		t.Fatal("sessionsDaemon without a daemon did not fail")
	}
	if !strings.Contains(runErr.Error(), "oberth serve") {
		t.Errorf("error = %q, want it to name the command that starts a daemon", runErr)
	}
}
