//go:build integration

// End-to-end coverage for session attribution: a real `oberth start` under an
// agent's environment, a real listener, and the sessions namespace answering
// about both. Run with `go test -tags integration ./internal/daemon/...`.
package daemon_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/daemon/client"
	"github.com/sheathedsharp/option-berth/engine/internal/daemon/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/sessions"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

// TestSessionAttributionEndToEnd follows one run from spawn to kill: a
// listener started under CLAUDE_CODE_SESSION_ID must appear with that session,
// its worktree and its branch; the snapshot must carry the collection; and
// `sessions.kill` must stop the port and leave the session inactive.
func TestSessionAttributionEndToEnd(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	env := newEnv(t)
	repo := checkoutWithBranch(t, env.home, "shop", "feature/x")
	env.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	api := env.connect(ctx)

	events, err := api.Subscribe(ctx, subscribeOptions())
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	const sessionID = "itest-session-1"
	port := freePort(t)
	command := env.command("start", "--group", "itest", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	command.Dir = repo
	command.Env = append(command.Env,
		"CLAUDE_CODE_SESSION_ID="+sessionID,
		"BERTH_SESSION_LABEL=acceptance demo")
	output := startTracked(t, command)

	added, ok := waitForAttributedPort(t, events, port, 60*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d with its run within 60s\n%s", port, output.String())
	}
	if added.Session == nil {
		t.Fatalf("port %d was published with no session\n%s", port, output.String())
	}
	want := state.Session{
		ID:       sessionID,
		Tool:     sessions.ToolClaudeCode,
		Label:    "acceptance demo",
		Branch:   "feature/x",
		Detected: true,
	}
	if !reflect.DeepEqual(*added.Session, want) {
		t.Errorf("session = %+v, want %+v", *added.Session, want)
	}

	var snapshot state.Snapshot
	if err := api.Call(ctx, "state.snapshot", rpc.StateSnapshotParams{}, &snapshot); err != nil {
		t.Fatalf("state.snapshot: %v", err)
	}
	if len(snapshot.Sessions) != 1 || snapshot.Sessions[0].ID != sessionID {
		t.Fatalf("snapshot sessions = %+v, want %s", snapshot.Sessions, sessionID)
	}

	var envelope rpc.KillEnvelope
	if err := api.Call(ctx, "sessions.kill", rpc.SessionsKillParams{ID: sessionID}, &envelope); err != nil {
		t.Fatalf("sessions.kill: %v", err)
	}
	if !envelope.OK || len(envelope.Results) == 0 {
		t.Fatalf("sessions.kill envelope = %+v", envelope)
	}
	if !waitForRemoved(t, events, port, 45*time.Second) {
		t.Fatalf("port %d never went away after sessions.kill", port)
	}
}

// TestStartWithoutAnAgentHasNoSession is the negative half: the same flow with
// every marker cleared must publish no session at all.
func TestStartWithoutAnAgentHasNoSession(t *testing.T) {
	listener, err := buildListener()
	if err != nil {
		t.Fatal(err)
	}

	env := newEnv(t)
	env.serve()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	api := env.connect(ctx)

	events, err := api.Subscribe(ctx, subscribeOptions())
	if err != nil {
		t.Fatalf("state.subscribe: %v", err)
	}

	port := freePort(t)
	command := env.command("start", "--group", "plain", "--name", "web",
		"--port", strconv.Itoa(port), "--", listener, strconv.Itoa(port))
	command.Dir = env.home
	command.Env = append(command.Env,
		"CLAUDECODE=", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_SESSION_ID=",
		"CODEX_THREAD_ID=", "CODEX_SANDBOX=", "CURSOR_AGENT=",
		"BERTH_SESSION=", "BERTH_SESSION_ID=")
	output := startTracked(t, command)

	added, ok := waitForAttributedPort(t, events, port, 45*time.Second)
	if !ok {
		t.Fatalf("no delta carried port %d\n%s", port, output.String())
	}
	if added.Session != nil {
		t.Errorf("a shell-started run got session %+v", *added.Session)
	}
}

func subscribeOptions() client.SubscribeOptions {
	return client.SubscribeOptions{Events: true}
}

// startTracked starts a command in its own process group and guarantees it is
// stopped and its output is available when the test ends.
func startTracked(t *testing.T, command *exec.Cmd) *safeBuffer {
	t.Helper()
	var output safeBuffer
	command.Stdout, command.Stderr = &output, &output
	ownProcessGroup(command)
	if err := command.Start(); err != nil {
		t.Fatalf("starting %v: %v", command.Args, err)
	}
	t.Cleanup(func() {
		stopCommand(command)
		if t.Failed() {
			t.Logf("command output:\n%s", output.String())
		}
	})
	return &output
}

// checkoutWithBranch makes a real repository under home with one commit on
// the named branch, because the session's branch is read from .git.
func checkoutWithBranch(t *testing.T, home, name, branch string) string {
	t.Helper()
	dir := filepath.Join(home, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(cmd.Environ(),
			"GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid",
			"GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Skipf("git %v: %v\n%s", args, err, strings.TrimSpace(string(output)))
		}
	}
	git("init", "-q", "-b", branch, ".")
	git("commit", "-q", "--allow-empty", "-m", "fixture")
	return dir
}
