package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sheathedsharp/option-berth/internal/state"
)

func TestNormalizeEventsScopePreservesWorktreeIDs(t *testing.T) {
	id := "0123456789abcdef01234567"
	scope := normalizeEventsScope(state.Scope{Worktrees: []string{id, "", "."}})
	if scope.Worktrees[0] != id {
		t.Fatalf("worktree ID was resolved as a path: %q", scope.Worktrees[0])
	}
	if len(scope.Worktrees) != 2 {
		t.Fatalf("blank selector was retained: %v", scope.Worktrees)
	}
	want, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	if scope.Worktrees[1] != want {
		t.Fatalf("relative path = %q, want %q", scope.Worktrees[1], want)
	}
	if _, err := os.Stat(scope.Worktrees[1]); err != nil {
		t.Fatalf("normalized path is not rooted in the current checkout: %v", err)
	}
}

func TestEventsRecordsFlattenProtocolPayload(t *testing.T) {
	snapshot := state.StreamSnapshot{
		Type:          "state.snapshot",
		Seq:           4,
		StateRevision: "4",
		ObservedAt:    "now",
		Worktrees:     []state.StreamWorktree{},
	}
	raw, err := json.Marshal(eventsSnapshotRecord{StreamSnapshot: snapshot})
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, `"snapshot"`) || !strings.Contains(text, `"type":"state.snapshot"`) {
		t.Fatalf("snapshot record is nested or missing type: %s", text)
	}

	change := state.StateChanged{
		Type: "state.changed", EventID: "e", Seq: 5, StateRevision: "5",
		Changed: []string{"port_changed"}, ObservedAt: "later",
	}
	raw, err = json.Marshal(eventsChangedRecord{StateChanged: change})
	if err != nil {
		t.Fatal(err)
	}
	text = string(raw)
	if strings.Contains(text, `"changed":{`) || !strings.Contains(text, `"type":"state.changed"`) {
		t.Fatalf("changed record is nested or missing type: %s", text)
	}
}
