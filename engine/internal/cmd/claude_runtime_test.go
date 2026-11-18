package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"strings"
	"testing"
)

func TestClaudeRuntimePreservesUnknownObservation(t *testing.T) {
	for _, obs := range []*rpc.ObservationStatus{{State: "blind", Code: "observation_blind"}, {State: "partial"}, {State: "observable", Source: "lsof"}} {
		var doc statusDocument
		doc.Scope.Root = "/test/tree"
		doc.Worktree.Services = []state.Service{{Name: "api"}}
		doc.Observation = obs
		data, err := claudeRuntimeContext(doc, "/test/tree", model.TaskContext{Services: []string{"api"}})
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(data, `"running":false`) {
			t.Fatalf("blind listener projected as stopped: %s", data)
		}
		var out map[string]any
		if json.Unmarshal([]byte(data), &out) != nil || out["observation"] == nil {
			t.Fatalf("missing coverage: %s", data)
		}
	}
}

func TestClaudeRuntimeExactExitSurvivesDisplayCap(t *testing.T) {
	var doc statusDocument
	doc.Scope.Root = "/test/tree"
	code := 2
	for i := 0; i < 8; i++ {
		doc.attentionExits = append(doc.attentionExits, rpc.RunRecord{ID: fmt.Sprint(i), Name: "worker", Status: "exited", ExitCode: &code})
	}
	doc.Exits = statusExitDisplay(doc.attentionExits)
	if len(doc.Exits) != 5 {
		t.Fatal("display contract changed")
	}
	data, err := claudeRuntimeContext(doc, "/test/tree", model.TaskContext{RunIDs: []string{"7"}})
	if err != nil || !strings.Contains(data, `"run_id":"7"`) || !strings.Contains(data, `"running":false`) {
		t.Fatalf("precise run lost after five newer exits: %s %v", data, err)
	}
	if _, err := claudeRuntimeContext(doc, "/other/tree", model.TaskContext{RunIDs: []string{"7"}}); err == nil {
		t.Fatal("cross-worktree facts accepted")
	}
}
