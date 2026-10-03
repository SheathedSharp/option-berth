package runsreg

import (
	"sort"
	"strings"

	"github.com/sheathedsharp/option-berth/internal/daemon"
	"github.com/sheathedsharp/option-berth/internal/state"
)

// CaptureGroupStop copies raw identity once. Re-reading RunStart after the port
// pass could target a replacement PID generation instead of the selected run.
func (r *Registry) CaptureGroupStop(group, reason string) daemon.GroupStopCapture {
	if reason == "" {
		reason = ReasonStopped
	}
	capture := daemon.GroupStopCapture{}
	r.mu.Lock()
	for _, rec := range r.runs {
		if !strings.EqualFold(rec.Group, group) {
			continue
		}
		capture.Runs = append(capture.Runs, daemon.GroupStopRun{
			Owner:     state.Run{ID: rec.ID, Group: rec.Group, Name: rec.Name, RootPID: rec.PID},
			StartedAt: rec.StartedAt,
		})
	}
	r.mu.Unlock()
	sort.Slice(capture.Runs, func(i, j int) bool { return capture.Runs[i].Owner.RootPID < capture.Runs[j].Owner.RootPID })
	if len(capture.Runs) == 0 {
		return capture
	}
	expected := make(map[int]Record, len(capture.Runs))
	for _, run := range capture.Runs {
		expected[run.Owner.RootPID] = Record{ID: run.Owner.ID, PID: run.Owner.RootPID, StartedAt: run.StartedAt}
	}
	capture.OnSignal = func(pid int) {
		if run, ok := expected[pid]; ok {
			r.recordStopSignal(run, reason)
		}
	}
	return capture
}
