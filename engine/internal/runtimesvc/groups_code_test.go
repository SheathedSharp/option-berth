package runtimesvc

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/state"
)

func TestServiceCodesKeepsRunningRowsAndLeavesUnknownUnknown(t *testing.T) {
	runID := "run-api"
	head := "0123456"
	changed := true
	files := 2
	hint := "oberth restart --only api"
	execPath := "/work/demo/bin/api"
	execAt := "2026-11-16T09:00:00Z"
	execChanged := true
	group := state.Group{Services: []state.Service{
		{Name: "api", Running: true, RunID: &runID,
			CodeHead: &head, CodeChangedSinceStart: &changed,
			CodeChangedFiles: &files, CodeHint: &hint,
			ExecutablePath: &execPath, ExecutableModifiedAt: &execAt,
			ExecutableChangedSinceStart: &execChanged},
		{Name: "worker", Running: true, RunID: strPtr("run-worker")},
		{Name: "old", Running: false, RunID: strPtr("run-old")},
	}}
	rows := serviceCodes(group)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want the two running services", rows)
	}
	if rows[0].Name != "api" || rows[0].RunID != runID ||
		rows[0].Changed == nil || !*rows[0].Changed ||
		rows[0].ChangedFiles == nil || *rows[0].ChangedFiles != files ||
		rows[0].Hint == nil || *rows[0].Hint != hint ||
		rows[0].CodeHead == nil || *rows[0].CodeHead != head ||
		rows[0].ExecutablePath == nil || *rows[0].ExecutablePath != execPath ||
		rows[0].ExecutableModifiedAt == nil || *rows[0].ExecutableModifiedAt != execAt ||
		rows[0].ExecutableChangedSinceStart == nil || !*rows[0].ExecutableChangedSinceStart {
		t.Fatalf("api row = %+v", rows[0])
	}
	if rows[1].Name != "worker" || rows[1].Changed != nil ||
		rows[1].ChangedFiles != nil || rows[1].Hint != nil ||
		rows[1].ExecutablePath != nil || rows[1].ExecutableChangedSinceStart != nil {
		t.Fatalf("worker row = %+v, want an explicit unknown row", rows[1])
	}
}

func TestGroupsCodeRejectsAnEmptyName(t *testing.T) {
	_, err := handleGroupsCode(context.Background(), &Request{
		Method: "groups.code",
		Params: json.RawMessage(`{"name":"  "}`),
	})
	var failure *rpc.Error
	if !errors.As(err, &failure) || failure.Code != rpc.CodeInvalidParams {
		t.Fatalf("err = %v, want invalid_params", err)
	}
}

func strPtr(s string) *string { return &s }
