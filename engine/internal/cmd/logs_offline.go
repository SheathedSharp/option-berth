package cmd

import (
	"context"
	"os"
	"strconv"
	"time"

	"github.com/sheathedsharp/option-berth/engine/internal/runs"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/client"
	"github.com/sheathedsharp/option-berth/engine/internal/runtimesvc/rpc"
)

// offlineRunRecords rebuilds the run list a log read needs from local evidence
// when no runtime service answers: live runs from the mirror, finished runs
// from the read-only store, and the endings the host never observed. The
// mirror is the only copy of a live run that survives a stopped host, so this
// is what lets a log read stay on one generation without the host.
//
// A history read failure is returned with the records that were still
// available; a live run is enough for the default read, while `--run` can say
// why it cannot resolve a finished generation.
func offlineRunRecords(ctx context.Context, group string) ([]rpc.RunRecord, error) {
	records := make([]rpc.RunRecord, 0)
	if snapshot := runs.Load(); snapshot != nil {
		for _, entry := range snapshot.Active() {
			if group != "" && entry.GroupOf() != group {
				continue
			}
			record := rpc.RunRecord{
				ID: entry.ID, PID: entry.PID, Group: entry.GroupOf(), Name: entry.NameOf(),
				Cmd: entry.Cmd, Cwd: entry.Cwd, StartedAt: normalizeStartedAt(entry.StartedAt),
				Ports: []int{}, Status: "running", LogPath: entry.LogPath,
			}
			offset := entry.LogOffset
			record.LogOffset = &offset
			if entry.PortHint > 0 {
				hint := entry.PortHint
				record.PortHint = &hint
				record.URL = "http://localhost:" + strconv.Itoa(hint)
			}
			records = append(records, record)
		}
	}

	var historyErr error
	rows, err := storedRunExits(ctx)
	if err != nil {
		historyErr = err
	} else {
		for _, row := range rows {
			if group != "" && row.Group != group {
				continue
			}
			code := row.Code
			record := rpc.RunRecord{
				ID: row.ID, PID: row.PID, Group: row.Group, Name: row.Name,
				Cmd: row.Cmd, Cwd: row.Cwd, StartedAt: row.StartedAt.UTC().Format(time.RFC3339),
				Ports: []int{}, Status: "exited", ConfigPath: row.ConfigPath,
				StartID: row.StartID, Origin: row.Origin, LogPath: row.LogPath,
				LogOffset: &row.LogOffset, ExitCode: &code, Reason: row.Reason,
				ExitedAt:  row.ExitedAt.UTC().Format(time.RFC3339),
				LastLines: append([]string{}, row.LastLines...),
			}
			if row.PortHint > 0 {
				hint := row.PortHint
				record.PortHint = &hint
				record.URL = "http://localhost:" + strconv.Itoa(hint)
			}
			records = append(records, record)
		}
	}
	records = append(records, unobservedEndings(group)...)
	return dedupeRunRecords(records), historyErr
}

// dedupeRunRecords keeps one record per generation. Durable exits come before
// the derived endings, so the more precise fact wins.
func dedupeRunRecords(records []rpc.RunRecord) []rpc.RunRecord {
	seen := make(map[string]bool, len(records))
	out := make([]rpc.RunRecord, 0, len(records))
	for _, record := range records {
		key := exitRecordKey(record)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, record)
	}
	return out
}

// currentRunForLogPath finds the generation whose bytes the default `logs
// <service>` read should show. It asks the runtime service when one answers
// and uses local evidence otherwise, so a stopped host does not mix the
// current generation with the ones before it. A record whose log path moved or
// whose start offset was never recorded is not usable as a scope.
func currentRunForLogPath(ctx context.Context, c *client.Client, target, logPath string) (rpc.RunRecord, []rpc.RunRecord, bool) {
	rec, records, ok := currentRunForLogs(ctx, c, target)
	if !ok || rec.LogPath != logPath || rec.LogOffset == nil {
		return rpc.RunRecord{}, nil, false
	}
	return rec, records, true
}

// currentRunForLogs resolves the generation a name refers to right now, from the
// one run-set resolver. Both log paths share it so the default read and the
// ad-hoc-name fallback cannot disagree about which generation is "now".
func currentRunForLogs(ctx context.Context, c *client.Client, target string) (rpc.RunRecord, []rpc.RunRecord, bool) {
	wd, err := os.Getwd()
	if err != nil {
		return rpc.RunRecord{}, nil, false
	}
	group, _ := projectAt(wd)
	if group == "" {
		return rpc.RunRecord{}, nil, false
	}
	records, err := worktreeRuns(ctx, c, group)
	if err != nil {
		return rpc.RunRecord{}, nil, false
	}
	rec, err := selectRunForLogs(records, group, target, "current")
	if err != nil {
		return rpc.RunRecord{}, nil, false
	}
	return rec, records, true
}

// recordedRunLogPath answers a name the manifest does not declare. `oberth
// start -d --name X` records the log its run writes, and status publishes that
// name in ad_hoc_runs and in the exit hint `oberth logs X --once`; without this
// the reader is sent to a command that refuses the very name it was told.
// Only a current generation with a recorded path answers: a run that logged
// nowhere has no file to open, and guessing one would invent evidence.
func recordedRunLogPath(ctx context.Context, c *client.Client, group, target string) (string, bool) {
	if group == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", false
		}
		group, _ = projectAt(wd)
	}
	if group == "" {
		return "", false
	}
	records, err := worktreeRuns(ctx, c, group)
	if err != nil {
		return "", false
	}
	rec, err := selectRunForLogs(records, group, target, "current")
	if err != nil || rec.LogPath == "" {
		return "", false
	}
	return rec.LogPath, true
}
