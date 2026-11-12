package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/runner"
)

// `oberth attn` carries the runner the standalone binary used to own. What has to
// stay true: it lives in the Handoff layer, it refuses to guess an
// authorization, and being disabled never invents an answer.

func TestAttnIsInTheHandoffLayer(t *testing.T) {
	if attnCmd.GroupID != commandGroupHandoff {
		t.Fatalf("jev group = %q, want %q", attnCmd.GroupID, commandGroupHandoff)
	}
	// A model-mediated handoff is not a fact command and not a control command:
	// pinning the absence here is what keeps a future contributor from
	// reclassifying it to make an unrelated test pass.
	for _, forbidden := range []string{commandGroupFacts, commandGroupOperations} {
		if attnCmd.GroupID == forbidden {
			t.Fatalf("jev is in %q; it hands a choice to a person", forbidden)
		}
	}
}

func TestAttnSurfaceIsTheRunnerAndSetupVerbs(t *testing.T) {
	want := map[string]bool{
		// the runner: what an agent calls while working
		"assess": false, "session": false, "feedback": false, "report": false,
		// setup and explanation: what a person calls once
		"status": false, "enable": false, "test": false, "disable": false, "preview": false,
	}
	for _, sub := range attnCmd.Commands() {
		if _, ok := want[sub.Name()]; !ok {
			t.Errorf("unexpected jev subcommand %q", sub.Name())
			continue
		}
		want[sub.Name()] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("jev %s is missing", name)
		}
	}
}

// The authorization is the agent's, never the artifact's. A missing allow-list
// has to fail as a usage error, because the alternative — guessing from the
// candidate options — would turn publication into permission.
func TestAttnRequiresAnExplicitAuthorization(t *testing.T) {
	t.Cleanup(func() {
		attnTaskFlag, attnTaskFileFlag, attnAuthorizeFlag = "", "", nil
	})
	attnTaskFlag = "get the worker running"
	attnTaskFileFlag = ""
	attnAuthorizeFlag = nil
	if _, err := attnTaskContext(); err == nil {
		t.Fatal("a task without --authorize was accepted")
	}

	attnTaskFlag = ""
	attnAuthorizeFlag = []string{"inspect_logs"}
	if _, err := attnTaskContext(); err == nil {
		t.Fatal("an allow-list without a task was accepted")
	}

	attnTaskFlag = "get the worker running"
	task, err := attnTaskContext()
	if err != nil {
		t.Fatal(err)
	}
	if task.Task != "get the worker running" || len(task.AuthorizedOptions) != 1 {
		t.Fatalf("task context = %+v", task)
	}
}

func TestAttnTaskContextFileAndFlagOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "task.json")
	if err := os.WriteFile(path, []byte(`{"task":"old","authorized_options":["inspect_logs"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		attnTaskFlag, attnTaskFileFlag, attnAuthorizeFlag = "", "", nil
	})
	attnTaskFileFlag = path
	attnTaskFlag = "new"
	attnAuthorizeFlag = []string{"restart_service", "inspect_manifest"}
	task, err := attnTaskContext()
	if err != nil {
		t.Fatal(err)
	}
	if task.Task != "new" {
		t.Fatalf("task = %q, want the flag to win", task.Task)
	}
	if len(task.AuthorizedOptions) != 2 || task.AuthorizedOptions[0] != "restart_service" {
		t.Fatalf("authorized options = %v", task.AuthorizedOptions)
	}
}

// A disabled configuration resolves to no client, and the command reports that
// fixed reason instead of pretending the provider failed.
func TestDisabledConsentProducesNoClient(t *testing.T) {
	home := t.TempDir()
	t.Setenv("BERTH_HOME", home)
	t.Setenv("BERTH_ATTN_KEY", "environment-only-key")
	t.Setenv("BERTH_ATTN", "")

	consent := attnResolvedConsent()
	if consent.Enabled {
		t.Fatalf("an environment key enabled the assessment: %+v", consent)
	}
	if _, err := consent.Client(); err == nil {
		t.Fatal("a disabled configuration produced a client")
	}
	if got := consent.ReasonOr(nil); got != model.ReasonDisabled {
		t.Fatalf("reason = %q, want %q", got, model.ReasonDisabled)
	}
}

// The output the agent parses must not carry an absolute worktree path: the
// same redaction rule that keeps it out of telemetry keeps it out of stdout.
func TestAttnOutputNeverCarriesTheWorktreeRoot(t *testing.T) {
	var buf strings.Builder
	output := runner.Output{Status: "clear", WorktreeID: "abc123", WorktreeRoot: "/Users/alice/work/repo"}
	if err := writeAttnOutput(&buf, output); err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(buf.String()), &decoded); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if _, ok := decoded["WorktreeRoot"]; ok {
		t.Fatalf("output exposes the worktree root: %s", buf.String())
	}
	if _, ok := decoded["worktree_id"]; ok {
		return
	}
	t.Fatal("the stable worktree id is missing from the output")
}
