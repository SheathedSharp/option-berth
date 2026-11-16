package cmd

import (
	"strconv"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The run ledger has two halves on disk: live runs in the runs mirror and ended
// runs in the store. A command that answers without a runtime service reads both
// through the conversions below, so one recorded run looks the same to every
// reader no matter which half it came from.

// exitRunRecord is one durable ended run as the wire record.
func exitRunRecord(row store.RunExitRow) rpc.RunRecord {
	code := row.Code
	record := rpc.RunRecord{
		ID: row.ID, PID: row.PID, Group: row.Group, Name: row.Name,
		Cmd: row.Cmd, Cwd: row.Cwd, StartedAt: row.StartedAt.UTC().Format(time.RFC3339),
		Ports: []int{}, Status: "exited", ConfigPath: row.ConfigPath,
		StartID: row.StartID, Origin: row.Origin, LogPath: row.LogPath,
		LogOffset: &row.LogOffset,
		ExitCode:  &code, Reason: row.Reason,
		ExitedAt:  row.ExitedAt.UTC().Format(time.RFC3339),
		LastLines: append([]string{}, row.LastLines...),
	}
	if row.PortHint > 0 {
		hint := row.PortHint
		record.PortHint = &hint
		record.URL = "http://localhost:" + strconv.Itoa(hint)
	}
	return record
}
