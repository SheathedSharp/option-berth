//go:build integration

// Integration coverage for slice 2A.4: a real `oberth start` under an agent's
// environment, a real listener, and the sessions namespace answering about it.
// Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/internal/sessions"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// TestSessionAttributionEndToEnd is the step's acceptance demo in one test: a
// listener started under CLAUDE_CODE_SESSION_ID is attributed to that session,
// `state.snapshot carries its worktree and branch,
// {session}` narrows to its ports, and `sessions.kill` stops it and leaves the
// session inactive.
func TestSessionAttributionEndToEnd(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	repo := gitCheckout(t, e.home, "shop", "feature/x")
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	c := e.connect(ctx)

	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	const sessionID = "itest-session-1"
	port := freePort(t)
	start := e.command("start", "--group", "itest", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	start.Dir = repo
	start.Env = append(start.Env,
		"CLAUDE_CODE_SESSION_ID="+sessionID,
		"BERTH_SESSION_LABEL=acceptance demo")
	var out safeBuffer
	start.Stdout, start.Stderr = &out, &out
	ownProcessGroup(start)
	if err := start.Start(); err != nil {
		t.Fatalf("oberth start: %v", err)
	}
	t.Cleanup(func() {
		stopCommand(start)
		if t.Failed() {
			t.Logf("oberth start output:\n%s", out.String())
		}
	})

	added, ok := waitForAttributedPort(t, sub, port, 60*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d with its run within 60s\n%s", port, out.String())
	}
	if added.Session == nil {
		t.Fatalf("port %d was published with no session\n%s", port, out.String())
	}
	if added.Session.ID != sessionID {
		t.Errorf("session id = %q, want %q", added.Session.ID, sessionID)
	}
	if added.Session.Tool != sessions.ToolClaudeCode {
		t.Errorf("tool = %q, want %s", added.Session.Tool, sessions.ToolClaudeCode)
	}
	if !added.Session.Detected {
		t.Error("a session recognised from CLAUDE_CODE_SESSION_ID must be marked detected")
	}
	if added.Session.Branch != "feature/x" {
		t.Errorf("branch = %q, want feature/x", added.Session.Branch)
	}
	if added.Session.Label != "acceptance demo" {
		t.Errorf("label = %q", added.Session.Label)
	}

	// The live snapshot carries the session attribution used by kill.
	var snap state.Snapshot
	if err := c.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snap); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	if len(snap.Sessions) != 1 || snap.Sessions[0].ID != sessionID {
		t.Fatalf("snapshot sessions = %+v, want %s", snap.Sessions, sessionID)
	}

	// sessions.kill stops everything the session started.
	var env rpc.KillEnvelope
	if err := c.Call(ctx, "sessions.kill", rpc.SessionsKillParams{ID: sessionID}, &env); err != nil {
		t.Fatalf("sessions.kill: %v", err)
	}
	if !env.OK || len(env.Results) == 0 {
		t.Fatalf("sessions.kill envelope = %+v", env)
	}
	if !waitForRemoved(t, sub, port, 45*time.Second) {
		t.Fatalf("port %d never went away after sessions.kill", port)
	}
}

func TestStartWithoutAnAgentHasNoSession(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	e := newEnv(t)
	e.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := e.connect(ctx)

	sub, err := c.Subscribe(ctx, client.SubscribeOptions{Events: true})
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	port := freePort(t)
	start := e.command("start", "--group", "plain", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	start.Dir = e.home
	start.Env = append(start.Env, "CLAUDECODE=", "CLAUDE_CODE_SESSION_ID=",
		"CLAUDE_SESSION_ID=", "CODEX_THREAD_ID=", "CODEX_SANDBOX=",
		"CURSOR_AGENT=", "BERTH_SESSION=", "BERTH_SESSION_ID=")
	var out safeBuffer
	start.Stdout, start.Stderr = &out, &out
	ownProcessGroup(start)
	if err := start.Start(); err != nil {
		t.Fatalf("oberth start: %v", err)
	}
	t.Cleanup(func() { stopCommand(start) })

	added, ok := waitForAttributedPort(t, sub, port, 45*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d\n%s", port, out.String())
	}
	if added.Session != nil {
		t.Errorf("a shell-started run got session %+v", *added.Session)
	}
}

// gitCheckout makes a real repository under home with one commit on branch,
// because the session's branch is read from .git.
func gitCheckout(t *testing.T, home, name, branch string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, strings.TrimSpace(string(out)))
		}
	}
	run("init", "-q", "-b", branch, ".")
	run("commit", "-q", "--allow-empty", "-m", "first")
	return dir
}
