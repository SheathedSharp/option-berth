package cmd

import (
	"encoding/json"
	"fmt"
	"github.com/sheathedsharp/option-berth/engine/internal/attn"
	"github.com/sheathedsharp/option-berth/engine/internal/attn/model"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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

// A listener blind spot must not erase an owner's positive exit evidence or
// change a declared service event into an exact-run-only event.
func TestClaudeDeclaredExitSurvivesObservationDegradation(t *testing.T) {
	for _, obs := range []rpc.ObservationStatus{{State: "observable"}, {State: "blind"}, {State: "partial"}, {State: "observable", Source: "lsof"}} {
		for _, reason := range []string{"", "crashed", "start_failed", "ready_timeout", "dependency_timeout", "stopped"} {
			t.Run(obs.State+"/"+obs.Source+"/"+reason, func(t *testing.T) {
				base := t.TempDir()
				t.Setenv("HOME", filepath.Join(base, "home"))
				t.Setenv("BERTH_HOME", filepath.Join(base, "state"))
				if e := os.MkdirAll(filepath.Join(base, "home"), 0700); e != nil {
					t.Fatal(e)
				}
				var doc statusDocument
				doc.Scope.Root = filepath.Join(base, "project")
				doc.Observation = &obs
				service := state.Service{Name: "api"}
				at := time.Now().UTC().Format(time.RFC3339Nano)
				if reason != "" {
					code := 2
					if reason == "stopped" {
						code = 0
					}
					service.LastExit = &state.ServiceExit{Code: code, Reason: reason, At: at, RunID: "known-api-run"}
					doc.attentionExits = []rpc.RunRecord{{ID: "known-api-run", Name: "api", Status: "exited", ExitCode: &code, Reason: reason, ExitedAt: at}}
				}
				doc.Worktree.Services = []state.Service{service}
				path := publishAttention(&doc)
				if reason == "" || reason == "stopped" {
					if path != "" {
						t.Fatalf("absence/normal stop invented anomaly: %+v", doc.Attn)
					}
					return
				}
				artifact, e := attn.Read(path)
				if e != nil {
					t.Fatal(e)
				}
				selected, ok := attn.Select(artifact, []string{"api"}, nil)
				if !ok || len(selected.Events) != 1 || selected.Events[0].RunOnly || selected.Events[0].RunID != "known-api-run" {
					t.Fatalf("known declared failure lost to service filter: selected=%+v original=%+v", selected.Events, artifact.Events)
				}
			})
		}
	}
}
