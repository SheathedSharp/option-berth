package cmd

import (
	"github.com/sheathedsharp/option-berth/engine/internal/agentstream"
	"github.com/sheathedsharp/option-berth/engine/internal/display"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"strings"
	"testing"
)

func TestAgentRuntimeContextUsesOnlyExplicitWorktreeTargets(t *testing.T) {
	id := "live-api"
	doc := statusDocument{Scope: display.Scope{Root: "/synthetic/a"}, Worktree: state.Group{Services: []state.Service{{Name: "api", Running: true, RunID: &id}, {Name: "unrelated", Running: true}}}, Runs: []rpc.RunRecord{{ID: "owned-worker", Name: "worker", Status: "running"}, {ID: "foreign-worker", Name: "private-other", Status: "running"}}}
	scope := agentstream.RuntimeTask{Services: []string{"api"}, RunIDs: []string{"owned-worker"}}
	text, err := agentRuntimeContext(doc, "/synthetic/a", scope)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "live-api") || !strings.Contains(text, "owned-worker") || strings.Contains(text, "unrelated") || strings.Contains(text, "private-other") {
		t.Fatalf("wrong scope %s", text)
	}
	if _, err := agentRuntimeContext(doc, "/synthetic/b", scope); err == nil {
		t.Fatal("cross-worktree context accepted")
	}
	scope.RunIDs = []string{"new-generation"}
	if _, err := agentRuntimeContext(doc, "/synthetic/a", scope); err == nil {
		t.Fatal("unrecorded replacement adopted")
	}
}
