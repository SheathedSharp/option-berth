package cmd

import (
	"context"
	"strconv"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The run ledger has two halves on disk: live runs in the runs mirror and ended
// runs in the store. A command that answers without a runtime service reads both
// through the conversions below, so one recorded run looks the same to every
// reader no matter which half it came from.

// liveRunRecord is one recorded live run as the wire record. runs.Load has
// already pruned every dead pid, so what is left is a process that is running.
func liveRunRecord(entry runs.Entry) rpc.RunRecord {
	record := rpc.RunRecord{
		ID: entry.ID, PID: entry.PID,
		Group: entry.GroupOf(), Name: entry.NameOf(),
		Cmd: entry.Cmd, Argv: append([]string(nil), entry.Argv...), Cwd: entry.Cwd,
		StartedAt: entry.StartedAt, Status: "running",
	}
	if entry.PortHint > 0 {
		hint := entry.PortHint
		record.PortHint = &hint
	}
	return record
}

// exitRunRecord is one durable ended run as the wire record.
func exitRunRecord(row store.RunExitRow) rpc.RunRecord {
	code := row.Code
	record := rpc.RunRecord{
		ID: row.ID, PID: row.PID, Group: row.Group, Name: row.Name,
		Cmd: row.Cmd, Argv: append([]string(nil), row.Argv...),
		Cwd: row.Cwd, StartedAt: row.StartedAt.UTC().Format(time.RFC3339),
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

// runLedgerRecords is the whole ledger as records: live runs from the mirror,
// ended runs from the store. It never starts a runtime service — explaining a
// refusal is no reason to bring one up — and never takes the write path; the
// ended half is read through a runtime service that already answers, or from the
// store read-only when none does.
//
// The ended half only sharpens an answer the caller already has: an unreadable
// store leaves the live records standing rather than replacing the refusal the
// caller asked for with a storage error.
func runLedgerRecords(ctx context.Context) []rpc.RunRecord {
	registry := runs.Load()
	records := make([]rpc.RunRecord, 0, len(registry.Runs))
	for _, entry := range registry.Active() {
		records = append(records, liveRunRecord(entry))
	}
	exits, err := storedRunExits(ctx)
	if err != nil {
		return records
	}
	for _, row := range exits {
		records = append(records, exitRunRecord(row))
	}
	return records
}
