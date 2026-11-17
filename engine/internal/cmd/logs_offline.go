package cmd

import (
	"context"
	"os"

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
			records = append(records, liveRunRecord(entry))
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
			records = append(records, exitRunRecord(row))
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

// currentRunForLogs resolves the generation a name refers to right now, from the
// one run-set resolver. Every log reader goes through it, so the default read
// and the ad-hoc-name fallback cannot disagree about which generation is "now".
// group "" means the checkout the command runs in; a target outside any project
// has no current run to resolve.
//
// It answers unconditionally on purpose: what makes a generation usable as a
// scope (a matching path, a recorded start) is the caller's rule, and the two
// callers have different rules.
func currentRunForLogs(ctx context.Context, c *client.Client, group, target string) (rpc.RunRecord, []rpc.RunRecord, bool) {
	if group == "" {
		wd, err := os.Getwd()
		if err != nil {
			return rpc.RunRecord{}, nil, false
		}
		group, _ = projectAt(wd)
	}
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
