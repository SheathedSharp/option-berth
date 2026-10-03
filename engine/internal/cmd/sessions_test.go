package cmd

import (
	"context"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/sessions"
)

// `--session current` is spec 2 §3's shorthand: it resolves from this shell's
// own environment, and says so when there is nothing to resolve.
func TestCurrentSessionResolvesFromTheEnvironment(t *testing.T) {
	t.Setenv(sessions.EnvSession, "claude-code:abc123")
	got, err := currentSession("current")
	if err != nil {
		t.Fatalf("currentSession: %v", err)
	}
	if got != "abc123" {
		t.Errorf("currentSession(current) = %q, want abc123", got)
	}

	// Anything else is passed through untouched.
	if got, err := currentSession("some-id"); err != nil || got != "some-id" {
		t.Errorf("currentSession(some-id) = %q, %v", got, err)
	}
	if got, err := currentSession(""); err != nil || got != "" {
		t.Errorf("currentSession(\"\") = %q, %v", got, err)
	}
}

func TestCurrentSessionWithoutAnAgentIsAnError(t *testing.T) {
	for _, key := range []string{
		sessions.EnvSession, sessions.EnvSessionID, sessions.EnvClaudeCode,
		sessions.EnvClaudeCodeSessionID, sessions.EnvClaudeSessionID,
		sessions.EnvCodexThreadID, sessions.EnvCodexSandbox, sessions.EnvCursorAgent,
		sessions.EnvCursorAgentSessionID,
	} {
		t.Setenv(key, "")
	}
	if _, err := currentSession("current"); err == nil {
		t.Error("currentSession(current) outside an agent did not fail")
	}
}

func TestSessionsCommandWithoutADaemonSaysSo(t *testing.T) {
	prev := dialDaemon
	dialDaemon = func(context.Context) (*client.Client, error) { return nil, client.ErrNotRunning }
	t.Cleanup(func() { dialDaemon = prev })

	_, err := sessionsDaemon(context.Background())
	if err == nil {
		t.Fatal("sessionsDaemon without a daemon did not fail")
	}
	if !strings.Contains(err.Error(), "oberth serve") {
		t.Errorf("error = %q, want it to name the command that starts a daemon", err)
	}
}
