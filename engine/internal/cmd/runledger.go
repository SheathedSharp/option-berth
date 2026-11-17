package cmd

import (
	"context"
	"strconv"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
	"github.com/sheathedsharp/option-berth/engine/internal/store"
)

// The run ledger has two halves on disk: live runs in the runs mirror and ended
// runs in the store. A command that answers without a runtime service reads both
// through the conversions below, so one recorded run looks the same to every
// reader no matter which half it came from.

// liveRunRecord is one recorded live run as the wire record. runs.Load has
// already pruned every dead pid, so what is left is a process that is running.
//
// This is the only conversion of a live entry: the log paths, the status
// document and the restart hint all read a run through it, so a field added
// here reaches every reader at once. A second conversion is how `logs --run`
// once lost the log offset that `logs` had.
func liveRunRecord(entry runs.Entry) rpc.RunRecord {
	record := rpc.RunRecord{
		ID: entry.ID, PID: entry.PID,
		Group: entry.GroupOf(), Name: entry.NameOf(),
		Cmd: entry.Cmd, Argv: append([]string(nil), entry.Argv...), Cwd: entry.Cwd,
		StartedAt: normalizeStartedAt(entry.StartedAt), Status: "running",
		Ports: []int{}, LogPath: entry.LogPath,
	}
	offset := entry.LogOffset
	record.LogOffset = &offset
	if entry.PortHint > 0 {
		hint := entry.PortHint
		record.PortHint = &hint
		record.URL = "http://localhost:" + strconv.Itoa(hint)
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
	records, _ := worktreeRuns(ctx, nil, "")
	return records
}

// worktreeRuns is the single answer to "which runs belong to this worktree".
// Every command reads it instead of re-deriving the set, because a command that
// derives its own answer can disagree with the command next to it: `logs` once
// refused a name `status` had just printed, and `status --brief` could not name
// a run `restart --run` could replay. Those were not three bugs; they were three
// copies of this question.
//
// The runtime service owns the answer while it answers — it is the one writer of
// run state — and the mirror plus the store are the same answer when it does not.
// group scopes the answer to one worktree; "" asks for every run the ledger
// holds, which only a caller explaining a refusal about an unknown project does.
//
// A nil client means the caller must not reach a host (explaining a refusal is no
// reason to start one). Otherwise a host failure is returned rather than
// silently answered from a possibly staler local copy.
func worktreeRuns(ctx context.Context, c *client.Client, group string) ([]rpc.RunRecord, error) {
	if c == nil {
		return offlineRunRecords(ctx, group)
	}
	var listed rpc.RunsListResult
	if err := c.Call(ctx, "runs.list", rpc.Empty{}, &listed); err != nil {
		return nil, cliError(err)
	}
	records := append(append([]rpc.RunRecord{}, listed.Runs...), listed.Exited...)
	if group == "" {
		return records, nil
	}
	out := records[:0]
	for _, record := range records {
		if record.Group == group {
			out = append(out, record)
		}
	}
	return out, nil
}
